package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/adapter/ownerwindow"
	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/ownerview"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOwnerWindowPermissionConflictReachesTerminal(t *testing.T) {
	for _, mode := range []string{"approved", "awaiting_accept", "awaiting_decline"} {
		t.Run(mode, func(t *testing.T) {
			f := ownerHTTP(t)
			principal, credential, client := f.agent(t, "connection")
			requester := informationcontrol.Authenticate(context.Background(), credential)
			old := contract.ManagementRequest{ID: "old-adjustment", Operation: "permissions", SubjectID: principal.ID, SubjectRevision: principal.Revision, Permissions: []contract.Permission{contract.ReadPermission}}
			if _, e := f.c.Propose(requester, old); e != nil {
				t.Fatal(e)
			}
			if mode == "approved" {
				if _, e := f.c.Decide(f.ctx, old.ID, true); e != nil {
					t.Fatal(e)
				}
			}
			pending := f.query(t, contract.OwnerQuery{View: "pending"})
			if len(pending.Decisions) != 1 {
				t.Fatal(pending)
			}
			handle := pending.Decisions[0].Handle
			newer := old
			newer.ID = "new-adjustment"
			newer.Permissions = []contract.Permission{contract.ReadPermission, contract.MaintainPermission}
			if _, e := f.p.Manage(f.ctx, newer); e != nil {
				t.Fatal(e)
			}
			want := "superseded"
			if mode == "awaiting_decline" {
				want = "declined"
			}
			if mode == "approved" {
				if result, e := f.p.Manage(requester, old); e != nil || result.Status != want {
					t.Fatal(result, e)
				}
			}
			result := f.act(t, contract.OwnerAction{Action: "decide", Handle: handle, Accept: mode != "awaiting_decline"})
			if result.State != want {
				t.Fatal(result)
			}
			if page := f.query(t, contract.OwnerQuery{View: "pending"}); len(page.Decisions) != 0 {
				t.Fatal("obsolete request remains pending", page)
			}
			before := f.query(t, contract.OwnerQuery{View: "events"})
			for _, accept := range []bool{false, true} {
				if result := f.act(t, contract.OwnerAction{Action: "decide", Handle: handle, Accept: accept}); result.State != want {
					t.Fatal(result)
				}
			}
			if after := f.query(t, contract.OwnerQuery{View: "events"}); len(after.Activity) != len(before.Activity) {
				t.Fatal("terminal retry duplicated events")
			}
			current, e := f.c.Principal(f.ctx, principal.ID)
			if e != nil || current.Revision != principal.Revision+1 || len(current.Permissions) != 2 {
				t.Fatal("stale request changed the newer rights", current, e)
			}
			found := false
			for _, decision := range f.query(t, contract.OwnerQuery{View: "history"}).Decisions {
				found = found || decision.State == want
			}
			if !found {
				t.Fatal("terminal result missing from history")
			}
			// The same terminal fact reaches the original MCP requester and is
			// removed from the connector's durable pending-delivery set.
			host := &hostConnector{vault: localowner.Vault{Root: t.TempDir()}, system: f.c.SystemID(), profile: "review-host", record: connectorRecord{Pending: []string{old.ID}}}
			delivery := &mcp.CallToolResult{}
			if e := host.appendReceipts(context.Background(), client, delivery); e != nil {
				t.Fatal(e)
			}
			if len(host.record.Pending) != 0 || len(delivery.Content) != 1 {
				t.Fatal("terminal result never delivered", host.record.Pending, delivery)
			}
			var receipt contract.ManagementReceipt
			if e := decodeTool(delivery, &receipt); e != nil || receipt.Status != want {
				t.Fatal(receipt, e)
			}
			if mode != "awaiting_decline" && acceptedDecision(receipt.Status) == nil {
				t.Fatal("superseded interpreted as approval")
			}
		})
	}
}

func TestOwnerWindowPermissionConflictDuringFrozenHandoff(t *testing.T) {
	f := ownerHTTP(t)
	principal, _, _ := f.agent(t, "connection")
	old := contract.ManagementRequest{ID: "old-frozen", Operation: "permissions", SubjectID: principal.ID, SubjectRevision: principal.Revision, Permissions: []contract.Permission{contract.ReadPermission}}
	if _, e := f.c.Propose(f.ctx, old); e != nil {
		t.Fatal(e)
	}
	newer := old
	newer.ID = "new-frozen"
	newer.Permissions = []contract.Permission{contract.ReadPermission, contract.MaintainPermission}
	if _, e := f.p.Manage(f.ctx, newer); e != nil {
		t.Fatal(e)
	}
	target := contract.Location{ServiceID: "receiver", SystemID: f.c.SystemID(), Endpoint: "https://isolated.test", Certificate: "fixture", Composition: "test"}
	h, e := f.c.PrepareHandoff(f.ctx, "freeze-conflict", target)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.c.DecideHandoff(f.ctx, h.ID, h.Revision, true); e != nil {
		t.Fatal(e)
	}
	if _, e = f.c.FreezeHandoff(f.ctx, h.ID, true); e != nil {
		t.Fatal(e)
	}
	before := f.c.State().Revision
	if _, e = f.p.Manage(f.ctx, old); !errors.Is(e, informationcontrol.ErrMoving) {
		t.Fatal(e)
	}
	if f.c.State().Revision != before {
		t.Fatal("conflict modified frozen authority")
	}
	if op, e := f.p.Receipt(f.ctx, old.ID); e != nil || op.Status != "approved" {
		t.Fatal("reported an uncommitted terminal result", op, e)
	}
	if e = f.p.CancelHandoff(f.ctx, h.ID); e != nil {
		t.Fatal(e)
	}
	if op, e := f.p.Manage(f.ctx, old); e != nil || op.Status != "superseded" {
		t.Fatal("unfrozen conflict cannot finish", op, e)
	}
}

// Reopen the actual database and rebuild all owner transport/control objects;
// neither a view key nor a browser session survives this restart.
func reopenOwnerHTTP(t *testing.T, f *ownerHTTPFixture) {
	t.Helper()
	f.server.Close()
	f.p.Close()
	if e := f.s.Close(); e != nil {
		t.Fatal(e)
	}
	b, e := resourcebudget.New(20*resourcebudget.MiB, 4*resourcebudget.MiB)
	if e != nil {
		t.Fatal(e)
	}
	s, e := boundedstore.OpenDeployment(context.Background(), f.root, boundedstore.DeploymentOptions{Options: boundedstore.Options{Budget: b}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	})
	a, e := s.OpenControlAuthority(context.Background(), contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "test"})
	if e != nil {
		t.Fatal(e)
	}
	c := informationcontrol.New(a)
	k := &core.StreamingAssets{Store: s, Budget: b, Scratch: filepath.Join(f.root, "scratch"), DiskBytes: 256 * resourcebudget.MiB}
	p := informationcontrol.NewProduct(k, c)
	t.Cleanup(p.Close)
	v, e := ownerview.New(s, c, p)
	if e != nil {
		t.Fatal(e)
	}
	w := ownerwindow.New(v, localOwnerArchives(k, c, f.root))
	m := mcpserver.NewStreamingStorage(k, "test", k.Scratch, b, k.DiskBytes)
	m.AddManagementTools(p)
	m.AddDraftTools(v)
	control := controlHTTPServer{server: m, control: c, product: p, kernel: k, window: w}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	server.Config.Handler = mountOwnerWindow(bearerTokenHandler(control.HTTPHandler(), "transport-test"), w.Mount(origin))
	server.Start()
	t.Cleanup(server.Close)
	f.s, f.c, f.k, f.p, f.w, f.server = s, c, k, p, w, server
	f.session = f.login(t)
}

func TestOwnerWindowPublicationRecoveryAfterRestart(t *testing.T) {
	f := ownerHTTP(t)
	draft := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("committed before the lost reply")})
	request := contract.OwnerAction{Action: "publish_draft", Handle: draft.Handle, OperationID: "lost-reply"}
	f.act(t, request) // The client loses this response.
	reopenOwnerHTTP(t, f)
	if response, _ := f.request(t, "action", request, nil); response.StatusCode != http.StatusConflict {
		t.Fatal("old process handle gained write authority", response.StatusCode)
	}
	q := contract.OwnerQuery{View: "publish_receipt", OperationID: request.OperationID}
	result := f.query(t, q).Publication
	if result == nil || result.State != "completed" || result.Asset == "" {
		t.Fatal("lost durable receipt", result)
	}
	if text := f.query(t, contract.OwnerQuery{View: "content", Handle: result.Asset}).Text; text == nil || text.Text != "committed before the lost reply" {
		t.Fatal(text)
	}
	if drafts := f.query(t, contract.OwnerQuery{View: "drafts"}); len(drafts.Drafts) != 0 {
		t.Fatal("resurrected draft")
	}
	if assets := f.query(t, contract.OwnerQuery{View: "assets"}); len(assets.Assets) != 1 {
		t.Fatal("recovery republished the draft")
	}
	if unknown := f.query(t, contract.OwnerQuery{View: "publish_receipt", OperationID: "wrong-id"}).Publication; unknown == nil || unknown.State != "unknown" || unknown.Asset != "" {
		t.Fatal(unknown)
	}
	edit := f.act(t, contract.OwnerAction{Action: "create_draft", Target: result.Asset, Text: textPtr("subsequent revision")})
	if response, _ := f.request(t, "action", contract.OwnerAction{Action: "publish_draft", Handle: edit.Handle, OperationID: request.OperationID}, nil); response.StatusCode == 200 {
		t.Fatal("changed draft reused an existing publication identity")
	}
	f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: edit.Handle, OperationID: "independent-edit"})
	changed := f.query(t, q).Publication
	if changed.State != "changed" || changed.Asset == "" {
		t.Fatal(changed)
	}
	if text := f.query(t, contract.OwnerQuery{View: "content", Handle: changed.Asset}).Text.Text; text != "subsequent revision" {
		t.Fatal("receipt linked to an unavailable old revision", text)
	}
	f.act(t, contract.OwnerAction{Action: "forget", Handle: changed.Asset, OperationID: "forget-published"})
	unavailable := queryOwnerAfterRefresh(t, f, q).Publication
	if unavailable.State != "unavailable" || unavailable.Asset != "" {
		t.Fatal("forgotten asset remained openable", unavailable)
	}
	var e error
	f.owner, e = f.c.RecoverOwner()
	if e != nil {
		t.Fatal(e)
	}
	f.ctx = informationcontrol.Authenticate(context.Background(), f.owner)
	if response, _ := f.request(t, "query", q, nil); response.StatusCode != http.StatusUnauthorized {
		t.Fatal("old identity read a receipt", response.StatusCode)
	}
	f.session = f.login(t)
	if result := queryOwnerAfterRefresh(t, f, q).Publication; result.State != "unavailable" || result.Asset != "" {
		t.Fatal(result)
	}
}

// A reader interrupted by concurrent forget follows the explicit v1 refresh
// response. No write is retried and arbitrary transport/SQL failures still fail.
func queryOwnerAfterRefresh(t *testing.T, f *ownerHTTPFixture, q contract.OwnerQuery) contract.OwnerPage {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		response, body := f.request(t, "query", q, nil)
		if response.StatusCode == http.StatusOK {
			var page contract.OwnerPage
			if e := json.Unmarshal(body, &page); e != nil {
				t.Fatal(e)
			}
			return page
		}
		if response.StatusCode != http.StatusConflict || strings.TrimSpace(string(body)) != contract.ErrOwnerRefresh.Error() {
			t.Fatalf("query returned an unexpected failure: %d %s", response.StatusCode, body)
		}
		t.Log("read interrupted by concurrent cleanup; client refreshes")
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owner projection did not recover after refresh")
	return contract.OwnerPage{}
}
