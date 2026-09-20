package main

import (
	"context"
	"encoding/json"
	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/rpcstream"
	"reflect"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/codexplugin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
