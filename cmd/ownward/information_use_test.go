package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/rpcstream"

	"github.com/HJSunDev/ownward/internal/codexplugin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 薄化后的 ownward_organize：暴露不依赖执行器与环境开关；调用即领取并把
// 组织材料交回调用者；已有持有者如实返回。测试不启动任何执行器。
func TestOrganizeThinContractWithoutExecutor(t *testing.T) {
	ctx := context.Background()
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case controlPrefix + "self":
			json.NewEncoder(w).Encode(contract.Principal{ID: "local", Revision: 1, Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission}})
		default:
			json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer control.Close()
	work := json.RawMessage(`[{"schema":"ownward.semantic-work/v1","id":"w1"}]`)
	call := func(c context.Context, request *mcp.CallToolRequest, name string, args any) (*mcp.CallToolResult, error) {
		var payload map[string]any
		switch name {
		case "ownward_status":
			payload = map[string]any{"organization": map[string]any{"status": "pending", "required_action": "ownward_semantic_jobs"}}
		case "ownward_semantic_jobs":
			if args.(map[string]any)["action"] == "claim" {
				payload = map[string]any{"available": true, "claim": map[string]any{"asset_id": "a1", "revision": 1, "generation": "g", "lease": "lease-1", "expires_at": time.Now().Add(time.Minute)}}
			}
		case "ownward_semantic_work":
			payload = map[string]any{"work": work}
		}
		b, _ := json.Marshal(payload)
		return &mcp.CallToolResult{StructuredContent: payload, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
	}
	connect := func(h *hostConnector, callFn func(ctx context.Context, request *mcp.CallToolRequest, name string, args any) (*mcp.CallToolResult, error)) *mcp.ClientSession {
		t.Helper()
		proxy := newConnectorServer("test", &mcp.InitializeResult{})
		h.addOrganizationDemand(proxy, callFn)
		ct, st := mcp.NewInMemoryTransports()
		ss, err := proxy.Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ss.Close() })
		client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { client.Close() })
		return client
	}
	if organizeRequestID("p", "a") != organizeRequestID("p", "a") || organizeRequestID("p", "a") == organizeRequestID("p", "b") {
		t.Fatal("organize request identity must be stable per principal and asset")
	}
	host := &hostConnector{descriptor: &sharedMCPDescriptor{Endpoint: control.URL}, record: connectorRecord{Credential: "test-local"}}
	client := connect(host, call)
	catalog, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range catalog.Tools {
		found = found || tool.Name == "ownward_organize"
	}
	if !found {
		t.Fatal("ownward_organize must be exposed without a registered executor")
	}
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_organize", Arguments: map[string]any{"id": "a1"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		b, _ := json.Marshal(result)
		t.Fatal(string(b))
	}
	body, _ := json.Marshal(result.StructuredContent)
	var out struct {
		Status string                     `json:"status"`
		Lease  contract.OrganizationLease `json:"lease"`
		Work   json.RawMessage            `json:"work"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "claimed" || out.Lease.Lease != "lease-1" || len(out.Work) == 0 {
		t.Fatalf("thin organize must return claim and work, got %s", body)
	}
	// 已有持有者：claim 返回 available=false 时必须如实报 held（而非"没有待办"）。
	heldCall := func(c context.Context, request *mcp.CallToolRequest, name string, args any) (*mcp.CallToolResult, error) {
		if name == "ownward_semantic_jobs" {
			payload := map[string]any{"available": false}
			b, _ := json.Marshal(payload)
			return &mcp.CallToolResult{StructuredContent: payload, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
		}
		return call(c, request, name, args)
	}
	heldClient := connect(host, heldCall)
	heldResult, err := heldClient.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_organize", Arguments: map[string]any{"id": "a1"}})
	if err != nil {
		t.Fatal(err)
	}
	if heldResult.IsError {
		b, _ := json.Marshal(heldResult)
		t.Fatal(string(b))
	}
	heldBody, _ := json.Marshal(heldResult.StructuredContent)
	var held struct {
		Status       string                     `json:"status"`
		Organization contract.OrganizationState `json:"organization"`
	}
	if err := json.Unmarshal(heldBody, &held); err != nil {
		t.Fatal(err)
	}
	if held.Status != "held" || held.Organization.RequiredAction != "ownward_semantic_jobs" {
		t.Fatalf("held path must report holder continuation, got %s", heldBody)
	}
}

func TestInformationUseNativeOptInAndRollback(t *testing.T) {
	for _, mode := range []string{"", "v1", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("OWNWARD_INFORMATION_USE_PATHS", mode)
			upstream := &mcp.InitializeResult{Instructions: "Unchanged kernel rules"}
			server := newConnectorServer("test", upstream)
			ct, st := mcp.NewInMemoryTransports()
			ss, err := server.Connect(context.Background(), st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ss.Close()
			client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture"}, nil).Connect(context.Background(), ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			got := client.InitializeResult().Instructions
			if mode == "v1" {
				if got != upstream.Instructions+"\n\n"+codexplugin.InformationUseInstructions {
					t.Fatal("contract not delivered unchanged")
				}
			} else if got != upstream.Instructions {
				t.Fatal("default deep path changed")
			}
			if strings.Count(got, upstream.Instructions) != 1 {
				t.Fatal("duplicate rules")
			}
		})
	}
}

// 首屏须交付完整工具集，且保留64KiB传输上限与所有模型输入定义。
func TestConnectorCompleteToolCatalogFitsEnvelope(t *testing.T) {
	f := newHostFixture(t)
	ctx := context.Background()
	connect := func(server *mcp.Server) *mcp.ClientSession {
		ct, st := mcp.NewInMemoryTransports()
		ss, err := server.Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ss.Close() })
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "catalog-fixture"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cs.Close() })
		return cs
	}
	upstream := connect(mcpserver.New(f.product, "fixture").MCP())
	proxy := newConnectorServer("fixture", upstream.InitializeResult())
	count := 0
	for tool, err := range upstream.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		compact := connectorTool(tool)
		if !reflect.DeepEqual(compact.InputSchema, tool.InputSchema) || compact.Description != tool.Description || compact.Name != tool.Name {
			t.Fatal("model contract changed")
		}
		proxy.AddTool(&compact, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) { return nil, nil })
		count++
	}
	client := connect(proxy)
	catalog, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.NextCursor != "" || len(catalog.Tools) != count {
		t.Fatal("formal host misses tools", len(catalog.Tools), count, catalog.NextCursor)
	}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": catalog})
	if err != nil {
		t.Fatal(err)
	}
	// 为接入专用工具预留空间；完整正式目录另经实际stdio入口验证。
	if len(raw) > rpcstream.EnvelopeBytes-4096 {
		t.Fatal("tool catalog exceeds reserved envelope", len(raw))
	}
	t.Logf("complete kernel tool catalog: %d tools, %d bytes", count, len(raw))
}
