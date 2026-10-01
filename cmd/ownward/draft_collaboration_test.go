package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

func TestDraftCollaborationWholeJourneyThroughWindowAndMCP(t *testing.T) {
	f := ownerHTTP(t)
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("写作中的草稿")})
	inv := f.act(t, contract.OwnerAction{Action: "invite_draft", Handle: d.Handle})
	if inv.Invitation == nil || inv.Instruction == "" {
		t.Fatal("empty handoff")
	}
	p, credential, agent := f.agent(t, "测试写作者")
	call := func(in contract.AgentDraftRequest, ok bool) contract.AgentDraftResult {
		t.Helper()
		r := hostCall(t, agent, "ownward_draft_work", in, ok)
		var out contract.AgentDraftResult
		if ok {
			if e := decodeTool(r, &out); e != nil {
				t.Fatal(e)
			}
		}
		return out
	}
	call(contract.AgentDraftRequest{Action: "request", Invitation: inv.Invitation.ID, System: "wrong-library"}, false)
	r := call(contract.AgentDraftRequest{Action: "request", Invitation: inv.Invitation.ID, System: f.c.SystemID()}, true)
	if r.State != "awaiting_approval" || r.Verification == "" {
		t.Fatal(r)
	}
	call(contract.AgentDraftRequest{Action: "read", Request: r.Request}, false)
	pending := f.query(t, contract.OwnerQuery{View: "pending"})
	if len(pending.Decisions) != 1 || pending.Decisions[0].Draft == nil || pending.Decisions[0].Verification != r.Verification {
		t.Fatal(pending)
	}
	before := f.query(t, contract.OwnerQuery{View: "changes"})
	f.act(t, contract.OwnerAction{Action: "decide", Handle: pending.Decisions[0].Handle, Accept: true})
	time.Sleep(contract.OwnerPollMin)
	if !f.query(t, contract.OwnerQuery{View: "changes", Cursor: before.Cursor}).Changed {
		t.Fatal("decision invisible")
	}
	status := call(contract.AgentDraftRequest{Action: "status", Request: r.Request, WaitSeconds: 1}, true)
	if status.State != "approved" {
		t.Fatal(status)
	}
	read := call(contract.AgentDraftRequest{Action: "read", Request: r.Request}, true)
	if read.Content == nil || read.Content.Text != "写作中的草稿" {
		t.Fatal(read)
	}
	written := call(contract.AgentDraftRequest{Action: "append", Request: r.Request, Handle: read.Handle, Text: "\n修改已经到达"}, true)
	call(contract.AgentDraftRequest{Action: "replace", Request: r.Request, Handle: read.Handle, Text: "stale overwrite"}, false)
	if written.Handle == read.Handle {
		t.Fatal("write handle unchanged")
	}
	page := f.query(t, contract.OwnerQuery{View: "resolve", Reference: d.Reference})
	if f.query(t, contract.OwnerQuery{View: "draft_content", Handle: page.Drafts[0].Handle}).Text.Text != "写作中的草稿\n修改已经到达" {
		t.Fatal("writing not visible")
	}
	self, e := f.c.Self(informationcontrol.Authenticate(context.Background(), credential))
	if e != nil || len(self.Permissions) != 0 || self.ID != p.ID {
		t.Fatal("permission expansion", e)
	}
	history := f.query(t, contract.OwnerQuery{View: "history"})
	if len(history.Decisions) != 1 || history.Decisions[0].Kind != "draft_collaboration" {
		t.Fatal("decision history missing", history)
	}
	collaborations := f.query(t, contract.OwnerQuery{View: "draft_collaborations", Handle: page.Drafts[0].Handle})
	f.act(t, contract.OwnerAction{Action: "end_collaboration", Handle: collaborations.Collaborations[0].Handle})
	call(contract.AgentDraftRequest{Action: "read", Request: r.Request}, false)
	if call(contract.AgentDraftRequest{Action: "status", Request: r.Request}, true).State != "ended" && call(contract.AgentDraftRequest{Action: "status", Request: r.Request}, true).State != "cancelled" {
		t.Fatal("ended request revived")
	}
	// A fresh invitation is explicit; a stale request never gets a new grant.
	newInv := f.act(t, contract.OwnerAction{Action: "invite_draft", Handle: page.Drafts[0].Handle})
	if newInv.Invitation.ID == inv.Invitation.ID {
		t.Fatal("restart reused ended request")
	}
	f.act(t, contract.OwnerAction{Action: "cancel_invitation", Handle: newInv.Handle})
	call(contract.AgentDraftRequest{Action: "request", Invitation: newInv.Invitation.ID, System: f.c.SystemID()}, false)
}

func TestDraftCollaborationMachineTransportAndBoundedWait(t *testing.T) {
	f := ownerHTTP(t)
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("local helper")})
	inv := f.act(t, contract.OwnerAction{Action: "invite_draft", Handle: d.Handle})
	_, credential, _ := f.agent(t, "执行型智能体")
	h := hostConnector{descriptor: &sharedMCPDescriptor{Endpoint: f.server.URL, BearerToken: "transport-test"}}
	in := contract.AgentDraftRequest{Action: "request", Invitation: inv.Invitation.ID, System: f.c.SystemID()}
	var r contract.AgentDraftResult
	if e := h.controlCall(context.Background(), "draft-work", credential, in, &r); e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	var waited contract.AgentDraftResult
	if e := h.controlCall(context.Background(), "draft-work", credential, contract.AgentDraftRequest{Action: "status", Request: r.Request, WaitSeconds: 1}, &waited); e != nil {
		t.Fatal(e)
	}
	if time.Since(start) > 3*time.Second || waited.State != "awaiting_approval" {
		t.Fatal("wait did not yield")
	}
	p := f.query(t, contract.OwnerQuery{View: "pending"})
	f.act(t, contract.OwnerAction{Action: "decide", Handle: p.Decisions[0].Handle, Accept: true})
	if e := h.controlCall(context.Background(), "draft-work", credential, contract.AgentDraftRequest{Action: "read", Request: r.Request}, &r); e != nil || r.Content == nil {
		t.Fatal(e)
	}
	request, _ := http.NewRequest("GET", f.server.URL+"/__ownward/owner/collaboration.js", nil)
	response, e := http.DefaultClient.Do(request)
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("new module not served", response.StatusCode)
	}
	encoded, e := json.Marshal(r)
	if e != nil || len(encoded) > contract.OwnerRequestBytes {
		t.Fatal(e)
	}
}
