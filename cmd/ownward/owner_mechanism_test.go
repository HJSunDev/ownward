package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOwnerMechanismApprovalFixesTargetUntilExecution(t *testing.T) {
	f := ownerHTTP(t)
	p, credential, _ := f.agent(t, "requester")
	agent := informationcontrol.Authenticate(context.Background(), credential)
	r := contract.ManagementRequest{ID: "versionless-input", Operation: "permissions", SubjectID: p.ID, Permissions: []contract.Permission{contract.ReadPermission}}
	if _, err := f.c.Propose(agent, r); err != nil {
		t.Fatal(err)
	}
	approved, err := f.c.Decide(f.ctx, r.ID, true)
	if err != nil || approved.ApprovedSubjectRevision != p.Revision || !reflect.DeepEqual(approved.Request, r) {
		t.Fatal(approved, err)
	}
	if err = f.c.SetPermissions(f.ctx, p.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		op, err := f.p.Manage(agent, r)
		if err != nil || op.Status != "superseded" || op.ApprovedSubjectRevision != approved.ApprovedSubjectRevision || !reflect.DeepEqual(op.Request, r) {
			t.Fatal("retry renewed stale approval", op, err)
		}
	}
	current, err := f.c.Principal(f.ctx, p.ID)
	if err != nil || len(current.Permissions) != 2 {
		t.Fatal("old request overwrote current rights", current, err)
	}
}

func TestOwnerMechanismLegacyUnversionedApprovalRequiresFreshDecision(t *testing.T) {
	f := ownerHTTP(t)
	p, credential, _ := f.agent(t, "requester")
	r := contract.ManagementRequest{ID: "legacy-approval", Operation: "permissions", SubjectID: p.ID, Permissions: []contract.Permission{contract.ReadPermission}}
	if _, err := f.c.Propose(informationcontrol.Authenticate(context.Background(), credential), r); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Decide(f.ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}
	authority, err := f.s.OpenControlAuthority(context.Background(), contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "test"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := authority.ReadSelectedControl(contract.ControlSelection{Operation: r.ID})
	if err != nil {
		t.Fatal(err)
	}
	state.InformationControl.Operations[0].ApprovedSubjectRevision = 0 // historical record, not a current approval
	state.Revision++
	if _, err = authority.CompareAndSwapControl(state.Revision-1, state); err != nil {
		t.Fatal(err)
	}
	if err = f.c.ApplyPermissions(r.ID); !errors.Is(err, informationcontrol.ErrDenied) {
		t.Fatal("historical unbound approval executed", err)
	}
	if _, err = f.c.Decide(f.ctx, r.ID, true); !errors.Is(err, contract.ErrOwnerRefresh) {
		t.Fatal("legacy decision renewed old approval", err)
	}
	rows, err := f.c.PendingManagement(f.ctx)
	page := f.query(t, contract.OwnerQuery{View: "pending"})
	if err != nil || len(rows) != 1 || len(page.Decisions) != 1 || rows[0].Status != "awaiting_approval" || page.Decisions[0].State != "awaiting_approval" {
		t.Fatal(rows, page, err)
	}
	f.act(t, contract.OwnerAction{Action: "decide", Handle: page.Decisions[0].Handle, Accept: true})
	op, err := f.c.Receipt(f.ctx, r.ID)
	if err != nil || op.Status != "completed" || op.ApprovedSubjectRevision != p.Revision || !reflect.DeepEqual(op.Request, r) {
		t.Fatal("fresh approval did not preserve original request", op, err)
	}
}

func TestOwnerMechanismInvalidApprovalSharesQueueAndRejectsOldForms(t *testing.T) {
	for _, accept := range []bool{true, false} {
		t.Run(fmt.Sprint(accept), func(t *testing.T) {
			f := ownerHTTP(t)
			target, _, client := f.agent(t, "requester")
			manager, credential, _ := f.agent(t, "approver")
			if err := f.c.SetPermissions(f.ctx, manager.ID, []contract.Permission{contract.ManagePermission}); err != nil {
				t.Fatal(err)
			}
			managerCtx := informationcontrol.Authenticate(context.Background(), credential)
			request := contract.ManagementRequest{ID: "same-decision", Operation: "permissions", SubjectID: target.ID, Permissions: []contract.Permission{contract.ReadPermission}}
			hostCall(t, client, "ownward_manage", request, true)
			old := f.query(t, contract.OwnerQuery{View: "pending"}).Decisions[0]
			host := hostConnector{descriptor: &sharedMCPDescriptor{Endpoint: f.server.URL, BearerToken: "transport-test"}}
			var preview struct{ Message, Decision string }
			if err := host.controlCall(f.ctx, "preview", f.owner, map[string]string{"id": request.ID}, &preview); err != nil {
				t.Fatal(err)
			}
			if preview.Decision == "" {
				t.Fatal("preview has no decision identity")
			}
			// Durable approval checkpoint followed by loss of approval authority.
			if _, err := f.c.Decide(managerCtx, request.ID, true); err != nil {
				t.Fatal(err)
			}
			if err := f.c.SetPermissions(f.ctx, manager.ID, nil); err != nil {
				t.Fatal(err)
			}
			page := f.query(t, contract.OwnerQuery{View: "pending"})
			pending, err := f.c.PendingManagement(f.ctx)
			if err != nil || len(pending) != 1 || len(page.Decisions) != 1 || page.Decisions[0].State != "awaiting_approval" || pending[0].Status != "awaiting_approval" {
				t.Fatal("queue disagreement", pending, page, err)
			}
			var receipt contract.ManagementReceipt
			if err = decodeTool(hostCall(t, client, "ownward_management_status", map[string]string{"id": request.ID}, true), &receipt); err != nil || receipt.Status != "awaiting_approval" {
				t.Fatal(receipt, err)
			}
			if err = decodeTool(hostCall(t, client, "ownward_manage", request, true), &receipt); err != nil || receipt.Status != "awaiting_approval" {
				t.Fatal("retry lost effective state", receipt, err)
			}
			response, _ := f.request(t, "action", contract.OwnerAction{Action: "decide", Handle: old.Handle, Accept: accept}, nil)
			if response.StatusCode != http.StatusConflict {
				t.Fatal("old card renewed approval", response.StatusCode)
			}
			if err = host.controlCall(f.ctx, "decide", f.owner, map[string]any{"id": request.ID, "decision": preview.Decision, "accept": accept}, &receipt); err == nil {
				t.Fatal("old host preview renewed approval")
			}
			current, err := f.c.Principal(f.ctx, target.ID)
			if err != nil || len(current.Permissions) != 0 {
				t.Fatal("stale forms changed rights", current, err)
			}
			want := "declined"
			if accept {
				want = "completed"
			}
			fresh := f.act(t, contract.OwnerAction{Action: "decide", Handle: page.Decisions[0].Handle, Accept: accept})
			if fresh.State != want {
				t.Fatal("fresh decision did not resume", fresh)
			}
			if late := f.act(t, contract.OwnerAction{Action: "decide", Handle: old.Handle, Accept: !accept}); late.State != want {
				t.Fatal("late decision reopened terminal", late)
			}
			pending, err = f.c.PendingManagement(f.ctx)
			if err != nil || len(pending) != 0 || len(f.query(t, contract.OwnerQuery{View: "pending"}).Decisions) != 0 {
				t.Fatal("terminal still pending", pending, err)
			}
		})
	}
}

func TestOwnerMechanismPendingSelectionDoesNotHideInvalidApprovals(t *testing.T) {
	f := ownerHTTP(t)
	target, token, _ := f.agent(t, "requester")
	manager, managerToken, _ := f.agent(t, "manager")
	if err := f.c.SetPermissions(f.ctx, manager.ID, []contract.Permission{contract.ManagePermission}); err != nil {
		t.Fatal(err)
	}
	ctx := informationcontrol.Authenticate(context.Background(), token)
	for i := 0; i < 65; i++ {
		r := contract.ManagementRequest{ID: fmt.Sprintf("a-%02d", i), Operation: "permissions", SubjectID: target.ID, Permissions: []contract.Permission{contract.ReadPermission}}
		if _, err := f.c.Propose(ctx, r); err != nil {
			t.Fatal(err)
		}
		if _, err := f.c.Decide(f.ctx, r.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	r := contract.ManagementRequest{ID: "z-expired", Operation: "permissions", SubjectID: target.ID}
	if _, err := f.c.Propose(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Decide(informationcontrol.Authenticate(context.Background(), managerToken), r.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := f.c.SetPermissions(f.ctx, manager.ID, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := f.c.PendingManagement(f.ctx)
	if err != nil || len(rows) != 1 || rows[0].Request.ID != r.ID {
		t.Fatal("valid approvals hid the pending decision", rows, err)
	}
}

func TestOwnerMechanismApprovedHostRequestResumesWithoutNewConfirmation(t *testing.T) {
	f := ownerHTTP(t)
	p, token, _ := f.agent(t, "requester")
	r := contract.ManagementRequest{ID: "approved-before-interruption", Operation: "permissions", SubjectID: p.ID, Permissions: []contract.Permission{contract.ReadPermission}}
	if _, err := f.c.Propose(informationcontrol.Authenticate(context.Background(), token), r); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Decide(f.ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}
	// Reading the durable receipt never performs the pending write.
	if op, err := f.p.Receipt(f.ctx, r.ID); err != nil || op.Status != "approved" {
		t.Fatal(op, err)
	}
	current, err := f.c.Principal(f.ctx, p.ID)
	if err != nil || len(current.Permissions) != 0 {
		t.Fatal(current, err)
	}
	vault := localowner.Vault{Root: t.TempDir()}
	if err = vault.Save(f.c.SystemID(), "owner", f.owner); err != nil {
		t.Fatal(err)
	}
	h := hostConnector{descriptor: &sharedMCPDescriptor{Endpoint: f.server.URL, BearerToken: "transport-test"}, vault: vault, system: f.c.SystemID(), profile: "resume-approved"}
	// No MCP elicitation session: a second confirmation would be a bug.
	op, err := h.confirm(context.Background(), &mcp.CallToolRequest{}, r.ID)
	if err != nil || op.Status != "completed" {
		t.Fatal("approved work was not resumed", op, err)
	}
	current, err = f.c.Principal(f.ctx, p.ID)
	if err != nil || len(current.Permissions) != 1 {
		t.Fatal(current, err)
	}
}

func TestOwnerMechanismObsoleteReadHandlesRequireRefresh(t *testing.T) {
	f := ownerHTTP(t)
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("private current text")})
	a := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "read-lifecycle"})
	bound := f.act(t, contract.OwnerAction{Action: "create_draft", Target: a.Handle})
	f.act(t, contract.OwnerAction{Action: "forget", Handle: a.Handle, OperationID: "forget-lifecycle"})
	for _, view := range []string{"content", "details", "original", "original_details", "relations", "draft_content"} {
		handle := a.Handle
		if view == "draft_content" {
			handle = bound.Handle
		}
		response, body := f.request(t, "query", contract.OwnerQuery{View: view, Handle: handle}, nil)
		if response.StatusCode != http.StatusConflict || strings.Contains(string(body), "sql:") || strings.Contains(string(body), "private current text") {
			t.Fatal(view, response.StatusCode, string(body))
		}
	}
	// Promotion and discard also end draft identities without deleting an asset.
	for _, old := range []string{d.Handle, bound.Handle} {
		response, _ := f.request(t, "query", contract.OwnerQuery{View: "draft_content", Handle: old}, nil)
		if response.StatusCode != http.StatusConflict {
			t.Fatal("ended draft was not refresh", response.StatusCode)
		}
	}
	d = f.act(t, contract.OwnerAction{Action: "create_draft"})
	f.act(t, contract.OwnerAction{Action: "discard_draft", Handle: d.Handle})
	response, _ := f.request(t, "query", contract.OwnerQuery{View: "draft_content", Handle: d.Handle}, nil)
	if response.StatusCode != http.StatusConflict {
		t.Fatal("discarded draft was not refresh", response.StatusCode)
	}
}

func TestOwnerMechanismFrozenOperationsNeverClaimVolatileDecisions(t *testing.T) {
	f := ownerHTTP(t)
	p, token, _ := f.agent(t, "requester")
	if err := f.c.SetPermissions(f.ctx, p.ID, []contract.Permission{contract.ReadPermission}); err != nil {
		t.Fatal(err)
	}
	agent := informationcontrol.Authenticate(context.Background(), token)
	r := contract.ManagementRequest{ID: "durable-before-freeze", Operation: "permissions", SubjectID: p.ID}
	if _, err := f.p.Manage(agent, r); err != nil {
		t.Fatal(err)
	}
	target := contract.Location{SystemID: f.c.SystemID(), ServiceID: "target", Endpoint: "https://target.test", Certificate: "test", Composition: "test"}
	h, err := f.c.PrepareHandoff(f.ctx, "frozen", target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.DecideHandoff(f.ctx, h.ID, h.Revision, true); err != nil {
		t.Fatal(err)
	}
	h, err = f.c.FreezeHandoff(f.ctx, h.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	page := f.query(t, contract.OwnerQuery{View: "pending"})
	response, _ := f.request(t, "action", contract.OwnerAction{Action: "decide", Handle: page.Decisions[0].Handle, Accept: false}, nil)
	if response.StatusCode == http.StatusOK {
		t.Fatal("volatile decline was reported as durable")
	}
	r.ID = "new-while-frozen"
	if _, err = f.p.Manage(agent, r); err == nil {
		t.Fatal("volatile request was accepted")
	}
	if _, err = f.c.Receipt(f.ctx, r.ID); err == nil {
		t.Fatal("unaccepted request acquired a receipt")
	}
	if f.c.State().Revision != h.Revision {
		t.Fatal("failed actions changed frozen snapshot")
	}
	// Reopen the real store: only the original durable request is pending.
	reopenOwnerHTTP(t, f)
	page = f.query(t, contract.OwnerQuery{View: "pending"})
	rows, err := f.c.PendingManagement(f.ctx)
	if err != nil || len(rows) != 1 || len(page.Decisions) != 1 || rows[0].Request.ID != "durable-before-freeze" || page.Decisions[0].State != rows[0].Status {
		t.Fatal(page, rows, err)
	}
	result := f.act(t, contract.OwnerAction{Action: "decide", Handle: page.Decisions[0].Handle, Accept: true})
	if result.State != "completed" || f.c.State().Access.Handoff != nil {
		t.Fatal("atomic safety decision failed", result)
	}
}
