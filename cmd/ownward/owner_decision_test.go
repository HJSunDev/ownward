package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOwnerWindowMigrationFormKeepsPresentedRevision(t *testing.T) {
	f := ownerHTTP(t)
	manager, credential, err := f.c.Enroll(f.ctx, "migration manager")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.c.SetPermissions(f.ctx, manager.ID, []contract.Permission{contract.ManagePermission}); err != nil {
		t.Fatal(err)
	}
	managerCtx := informationcontrol.Authenticate(context.Background(), credential)
	target := contract.Location{SystemID: f.c.SystemID(), ServiceID: "review-target", Endpoint: "https://127.0.0.1:9443", Certificate: strings.Repeat("a", 64), Composition: "test"}
	initial, err := f.c.PrepareHandoff(managerCtx, "review-move", target)
	if err != nil {
		t.Fatal(err)
	}
	opened, accept, renewed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var renewedOnce, openedOnce sync.Once
	var mutated atomic.Bool
	var started atomic.Bool
	var sentRevision atomic.Uint64
	transport := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := informationcontrol.Authenticate(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		var value any
		var err error
		switch r.URL.Path {
		case "/remote/identity":
			value = map[string]any{"handoff": initial}
		case "/remote/migration/prepare":
			value, err = f.c.PrepareHandoff(ctx, initial.ID, target)
		case "/remote/migration/status":
			var state contract.Handoff
			state, err = f.c.HandoffStatus(ctx, initial.ID)
			value = state
			if mutated.Load() && state.ApprovalStatus == "awaiting_approval" {
				defer renewedOnce.Do(func() { close(renewed) })
			}
		case "/remote/migration/decide":
			var in struct {
				ID       string `json:"id"`
				Revision uint64 `json:"revision"`
				Accept   bool   `json:"accept"`
			}
			err = json.NewDecoder(r.Body).Decode(&in)
			if err == nil {
				sentRevision.Store(in.Revision)
				value, err = f.p.DecideHandoff(ctx, in.ID, in.Revision, in.Accept)
			}
		case "/remote/migration/start":
			_, err = f.c.FreezeHandoff(ctx, initial.ID, true)
			if err == nil {
				started.Store(true)
				value = receiverState{ID: initial.ID, Status: "frozen"}
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	defer transport.Close()
	host := &hostConnector{vault: localowner.Vault{Root: t.TempDir()}, system: f.c.SystemID(), profile: "review-move", record: connectorRecord{Credential: credential, Principal: manager.ID, Connected: true, MigrationID: initial.ID, NextLocation: &target}, remote: &remoteConnection{Material: connectionMaterial{Location: contract.Location{Endpoint: transport.URL, SystemID: f.c.SystemID()}}, Client: transport.Client()}}
	proxy := mcp.NewServer(&mcp.Implementation{Name: "review-host"}, nil)
	addMigrationTool(proxy, host)
	ct, st := mcp.NewInMemoryTransports()
	server, err := proxy.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "review-client"}, &mcp.ClientOptions{ElicitationHandler: func(ctx context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		openedOnce.Do(func() { close(opened) })
		select {
		case <-accept:
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}})
	session, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	type response struct {
		result *mcp.CallToolResult
		err    error
	}
	done := make(chan response, 1)
	go func() {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_migrate", Arguments: map[string]any{"target": target}})
		done <- response{result, err}
	}()
	select {
	case <-opened:
	case <-ctx.Done():
		t.Fatal("form did not open")
	}
	if _, err = f.c.DecideHandoff(f.ctx, initial.ID, initial.Revision, true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.RecoverOwner(); err != nil {
		t.Fatal(err)
	}
	mutated.Store(true)
	select {
	case <-renewed:
	case <-ctx.Done():
		t.Fatal("did not observe invalidated approval")
	}
	current, err := f.c.HandoffStatus(managerCtx, initial.ID)
	if err != nil || current.ApprovalStatus != "awaiting_approval" {
		t.Fatal(current, err)
	}
	close(accept)
	select {
	case result := <-done:
		if result.err != nil || !result.result.IsError {
			t.Fatal("stale form was not rejected", result)
		}
	case <-ctx.Done():
		t.Fatal("tool did not finish")
	}
	t.Logf("form revision=%d invalidated revision=%d submitted revision=%d migration_started=%v", initial.Revision, current.Revision, sentRevision.Load(), started.Load())
	if started.Load() || sentRevision.Load() != initial.Revision {
		t.Fatal("stale form borrowed a new version or started migration")
	}
	fresh, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_migrate", Arguments: map[string]any{"target": target}})
	if err != nil || fresh.IsError || !started.Load() || sentRevision.Load() != current.Revision {
		t.Fatal("a freshly presented confirmation could not resume", fresh, err)
	}
}

func TestOwnerWindowCompletedAccessDecisionsRemainVisible(t *testing.T) {
	f := ownerHTTP(t)
	if _, err := f.c.Invite(f.ctx, "review-join"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Join("review-join", strings.Repeat("p", 48), "new reader", []contract.Permission{contract.ReadPermission}); err != nil {
		t.Fatal(err)
	}
	pending := f.query(t, contract.OwnerQuery{View: "pending"})
	if len(pending.Decisions) != 1 {
		t.Fatal(pending)
	}
	f.act(t, contract.OwnerAction{Action: "decide", Handle: pending.Decisions[0].Handle, Accept: true})
	target := contract.Location{SystemID: f.c.SystemID(), ServiceID: "review-target", Endpoint: "https://127.0.0.1:9443", Certificate: strings.Repeat("a", 64), Composition: "test"}
	if _, err := f.c.PrepareHandoff(f.ctx, "review-decline", target); err != nil {
		t.Fatal(err)
	}
	pending = f.query(t, contract.OwnerQuery{View: "pending"})
	if len(pending.Decisions) != 1 {
		t.Fatal(pending)
	}
	f.act(t, contract.OwnerAction{Action: "decide", Handle: pending.Decisions[0].Handle, Accept: false})
	pending = f.query(t, contract.OwnerQuery{View: "pending"})
	history := f.query(t, contract.OwnerQuery{View: "history"})
	events := f.query(t, contract.OwnerQuery{View: "events"})
	t.Logf("after approval+decline: pending=%d history=%d activity=%d", len(pending.Decisions), len(history.Decisions), len(events.Activity))
	if len(history.Decisions) != 2 {
		t.Fatal("completed enrollment and declined migration disappear from owner decision history")
	}
	if len(events.Activity) != 2 {
		t.Fatal("decision activity missing", events)
	}
	for _, d := range history.Decisions {
		if d.At == nil || d.At.IsZero() || d.Subject == "" {
			t.Fatal("incomplete historical scope", d)
		}
		response, _ := f.request(t, "action", contract.OwnerAction{Action: "decide", Handle: d.Handle, Accept: true}, nil)
		if response.StatusCode == 200 {
			t.Fatal("history can execute a decision")
		}
	}
	f.session = f.login(t)
	var all []contract.OwnerDecision
	query := contract.OwnerQuery{View: "history", Limit: 1}
	for {
		page := f.query(t, query)
		all = append(all, page.Decisions...)
		if page.Next == "" {
			break
		}
		query.After = page.Next
	}
	if len(all) != 2 {
		t.Fatal("reopened history lost paged decisions", all)
	}
}
