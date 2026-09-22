package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

func TestOwnerWindowManagementErrorsAreNotCompleted(t *testing.T) {
	f := ownerHTTP(t)
	p, _, e := f.c.Enroll(f.ctx, "connection")
	if e != nil {
		t.Fatal(e)
	}
	connection := func() string {
		for _, c := range f.query(t, contract.OwnerQuery{View: "connections"}).Connections {
			if !c.Owner {
				return c.Handle
			}
		}
		t.Fatal("missing connection")
		return ""
	}
	old := connection()
	if e = f.c.SetPermissions(f.ctx, p.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission}); e != nil {
		t.Fatal(e)
	}
	response, body := f.request(t, "action", contract.OwnerAction{Action: "permissions", Handle: old, OperationID: "stale-revoke"}, nil)
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(body), contract.ErrOwnerRefresh.Error()) {
		t.Fatalf("stale permission action: %d %s", response.StatusCode, body)
	}
	current, e := f.c.Principal(f.ctx, p.ID)
	if e != nil || len(current.Permissions) != 2 {
		t.Fatal("rejected request altered permissions", current, e)
	}
	// Other management errors retain their meaning, rather than being labelled
	// either completed or a version conflict.
	response, body = f.request(t, "action", contract.OwnerAction{Action: "permissions", Handle: connection(), OperationID: "invalid-rights", Permissions: []contract.Permission{"invalid"}}, nil)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "授权能力无效") {
		t.Fatalf("invalid permission action: %d %s", response.StatusCode, body)
	}
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("keep this")})
	a := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "publish-for-error"})
	response, body = f.request(t, "action", contract.OwnerAction{Action: "forget", Handle: a.Handle, OperationID: strings.Repeat("x", 129)}, nil)
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "操作标识无效") {
		t.Fatalf("invalid forget action: %d %s", response.StatusCode, body)
	}
	result := f.act(t, contract.OwnerAction{Action: "permissions", Handle: connection(), OperationID: "fresh-revoke"})
	current, e = f.c.Principal(f.ctx, p.ID)
	if result.State != "completed" || e != nil || len(current.Permissions) != 0 {
		t.Fatal("fresh revocation failed", result, current, e)
	}
}

func TestOwnerWindowStaleForgetEndsWithoutDeletingCurrentContent(t *testing.T) {
	for _, mode := range []string{"owner", "awaiting_accept", "awaiting_decline", "already_forgotten"} {
		t.Run(mode, func(t *testing.T) {
			f := ownerHTTP(t)
			d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("version one")})
			first := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "first"})
			var decision string
			if strings.HasPrefix(mode, "awaiting") {
				_, credential, e := f.c.Enroll(f.ctx, "requester")
				if e != nil {
					t.Fatal(e)
				}
				rows, _, e := f.s.OwnerAssets(f.ctx, "", "", "", "", 10)
				if e != nil || len(rows) != 1 {
					t.Fatal(rows, e)
				}
				requester := informationcontrol.Authenticate(context.Background(), credential)
				_, e = f.p.Manage(requester, contract.ManagementRequest{ID: "old-forget", Operation: "forget", Targets: []contract.AssetVersion{{ID: rows[0].Meta.ID, Revision: rows[0].Meta.Revision}}})
				if e != nil {
					t.Fatal(e)
				}
				decision = f.query(t, contract.OwnerQuery{View: "pending"}).Decisions[0].Handle
			}
			edit := f.act(t, contract.OwnerAction{Action: "create_draft", Target: first.Handle, Text: textPtr("version two")})
			second := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: edit.Handle, OperationID: "second"})
			if mode == "already_forgotten" {
				f.act(t, contract.OwnerAction{Action: "forget", Handle: second.Handle, OperationID: "fresh-forget"})
				if e := f.s.DrainMaintenance(f.ctx); e != nil {
					t.Fatal(e)
				}
			}
			before, e := f.s.OwnerCheckpoint(f.ctx)
			if e != nil {
				t.Fatal(e)
			}
			request := contract.OwnerAction{Action: "forget", Handle: first.Handle, OperationID: "old-forget"}
			want := "superseded"
			if decision != "" {
				request = contract.OwnerAction{Action: "decide", Handle: decision, Accept: mode == "awaiting_accept"}
				if !request.Accept {
					want = "declined"
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				if got := f.act(t, request); got.State != want {
					t.Fatal("obsolete forget did not reach its terminal result", got)
				}
			}
			after, e := f.s.OwnerCheckpoint(f.ctx)
			if e != nil || before.Deletion != after.Deletion {
				t.Fatal("obsolete request published a new deletion barrier", before, after, e)
			}
			for _, v := range queryOwnerAfterRefresh(t, f, contract.OwnerQuery{View: "pending"}).Decisions {
				if v.Kind == "forget" && v.State == "approved" {
					t.Fatal("obsolete forget remains pending", v)
				}
			}
			found := false
			for _, v := range queryOwnerAfterRefresh(t, f, contract.OwnerQuery{View: "history"}).Decisions {
				if v.State == want {
					found = true
					if want == "superseded" && !strings.Contains(v.Consequence, "遗忘") {
						t.Fatal("wrong operation explanation", v)
					}
				}
			}
			if !found {
				t.Fatal("terminal result missing from history")
			}
			if mode == "owner" {
				reopenOwnerHTTP(t, f)
				if op, e := f.p.Receipt(f.ctx, "old-forget"); e != nil || op.Status != want {
					t.Fatal("terminal outcome lost after actual restart", op, e)
				}
				second.Handle = f.query(t, contract.OwnerQuery{View: "publish_receipt", OperationID: "second"}).Publication.Asset
			}
			if mode != "already_forgotten" {
				page := f.query(t, contract.OwnerQuery{View: "content", Handle: second.Handle})
				if page.Text.Text != "version two" {
					t.Fatal("current content changed", page)
				}
				// Only a fresh explicit operation against the current version may
				// remove it. The obsolete ID cannot silently retarget itself.
				response, body := f.request(t, "action", contract.OwnerAction{Action: "forget", Handle: second.Handle, OperationID: "old-forget"}, nil)
				var result contract.OwnerResult
				_ = json.Unmarshal(body, &result)
				if response.StatusCode == http.StatusOK || result.State == "completed" {
					t.Fatal("old identity retargeted", response.StatusCode, string(body))
				}
				if got := f.act(t, contract.OwnerAction{Action: "forget", Handle: second.Handle, OperationID: "new-forget"}); got.State != "cleaning" && got.State != "completed" {
					t.Fatal("fresh forget cannot continue", got)
				}
			}
		})
	}
}

func TestOwnerWindowStaleForgetLeavesFrozenCandidateUntouched(t *testing.T) {
	f := ownerHTTP(t)
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("retain through migration")})
	f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "publish"})
	rows, _, e := f.s.OwnerAssets(f.ctx, "", "", "", "", 10)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
	old := contract.ManagementRequest{ID: "old-forget", Operation: "forget", Targets: []contract.AssetVersion{{ID: rows[0].Meta.ID, Revision: rows[0].Meta.Revision + 1}}}
	if _, e = f.c.Propose(f.ctx, old); e != nil {
		t.Fatal(e)
	}
	target := contract.Location{ServiceID: "receiver", SystemID: f.c.SystemID(), Endpoint: "https://isolated.test", Certificate: "fixture", Composition: "test"}
	h, e := f.c.PrepareHandoff(f.ctx, "frozen-forget", target)
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
		t.Fatal("frozen conflict falsely completed", e)
	}
	state := f.c.State()
	if state.Revision != before || state.Access.Handoff == nil || state.Access.Handoff.Phase != "frozen" {
		t.Fatal("stale forget altered frozen authority", state)
	}
	if e = f.p.CancelHandoff(f.ctx, h.ID); e != nil {
		t.Fatal(e)
	}
	if result, e := f.p.Manage(f.ctx, old); e != nil || result.Status != "superseded" {
		t.Fatal("unfrozen conflict cannot end", result, e)
	}
}
