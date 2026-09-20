package main

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOrganizationForegroundStatusAllowsPendingRouteWriter(t *testing.T) {
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "status-fixture"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ownward_status"}, func(context.Context, *mcp.CallToolRequest, struct {
		ID string `json:"id"`
	}) (*mcp.CallToolResult, struct {
		Organization contract.OrganizationState `json:"organization"`
	}, error) { return nil, struct {
		Organization contract.OrganizationState `json:"organization"`
	}{contract.OrganizationState{Status: "pending", ExecutionIdentity: "work", RequiredAction: "ownward_semantic_jobs"}}, nil })
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "status-client"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	j, err := openOrganizationJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	if _, err = j.db.Exec(`INSERT INTO execution_blocks VALUES('system:person:1','work','budget exhausted')`); err != nil {
		t.Fatal(err)
	}
	h := &hostConnector{system: "system", remote: &remoteConnection{}}
	o := &organizationHost{host: h, journal: j, call: func(context.Context, string, any) (*mcp.CallToolResult, error) {
		t.Error("foreground used background route wrapper")
		return nil, errors.New("wrong wrapper")
	}}
	request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "ownward_status", Arguments: json.RawMessage(`{"id":"asset"}`)}}
	h.routeMu.RLock()
	writerDone := make(chan struct{})
	go func() { h.routeMu.Lock(); h.routeMu.Unlock(); close(writerDone) }()
	deadline := time.Now().Add(time.Second)
	for h.routeMu.TryRLock() {
		h.routeMu.RUnlock()
		if time.Now().After(deadline) {
			h.routeMu.RUnlock()
			t.Fatal("writer did not queue")
		}
		runtime.Gosched()
	}
	type reply struct {
		value *mcp.CallToolResult
		err   error
	}
	done := make(chan reply, 1)
	go func() {
		r, e := o.statusRequest(ctx, request, contract.Principal{ID: "person", Revision: 1}, session)
		done <- reply{r, e}
	}()
	var got reply
	select {
	case got = <-done:
	case <-time.After(time.Second):
		h.routeMu.RUnlock()
		<-writerDone
		t.Fatal("status blocked behind pending route writer")
	}
	h.routeMu.RUnlock()
	<-writerDone
	if got.err != nil {
		t.Fatal(got.err)
	}
	var state struct{ Organization contract.OrganizationState }
	if err = decodeTool(got.value, &state); err != nil || state.Organization.RequiredAction != "restore_organization_execution" {
		t.Fatal(state, err)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = o.statusRequest(ctx, request, contract.Principal{}, session); !errors.Is(err, errRemoteUnavailable) {
		t.Fatalf("lost foreground reconnect signal: %v", err)
	}
}

func TestOrganizationBlockedStatusSurvivesRestartAndIsScoped(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	j, err := openOrganizationJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = j.db.Exec(`INSERT INTO execution_blocks VALUES('system:person:3','version-one','budget exhausted')`)
	if err != nil {
		t.Fatal(err)
	}
	j.close()
	j, err = openOrganizationJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	o := &organizationHost{journal: j, host: &hostConnector{system: "system"}}
	for _, tc := range []struct {
		name, identity, status string
		principal              contract.Principal
		blocked                bool
	}{
		{"same work after restart", "version-one", "pending", contract.Principal{ID: "person", Revision: 3}, true},
		{"other principal", "version-one", "pending", contract.Principal{ID: "other", Revision: 3}, false},
		{"new grant", "version-one", "pending", contract.Principal{ID: "person", Revision: 4}, false},
		{"new work", "version-two", "pending", contract.Principal{ID: "person", Revision: 3}, false},
		{"kernel completed elsewhere", "version-one", "ready", contract.Principal{ID: "person", Revision: 3}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &mcp.CallToolResult{StructuredContent: map[string]any{"organization": contract.OrganizationState{ExecutionIdentity: tc.identity, Status: tc.status, RequiredAction: "ownward_semantic_jobs"}}}
			r, err := o.annotateStatus(ctx, input, tc.principal)
			if err != nil {
				t.Fatal(err)
			}
			var out struct{ Organization contract.OrganizationState }
			if err = decodeTool(r, &out); err != nil {
				t.Fatal(err)
			}
			if (out.Organization.RequiredAction == "restore_organization_execution") != tc.blocked {
				t.Fatal(out)
			}
		})
	}
}
