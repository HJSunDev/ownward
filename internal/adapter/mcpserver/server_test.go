package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/embedding"
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
	service, err := core.NewCollaborative(store, state, embedding.HashForTesting{Dimensions: 64})
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
	if len(tools.Tools) != 11 {
		t.Fatalf("unexpected tool count: %d", len(tools.Tools))
	}
	toolsByName := make(map[string]*mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		toolsByName[tool.Name] = tool
		if tool.Annotations == nil || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %s must declare its closed-world boundary: %#v", tool.Name, tool.Annotations)
		}
	}
	for _, name := range []string{"ownward_rules", "ownward_read", "ownward_status", "ownward_search", "ownward_navigate", "ownward_semantic_work"} {
		if tool := toolsByName[name]; tool == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
			t.Fatalf("tool %s must be declared read-only and idempotent: %#v", name, tool)
		}
	}
	semanticSchema, err := json.Marshal(toolsByName["ownward_semantic_submit"].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"ownward.semantic-submission/v1", "complete", "uncertain", "outgoing", "incoming"} {
		if !strings.Contains(string(semanticSchema), required) {
			t.Fatalf("semantic tool schema does not explain %q: %s", required, semanticSchema)
		}
	}
	if tool := toolsByName["ownward_create"]; tool == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
		t.Fatalf("create must be declared additive: %#v", tool)
	}
	if tool := toolsByName["ownward_create_batch"]; tool == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
		t.Fatalf("batch create must be declared additive: %#v", tool)
	}
	if tool := toolsByName["ownward_update"]; tool == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
		t.Fatalf("update must retain its write boundary: %#v", tool)
	}
	if tool := toolsByName["ownward_semantic_submit"]; tool == nil || tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
		t.Fatalf("semantic submission must be an idempotent write: %#v", tool)
	}
	if tool := toolsByName["ownward_semantic_submit_batch"]; tool == nil || tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
		t.Fatalf("semantic batch submission must be an idempotent write: %#v", tool)
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

func TestStreamableHTTPUsesTheSameCore(t *testing.T) {
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
	service, err := core.NewCollaborative(store, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	httpServer := httptest.NewServer(New(service, "test").HTTPHandler())
	defer httpServer.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "streamable-http-test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	created, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "ownward_create", Arguments: map[string]any{"content": "同一常驻内核服务多个独立智能体会话。"},
	})
	if err != nil || created.IsError {
		t.Fatalf("streamable HTTP create failed: result=%#v error=%v", created, err)
	}
	if len(store.All()) != 1 {
		t.Fatalf("streamable HTTP did not reach the shared core: %#v", store.All())
	}
}
