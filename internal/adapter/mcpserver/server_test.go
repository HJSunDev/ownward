package mcpserver

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerExposesUnifiedCoreOperations(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := core.NewOrganized(store, state, semantics.Heuristic{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := New(service, "test")
	serverSession, err := server.MCP().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result := clientSession.InitializeResult(); result == nil || !strings.Contains(result.Instructions, "复杂问题") {
		t.Fatalf("collaboration rules were not delivered by the server: %#v", result)
	}
	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 6 {
		t.Fatalf("unexpected tool count: %d", len(tools.Tools))
	}
	created, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "ownward_create",
		Arguments: map[string]any{
			"content":  "先验证真实目标，再执行破坏性操作。",
			"contexts": []map[string]any{{"key": "platform", "value": "windows"}},
		},
	})
	if err != nil || created.IsError {
		t.Fatalf("create failed: result=%#v error=%v", created, err)
	}
	values := store.All()
	if len(values) != 1 {
		t.Fatalf("create did not reach the authoritative store: %#v", values)
	}
	id := values[0].ID
	read, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_read", Arguments: map[string]any{"id": id}})
	if err != nil || read.IsError {
		t.Fatalf("read failed: result=%#v error=%v", read, err)
	}
	updated, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "ownward_update", Arguments: map[string]any{"id": id, "expected_revision": 1, "content": "先验证并解析真实目标，再执行破坏性操作。"},
	})
	if err != nil || updated.IsError {
		t.Fatalf("update failed: result=%#v error=%v", updated, err)
	}
	current, ok := store.Get(id)
	if !ok || current.Revision != 2 {
		t.Fatalf("update did not reach the authoritative store: %#v", current)
	}
	searched, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ownward_search",
		Arguments: map[string]any{"query": "破坏性操作", "limit": 10},
	})
	if err != nil || searched.IsError {
		t.Fatalf("search failed: result=%#v error=%v", searched, err)
	}
	navigated, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_navigate", Arguments: map[string]any{"start_ids": []string{id}}})
	if err != nil || navigated.IsError {
		t.Fatalf("navigate failed: result=%#v error=%v", navigated, err)
	}
	rules, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_rules", Arguments: map[string]any{}})
	if err != nil || rules.IsError {
		t.Fatalf("rules failed: result=%#v error=%v", rules, err)
	}
	if err := clientSession.Close(); err != nil {
		t.Fatal(err)
	}
	if err := serverSession.Wait(); err != nil {
		t.Fatal(err)
	}
}
