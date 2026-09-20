package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/rpcstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConnectorToolDiscoveryFitsEnvelope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	budget, e := resourcebudget.New(8*resourcebudget.MiB, resourcebudget.MiB)
	if e != nil {
		t.Fatal(e)
	}
	service := mcpserver.NewStreamingStorage(nil, "review", t.TempDir(), budget, 32*resourcebudget.MiB)
	service.AddManagementTools(nil) // Same formal tool set as productServer; no tool handler is invoked.
	httpService := httptest.NewServer(service.HTTPHandler())
	defer httpService.Close()
	upstream, e := mcp.NewClient(&mcp.Implementation{Name: "review"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpService.URL, DisableStandaloneSSE: true}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer upstream.Close()
	var tools []*mcp.Tool
	for tool, e := range upstream.Tools(ctx, nil) {
		if e != nil {
			t.Fatal(e)
		}
		tools = append(tools, tool)
	}
	b, _ := json.Marshal(&mcp.ListToolsResult{Tools: tools})
	t.Logf("actual upstream tools=%d complete list bytes=%d envelope limit=%d", len(tools), len(b), rpcstream.EnvelopeBytes)
	t.Run("formal_local_and_remote_proxy", func(t *testing.T) {
		proxy := newConnectorServer("test", upstream.InitializeResult())
		for _, tool := range tools {
			copy := connectorTool(tool)
			proxy.AddTool(&copy, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
		}
		localBudget, e := resourcebudget.New(2*resourcebudget.MiB, 128*1024)
		if e != nil {
			t.Fatal(e)
		}
		scope := rpcstream.New(t.TempDir(), localBudget, 32*resourcebudget.MiB)
		serverRead, clientWrite := io.Pipe()
		clientRead, serverWrite := io.Pipe()
		local, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- runConnectorIO(local, proxy, scope, serverRead, serverWrite) }()
		defer func() {
			cancel()
			serverRead.Close()
			serverWrite.Close()
			clientRead.Close()
			clientWrite.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("connector did not finish")
			}
		}()
		session, e := mcp.NewClient(&mcp.Implementation{Name: "user-host"}, nil).Connect(local, &mcp.IOTransport{Reader: clientRead, Writer: clientWrite}, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer session.Close()
		count := 0
		for _, e := range session.Tools(local, nil) {
			if e != nil {
				t.Fatalf("tool discovery failed after %d tools: %v", count, e)
			}
			count++
		}
		if count != len(tools) {
			t.Fatalf("tools lost %d/%d", count, len(tools))
		}
		t.Logf("all %d tools discovered", count)
	})
}
