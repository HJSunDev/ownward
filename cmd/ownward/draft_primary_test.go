package main

import (
	"context"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/ownerview"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func draftCall(t *testing.T, client *mcp.ClientSession, in contract.AgentDraftRequest, ok bool) contract.AgentDraftResult {
	t.Helper()
	r := hostCall(t, client, "ownward_draft_work", in, ok)
	var out contract.AgentDraftResult
	if ok {
		if e := decodeTool(r, &out); e != nil {
			t.Fatal(e)
		}
	}
	return out
}

var primaryPermissions = []contract.Permission{contract.ReadPermission, contract.MaintainPermission, contract.ManagePermission}

func TestDraftListDoesNotDeliverDiscardedMetadata(t *testing.T) {
	f := ownerHTTP(t)
	p, token, primary := f.agent(t, "Main")
	if e := f.c.SetPermissions(f.ctx, p.ID, primaryPermissions); e != nil {
		t.Fatal(e)
	}
	d := draftCall(t, primary, contract.AgentDraftRequest{Action: "create", Text: "private"}, true)
	view, e := ownerview.New(f.s, f.c, f.p)
	if e != nil {
		t.Fatal(e)
	}
	ctx := informationcontrol.Authenticate(context.Background(), token)
	in := contract.AgentDraftRequest{Action: "list"}
	page, e := view.DraftWork(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	draftCall(t, primary, contract.AgentDraftRequest{Action: "discard", Draft: d.Draft, Handle: d.Handle}, true)
	if e = view.CheckDraftWork(ctx, in, page.Handle); e == nil {
		t.Fatal("discarded draft metadata delivered from an in-flight list")
	}
}

// No window calls after fixture setup: writing, delegation, approval and
// publication all complete through the authenticated, host-neutral MCP tools.
func TestPrimaryDraftJourneyWithoutWindow(t *testing.T) {
	f := ownerHTTP(t)
	p, token, primary := f.agent(t, "Main")
	if e := f.c.SetPermissions(f.ctx, p.ID, primaryPermissions); e != nil {
		t.Fatal(e)
	}
	_, _, helper := f.agent(t, "Helper")
	d := draftCall(t, primary, contract.AgentDraftRequest{Action: "create", Text: "first"}, true)
	page := draftCall(t, primary, contract.AgentDraftRequest{Action: "list", Limit: 1}, true)
	if len(page.Drafts) != 1 || page.Drafts[0].ID != d.Draft {
		t.Fatal(page)
	}
	inv := draftCall(t, primary, contract.AgentDraftRequest{Action: "invite", Draft: d.Draft, Handle: d.Handle}, true)
	ready := draftCall(t, primary, contract.AgentDraftRequest{Action: "request", System: f.c.SystemID(), Invitation: inv.Invitation.ID}, true)
	if ready.State != "ready" || ready.Request != "" || ready.Content.Text != "first" {
		t.Fatal(ready)
	}
	pending := draftCall(t, primary, contract.AgentDraftRequest{Action: "requests"}, true)
	if len(pending.Requests) != 0 {
		t.Fatal("primary requested approval", pending)
	}
	r := draftCall(t, helper, contract.AgentDraftRequest{Action: "request", System: f.c.SystemID(), Invitation: inv.Invitation.ID}, true)
	for _, action := range []string{"list", "create", "requests", "publish", "discard", "invite", "decide", "end", "cancel"} {
		draftCall(t, helper, contract.AgentDraftRequest{Action: action, Draft: d.Draft, Request: r.Request, Handle: d.Handle, Invitation: inv.Invitation.ID}, false)
	}
	pending = draftCall(t, primary, contract.AgentDraftRequest{Action: "requests", Request: r.Request}, true)
	if len(pending.Requests) != 1 || pending.Requests[0].Subject != "Helper" || pending.Requests[0].Verification != r.Verification {
		t.Fatal(pending)
	}
	decision := pending.Requests[0]
	draftCall(t, primary, contract.AgentDraftRequest{Action: "decide", Request: r.Request, Handle: decision.Handle, Accept: true}, true)
	read := draftCall(t, helper, contract.AgentDraftRequest{Action: "read", Request: r.Request}, true)
	draftCall(t, helper, contract.AgentDraftRequest{Action: "append", Request: r.Request, Handle: read.Handle, Text: " + helper"}, true)
	draftCall(t, primary, contract.AgentDraftRequest{Action: "replace", Draft: d.Draft, Handle: d.Handle, Text: "stale"}, false)
	final := draftCall(t, primary, contract.AgentDraftRequest{Action: "read", Draft: d.Draft}, true)
	if final.Content.Text != "first + helper" {
		t.Fatal(final)
	}
	pub := contract.AgentDraftRequest{Action: "publish", Draft: d.Draft, Handle: final.Handle, Operation: "primary-publish"}
	asset := draftCall(t, primary, pub, true)
	retry := draftCall(t, primary, pub, true)
	if asset.Asset == nil || retry.Asset == nil || *asset.Asset != *retry.Asset {
		t.Fatal("publication not recoverable", asset, retry)
	}
	// A new service instance has a new handle key. Read-only recovery uses the
	// durable receipt, not the consumed draft or an old process's handle.
	restarted, e := ownerview.New(f.s, f.c, f.p)
	if e != nil {
		t.Fatal(e)
	}
	ctx := informationcontrol.Authenticate(context.Background(), token)
	recovered, e := restarted.DraftWork(ctx, contract.AgentDraftRequest{Action: "publication", Operation: pub.Operation})
	if e != nil || recovered.Asset == nil || *recovered.Asset != *asset.Asset {
		t.Fatal("restart lost publication", recovered, e)
	}
	if e = restarted.CheckDraftWork(ctx, contract.AgentDraftRequest{Action: "publication", Operation: pub.Operation}, recovered.Handle); e != nil {
		t.Fatal(e)
	}
	draftCall(t, helper, contract.AgentDraftRequest{Action: "read", Request: r.Request}, false)
	if page = draftCall(t, primary, contract.AgentDraftRequest{Action: "list"}, true); len(page.Drafts) != 0 {
		t.Fatal(page)
	}
	// Editing an existing asset retains the source and versioned publish path.
	edit := draftCall(t, primary, contract.AgentDraftRequest{Action: "create", Target: *asset.Asset}, true)
	text := draftCall(t, primary, contract.AgentDraftRequest{Action: "read", Draft: edit.Draft}, true)
	if text.Content.Text != "first + helper" {
		t.Fatal(text)
	}
	draftCall(t, primary, contract.AgentDraftRequest{Action: "discard", Draft: edit.Draft, Handle: edit.Handle}, true)
}

func TestPrimaryAuthorityAndDelegationCannotSurviveRevocation(t *testing.T) {
	f := ownerHTTP(t)
	p, token, primary := f.agent(t, "Main")
	if e := f.c.SetPermissions(f.ctx, p.ID, primaryPermissions); e != nil {
		t.Fatal(e)
	}
	_, _, helper := f.agent(t, "Helper")
	d := draftCall(t, primary, contract.AgentDraftRequest{Action: "create", Text: "private"}, true)
	inv := draftCall(t, primary, contract.AgentDraftRequest{Action: "invite", Draft: d.Draft, Handle: d.Handle}, true)
	r := draftCall(t, helper, contract.AgentDraftRequest{Action: "request", System: f.c.SystemID(), Invitation: inv.Invitation.ID}, true)
	old := draftCall(t, primary, contract.AgentDraftRequest{Action: "requests", Request: r.Request}, true).Requests[0]
	if e := f.c.SetPermissions(f.ctx, p.ID, nil); e != nil {
		t.Fatal(e)
	}
	draftCall(t, primary, contract.AgentDraftRequest{Action: "read", Draft: d.Draft}, false)
	if e := f.c.SetPermissions(f.ctx, p.ID, primaryPermissions); e != nil {
		t.Fatal(e)
	}
	draftCall(t, primary, contract.AgentDraftRequest{Action: "decide", Request: r.Request, Handle: old.Handle, Accept: true}, false)
	draftCall(t, primary, contract.AgentDraftRequest{Action: "append", Draft: d.Draft, Handle: d.Handle, Text: "stale authority"}, false)
	current := draftCall(t, primary, contract.AgentDraftRequest{Action: "requests", Request: r.Request}, true).Requests[0]
	draftCall(t, primary, contract.AgentDraftRequest{Action: "decide", Request: r.Request, Handle: current.Handle, Accept: true}, true)
	read := draftCall(t, helper, contract.AgentDraftRequest{Action: "read", Request: r.Request}, true)
	view, e := ownerview.New(f.s, f.c, f.p)
	if e != nil {
		t.Fatal(e)
	}
	ctx := informationcontrol.Authenticate(context.Background(), token)
	in := contract.AgentDraftRequest{Action: "read", Draft: d.Draft}
	result, e := view.DraftWork(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.c.SetPermissions(f.ctx, p.ID, nil); e != nil {
		t.Fatal(e)
	}
	if e = view.CheckDraftWork(ctx, in, result.Handle); e == nil {
		t.Fatal("revoked primary delivered data")
	}
	draftCall(t, helper, contract.AgentDraftRequest{Action: "read", Request: r.Request}, false)
	draftCall(t, helper, contract.AgentDraftRequest{Action: "append", Request: r.Request, Handle: read.Handle, Text: "revoked"}, false)
	if e = f.c.SetPermissions(f.ctx, p.ID, primaryPermissions); e != nil {
		t.Fatal(e)
	}
	draftCall(t, helper, contract.AgentDraftRequest{Action: "read", Request: r.Request}, false)
	if status := draftCall(t, helper, contract.AgentDraftRequest{Action: "status", Request: r.Request}, true); status.State != "ended" {
		t.Fatal(status)
	}
}

func TestDraftManagementDoesNotImplyContentAndNamesDoNotGrantAuthority(t *testing.T) {
	f := ownerHTTP(t)
	p, _, ordinary := f.agent(t, "Main")
	for _, perms := range [][]contract.Permission{nil, {contract.ReadPermission, contract.MaintainPermission}, {contract.ManagePermission}} {
		if e := f.c.SetPermissions(f.ctx, p.ID, perms); e != nil {
			t.Fatal(e)
		}
		for _, action := range []string{"list", "create"} {
			draftCall(t, ordinary, contract.AgentDraftRequest{Action: action}, false)
		}
	}
	// A management-only connection may approve a scoped request without gaining
	// draft text. This is the same existing separation as other access decisions.
	_, _, helper := f.agent(t, "Main")
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("private")})
	inv := f.act(t, contract.OwnerAction{Action: "invite_draft", Handle: d.Handle})
	r := draftCall(t, helper, contract.AgentDraftRequest{Action: "request", System: f.c.SystemID(), Invitation: inv.Invitation.ID}, true)
	pending := draftCall(t, ordinary, contract.AgentDraftRequest{Action: "requests", Request: r.Request}, true)
	if len(pending.Requests) != 1 || pending.Requests[0].Distinction == "" {
		t.Fatal(pending)
	}
	draftCall(t, ordinary, contract.AgentDraftRequest{Action: "read", Draft: pending.Requests[0].Draft}, false)
	draftCall(t, ordinary, contract.AgentDraftRequest{Action: "decide", Request: r.Request, Handle: pending.Requests[0].Handle, Accept: true}, true)
	approved := draftCall(t, ordinary, contract.AgentDraftRequest{Action: "requests", Request: r.Request}, true)
	draftCall(t, ordinary, contract.AgentDraftRequest{Action: "end", Request: r.Request, Handle: approved.Requests[0].Handle}, true)
	draftCall(t, helper, contract.AgentDraftRequest{Action: "read", Request: r.Request}, false)
}
