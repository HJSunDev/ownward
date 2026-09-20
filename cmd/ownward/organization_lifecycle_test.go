package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/HJSunDev/ownward/internal/contract"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/rpcstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOrganizationBackgroundSessionRecoversAfterServerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	handler := func() http.Handler {
		server := mcp.NewServer(&mcp.Implementation{Name: "review", Version: "1"}, nil)
		mcp.AddTool(server, &mcp.Tool{Name: "ownward_semantic_jobs", Description: "local fixture"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
			return nil, struct{}{}, nil
		})
		return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	}
	var mu sync.RWMutex
	current := handler()
	var location contract.Location
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/remote/identity" {
			json.NewEncoder(w).Encode(remoteIdentity{Location: location})
			return
		}
		mu.RLock()
		h := current
		mu.RUnlock()
		h.ServeHTTP(w, r)
	}))
	defer api.Close()
	location = contract.Location{SystemID: "fixture", ServiceID: "service", Endpoint: api.URL}
	connect := func() *mcp.ClientSession {
		session, e := mcp.NewClient(&mcp.Implementation{Name: "review"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: api.URL, DisableStandaloneSSE: true, MaxRetries: 1}, nil)
		if e != nil {
			t.Fatal(e)
		}
		return session
	}
	session := connect()
	defer func() { session.Close() }()
	host := &hostConnector{system: "fixture", descriptor: &sharedMCPDescriptor{Endpoint: api.URL}, remote: &remoteConnection{Client: &http.Client{Transport: http.DefaultTransport}, Material: connectionMaterial{Location: location}}}
	if _, e := organizationToolCall(ctx, nil, session, "ownward_semantic_jobs", struct{}{}); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	current = handler()
	mu.Unlock() // same live endpoint, old server sessions lost
	old := session
	_, err := host.remoteOrganizationCall(ctx, &session, "ownward_semantic_jobs", struct{}{})
	if err != nil || session == old {
		t.Fatal("background did not recover session", err)
	}
	_, err = host.remoteOrganizationCall(ctx, &session, "ownward_semantic_jobs", struct{}{})
	if err != nil {
		t.Fatal("recovered session unusable", err)
	}
}

func TestOrganizationConnectorExitKeepsScopeUntilRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	budget, e := resourcebudget.New(12*resourcebudget.MiB, resourcebudget.MiB)
	if e != nil {
		t.Fatal(e)
	}
	scope := rpcstream.New(t.TempDir(), budget, 64*resourcebudget.MiB)
	defer scope.Close()
	server := mcp.NewServer(&mcp.Implementation{Name: "review"}, nil)
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	defer sr.Close()
	defer cw.Close()
	defer cr.Close()
	defer sw.Close()
	callback := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- runConnectorIO(ctx, server, scope, sr, sw, func() {
			_, call, e := scope.Project(context.Background(), bytes.NewBufferString(`{"jsonrpc":"2.0","id":"review-release","method":"tools/call","params":{"name":"ownward_semantic_jobs","arguments":{"action":"release","asset_id":"synthetic","lease":"synthetic"}}}`))
			if call != nil {
				call.Close()
			}
			callback <- e
		})
	}()
	client, e := mcp.NewClient(&mcp.Implementation{Name: "review-client"}, nil).Connect(ctx, &mcp.IOTransport{Reader: cr, Writer: cw}, nil)
	if e != nil {
		t.Fatal(e)
	}
	client.Close()
	select {
	case e := <-callback:
		t.Logf("lease release projection during executor stop callback: %v", e)
		if e != nil {
			t.Error("shared scope closed before organization stop callback can release its lease")
		}
	case <-ctx.Done():
		t.Fatal("connector did not stop")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("connector shutdown incomplete")
	}
}

func TestOrganizationCompactPreservesForeground(t *testing.T) {
	j, err := openOrganizationJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	o := &organizationHost{ctx: context.Background(), journal: j, active: map[string]bool{}, wake: make(chan struct{}, 1)}
	o.event("a", "UserPromptSubmit")
	o.event("a", "SessionStart")
	o.event("b", "UserPromptSubmit")
	o.event("b", "Stop")
	if active, err := j.foreground(o.ctx); err != nil || !active {
		t.Fatal("compaction or another session cleared active foreground", active, err)
	}
	o.event("a", "SessionClear")
	if active, err := j.foreground(o.ctx); err != nil || active {
		t.Fatal("clear retained obsolete turn", active, err)
	}
}
