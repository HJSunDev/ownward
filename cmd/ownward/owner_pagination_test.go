package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestOwnerCollaborationFilteringPrecedesPagination(t *testing.T) {
	f := ownerHTTP(t)
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("paging fixture")})
	inv := f.act(t, contract.OwnerAction{Action: "invite_draft", Handle: d.Handle})
	for i := 0; i < 11; i++ {
		_, _, agent := f.agent(t, "helper-"+string(rune('a'+i)))
		hostCall(t, agent, "ownward_draft_work", contract.AgentDraftRequest{Action: "request", Invitation: inv.Invitation.ID, System: f.c.SystemID()}, true)
	}
	page := f.query(t, contract.OwnerQuery{View: "pending", Limit: 50})
	if len(page.Decisions) != 11 {
		t.Fatal("fixture", len(page.Decisions))
	}
	for _, item := range page.Decisions {
		f.act(t, contract.OwnerAction{Action: "decide", Handle: item.Handle})
	}
	for _, view := range []string{"pending", "draft_collaborations"} {
		q := contract.OwnerQuery{View: view, Limit: 10}
		if view == "draft_collaborations" {
			q.Handle, q.State = d.Handle, "active"
		}
		p := f.query(t, q)
		if len(p.Decisions)+len(p.Collaborations) != 0 || p.Next != "" {
			t.Errorf("%s advertises completed records: decisions=%d collaborations=%d next=%t", view, len(p.Decisions), len(p.Collaborations), p.Next != "")
		}
	}
	history := f.query(t, contract.OwnerQuery{View: "history", Limit: 11})
	if len(history.Decisions) != 11 || history.Next != "" {
		t.Error("history advertises an empty final page")
	}
	// A live request beyond terminal rows must appear in the first visible page.
	_, _, late := f.agent(t, "late-helper")
	hostCall(t, late, "ownward_draft_work", contract.AgentDraftRequest{Action: "request", Invitation: inv.Invitation.ID, System: f.c.SystemID()}, true)
	p := f.query(t, contract.OwnerQuery{View: "pending", Limit: 1})
	if len(p.Decisions) != 1 || p.Next != "" {
		t.Error("live request is obscured by terminal rows or a false continuation")
	}
	active := f.query(t, contract.OwnerQuery{View: "draft_collaborations", Handle: d.Handle, State: "active", Limit: 1})
	if len(active.Collaborations) != 1 || active.Collaborations[0].State != "awaiting_approval" || active.Next != "" {
		t.Error("active summary includes history")
	}
	f.act(t, contract.OwnerAction{Action: "cancel_invitation", Handle: inv.Handle})
	status := f.query(t, contract.OwnerQuery{View: "draft_collaborations", Handle: d.Handle, Reference: inv.Invitation.ID, Limit: 16})
	if !status.Unavailable {
		t.Error("cancelled invitation still reusable")
	}
}

func TestOwnerListRefreshKeepsPositionWithoutReusingSnapshot(t *testing.T) {
	f := ownerHTTP(t)
	for i := 0; i < 5; i++ {
		f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("draft")})
	}
	first := f.query(t, contract.OwnerQuery{View: "drafts", Limit: 2})
	f.query(t, contract.OwnerQuery{View: "drafts", Limit: 2, After: first.Next})
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("background change")})
	r, _ := f.request(t, "query", contract.OwnerQuery{View: "drafts", Limit: 2, After: first.Next}, nil)
	if r.StatusCode != http.StatusConflict {
		t.Fatal("ordinary stale continuation accepted")
	}
	r, body := f.request(t, "query", map[string]any{"view": "drafts", "limit": 2, "after": first.Next, "refresh": true}, nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("position refresh failed: %d %s", r.StatusCode, body)
	}
	var fresh contract.OwnerPage
	if e := json.Unmarshal(body, &fresh); e != nil {
		t.Fatal(e)
	}
	if len(fresh.Drafts) != 2 || fresh.Drafts[0].Reference == first.Drafts[0].Reference {
		t.Fatal("refresh reset to first page")
	}
	// A cursor never switches filters or view, and refresh does not repair a stale write.
	r, _ = f.request(t, "query", map[string]any{"view": "connections", "limit": 2, "after": first.Next, "refresh": true}, nil)
	if r.StatusCode != http.StatusConflict {
		t.Fatal("cross-view refresh accepted")
	}
	f.act(t, contract.OwnerAction{Action: "replace_draft", Handle: d.Handle, Text: textPtr("current")})
	time.Sleep(contract.OwnerPollMin) // Respect the real HTTP burst limit before the rejection probe.
	r, _ = f.request(t, "action", contract.OwnerAction{Action: "replace_draft", Handle: d.Handle, Text: textPtr("stale")}, nil)
	if r.StatusCode != http.StatusConflict {
		t.Fatal("write revision check weakened", r.StatusCode)
	}
}

func TestOwnerPositionRefreshHonorsIdentityAndForgetBarriers(t *testing.T) {
	for _, barrier := range []string{"forget", "owner"} {
		t.Run(barrier, func(t *testing.T) {
			f := ownerHTTP(t)
			for i := 0; i < 3; i++ {
				f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("fixture")})
			}
			first := f.query(t, contract.OwnerQuery{View: "drafts", Limit: 1})
			if first.Next == "" {
				t.Fatal("fixture requires continuation")
			}
			if barrier == "forget" {
				_, e := f.k.Create(f.ctx, contract.CreateInput{Content: "disposable fixture", Source: domain.Source{Ref: "test:pagination"}})
				if e != nil {
					t.Fatal(e)
				}
				assets := f.query(t, contract.OwnerQuery{View: "assets"})
				f.act(t, contract.OwnerAction{Action: "forget", Handle: assets.Assets[0].Handle, OperationID: "forget-refresh"})
			} else {
				var e error
				f.owner, e = f.c.RecoverOwner()
				if e != nil {
					t.Fatal(e)
				}
				f.session = f.login(t)
			}
			r, _ := f.request(t, "query", contract.OwnerQuery{View: "drafts", Limit: 1, After: first.Next, Refresh: true}, nil)
			if r.StatusCode != http.StatusConflict {
				t.Fatalf("%s barrier accepted old position: %d", barrier, r.StatusCode)
			}
		})
	}
}

func TestOwnerFullAssetPageHasNoPhantomStoppedPage(t *testing.T) {
	f := ownerHTTP(t)
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("one asset")})
	f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "one"})
	p := f.query(t, contract.OwnerQuery{View: "assets", Limit: 1})
	if len(p.Assets) != 1 || p.Next != "" {
		t.Fatal("full asset page advertises nonexistent stopped material")
	}
	recent := f.query(t, contract.OwnerQuery{View: "recent", Limit: 100})
	if len(recent.Activity) == 0 || recent.Next != "" {
		t.Fatal("recent events advertise an empty older page")
	}
	stream := f.query(t, contract.OwnerQuery{View: "events", Limit: 100})
	if stream.Next == "" {
		t.Fatal("incremental stream lost its resumable tail")
	}
}
