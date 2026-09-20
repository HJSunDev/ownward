package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOrganizationRenewalRunsDuringSlowWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{})
	gate := make(chan struct{})
	var renewCalls atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "review"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ownward_semantic_work", Description: "controlled slow work"}, func(c context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct{}, error) {
		close(entered)
		select {
		case <-gate:
			return nil, struct{}{}, nil
		case <-c.Done():
			return nil, struct{}{}, c.Err()
		}
	})
	mcp.AddTool(server, &mcp.Tool{Name: "ownward_semantic_jobs", Description: "controlled renewal"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		renewCalls.Add(1)
		return nil, struct{}{}, nil
	})
	endpoint := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	var location contract.Location
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/remote/identity" {
			json.NewEncoder(w).Encode(remoteIdentity{Location: location})
			return
		}
		endpoint.ServeHTTP(w, r)
	}))
	defer api.Close()
	location = contract.Location{SystemID: "fixture", ServiceID: "service", Endpoint: api.URL}
	session, e := mcp.NewClient(&mcp.Implementation{Name: "review"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: api.URL, DisableStandaloneSSE: true, MaxRetries: 1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { session.Close() }()
	h := &hostConnector{system: "fixture", descriptor: &sharedMCPDescriptor{Endpoint: api.URL}, remote: &remoteConnection{Client: &http.Client{Transport: http.DefaultTransport}, Material: connectionMaterial{Location: location}}}
	run := func(c context.Context, name string) (*mcp.CallToolResult, error) {
		return h.remoteOrganizationCall(c, &session, name, struct{}{})
	}
	workDone := make(chan error, 1)
	go func() {
		_, e := run(ctx, "ownward_semantic_work")
		workDone <- e
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("slow work did not start")
	}
	renewCtx, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stop()
	renewDone := make(chan error, 1)
	go func() {
		_, e := run(renewCtx, "ownward_semantic_jobs")
		renewDone <- e
	}()
	var renewErr error
	returned := false
	select {
	case renewErr = <-renewDone:
		returned = true
	case <-time.After(650 * time.Millisecond):
	}
	callsBeforeRelease := renewCalls.Load()
	close(gate)
	if e := <-workDone; e != nil {
		t.Fatal(e)
	}
	if !returned {
		select {
		case renewErr = <-renewDone:
		case <-ctx.Done():
			t.Fatal("renewal blocked after work released")
		}
	}
	t.Logf("renewal reached service before work release=%d; returned within 650ms=%v; 300ms-deadline result=%v", callsBeforeRelease, returned, renewErr)
	if callsBeforeRelease != 1 || renewErr != nil {
		t.Error("slow remote work blocks concurrent renewal behind route refresh write lock")
	}
}
