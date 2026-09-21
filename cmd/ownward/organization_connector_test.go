package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/rpcstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOrganizationWriteRetryKeepsOriginalMode(t *testing.T) {
	ctx := context.Background()
	budget, _ := resourcebudget.New(2*resourcebudget.MiB, 128*1024)
	scope := rpcstream.New(t.TempDir(), budget, 16*resourcebudget.MiB)
	defer scope.Close()
	ctx = rpcstream.WithScope(ctx, scope)
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"generation": 1})
	}))
	defer control.Close()
	h := &hostConnector{descriptor: &sharedMCPDescriptor{Endpoint: control.URL}, system: "test", profile: "test", vault: localowner.Vault{Root: t.TempDir()}, organization: &organizationHost{wake: make(chan struct{}, 1)}}
	t.Setenv("OWNWARD_DEFERRED_WRITE", "1")
	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	var modes, operations []string
	server.AddTool(&mcp.Tool{Name: "ownward_create", InputSchema: map[string]any{"type": "object"}}, func(c context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		call, e := scope.Resolve(r.Params.Name, r.Params.Arguments)
		if e != nil {
			return nil, e
		}
		var body struct{ Content, OrganizationMode string }
		var raw struct {
			Content string `json:"content"`
			Mode    string `json:"organization_mode"`
		}
		if e = call.Arguments.DecodeSmall(&raw, 4096); e != nil {
			return nil, e
		}
		body.Content = raw.Content
		body.OrganizationMode = raw.Mode
		if body.Content != "unaltered source" {
			t.Error("source changed")
		}
		modes = append(modes, body.OrganizationMode)
		operations = append(operations, r.Params.Meta["ownward/operation"].(string))
		return &mcp.CallToolResult{IsError: len(modes) == 1}, nil
	})
	ct, st := mcp.NewInMemoryTransports()
	ss, e := server.Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	client, e := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	for i := 0; i < 2; i++ {
		envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": "tools/call", "params": map[string]any{"name": "ownward_create", "arguments": map[string]any{"content": "unaltered source"}}})
		projected, call, e := scope.Project(ctx, bytes.NewReader(envelope))
		if e != nil {
			t.Fatal(e)
		}
		var request struct{ Params mcp.CallToolParamsRaw }
		if e = json.Unmarshal(projected, &request); e != nil {
			t.Fatal(e)
		}
		if _, e = h.callProduct(ctx, &mcp.CallToolRequest{Params: &request.Params}, client); e != nil {
			t.Fatal(e)
		}
		call.Close()
		h.organization.ready.Store(false)
	}
	if len(modes) != 2 || modes[0] != "deferred-v1" || modes[1] != modes[0] || operations[0] != operations[1] {
		t.Fatal("retry identity drift", modes, operations)
	}
	if len(h.record.Mutations) != 0 || len(h.record.Deferred) != 0 {
		t.Fatal("completed operation not cleared")
	}
}

func TestDeferredWriteGateIndependentOfExecutor(t *testing.T) {
	run := func(t *testing.T, switchValue string, executorReady bool) string {
		ctx := context.Background()
		budget, _ := resourcebudget.New(2*resourcebudget.MiB, 128*1024)
		scope := rpcstream.New(t.TempDir(), budget, 16*resourcebudget.MiB)
		defer scope.Close()
		ctx = rpcstream.WithScope(ctx, scope)
		control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"generation": 1})
		}))
		defer control.Close()
		h := &hostConnector{descriptor: &sharedMCPDescriptor{Endpoint: control.URL}, system: "test", profile: "test", vault: localowner.Vault{Root: t.TempDir()}}
		if executorReady {
			h.organization = &organizationHost{wake: make(chan struct{}, 1)}
			h.organization.ready.Store(true)
		}
		t.Setenv("OWNWARD_DEFERRED_WRITE", switchValue)
		server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
		var mode string
		server.AddTool(&mcp.Tool{Name: "ownward_create", InputSchema: map[string]any{"type": "object"}}, func(c context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			call, e := scope.Resolve(r.Params.Name, r.Params.Arguments)
			if e != nil {
				return nil, e
			}
			var raw struct {
				Mode string `json:"organization_mode"`
			}
			if e = call.Arguments.DecodeSmall(&raw, 4096); e != nil {
				return nil, e
			}
			mode = raw.Mode
			return &mcp.CallToolResult{}, nil
		})
		ct, st := mcp.NewInMemoryTransports()
		ss, e := server.Connect(ctx, st, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer ss.Close()
		client, e := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer client.Close()
		envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "ownward_create", "arguments": map[string]any{"content": "source"}}})
		projected, call, e := scope.Project(ctx, bytes.NewReader(envelope))
		if e != nil {
			t.Fatal(e)
		}
		defer call.Close()
		var request struct{ Params mcp.CallToolParamsRaw }
		if e = json.Unmarshal(projected, &request); e != nil {
			t.Fatal(e)
		}
		if _, e = h.callProduct(ctx, &mcp.CallToolRequest{Params: &request.Params}, client); e != nil {
			t.Fatal(e)
		}
		return mode
	}
	if mode := run(t, "", true); mode != "" {
		t.Fatalf("executor readiness must not enable deferred write, got %q", mode)
	}
	if mode := run(t, "1", false); mode != "deferred-v1" {
		t.Fatalf("explicit switch must enable deferred write, got %q", mode)
	}
}

func TestOrganizationRewritePreservesExplicitModeAndFields(t *testing.T) {
	budget, _ := resourcebudget.New(2*resourcebudget.MiB, 128*1024)
	scope := rpcstream.New(t.TempDir(), budget, 16*resourcebudget.MiB)
	defer scope.Close()
	for _, args := range []string{`{"content":"unaltered","organization_mode":""}`, `{"items":[{"content":"first"},{"content":"second","organization_mode":""}]}`} {
		name := "ownward_create"
		if bytes.Contains([]byte(args), []byte(`"items"`)) {
			name = "ownward_create_batch"
		}
		msg := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`)
		projected, call, e := scope.Project(context.Background(), bytes.NewReader(msg))
		if e != nil {
			t.Fatal(e)
		}
		var req struct{ Params mcp.CallToolParamsRaw }
		json.Unmarshal(projected, &req)
		h := &hostConnector{}
		cleanup, e := h.deferOrganization(rpcstream.WithScope(context.Background(), scope), &mcp.CallToolRequest{Params: &req.Params}, true)
		if e != nil {
			t.Fatal(e)
		}
		var b bytes.Buffer
		e = call.Arguments.Copy(&b)
		data := b.Bytes()
		if e != nil {
			t.Fatal(e)
		}
		if name == "ownward_create" && string(data) != args {
			t.Fatal(string(data))
		}
		if name == "ownward_create_batch" && !bytes.Contains(data, []byte(`{"organization_mode":"deferred-v1","content":"first"}`)) {
			t.Fatal(string(data))
		}
		cleanup()
		call.Close()
	}
}
