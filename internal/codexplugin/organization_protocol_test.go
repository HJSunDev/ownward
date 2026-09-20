package codexplugin

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("ORGANIZATION_PROTOCOL_TEST"); mode != "" && len(os.Args) > 1 && os.Args[1] == "app-server" {
		// 隔离的本地协议故障进程；不调用模型或网络。
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var req struct {
				ID     json.RawMessage
				Method string
			}
			if json.Unmarshal(scanner.Bytes(), &req) != nil {
				os.Exit(2)
			}
			var result any = map[string]any{}
			switch req.Method {
			case "initialize":
				if mode == "initialize-lost" {
					os.Exit(0)
				}
			case "initialized":
				continue
			case "config/read":
				result = map[string]any{"config": map[string]any{}}
			case "thread/start":
				result = map[string]any{"thread": map[string]any{"id": "fixture-thread"}, "model": "probe", "modelProvider": "probe"}
			case "turn/start":
				if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "start-received"), []byte("received"), 0600); err != nil {
					os.Exit(3)
				}
				os.Exit(0)
			default:
				os.Exit(4)
			}
			if json.NewEncoder(os.Stdout).Encode(map[string]any{"id": req.ID, "result": result}) != nil {
				os.Exit(5)
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestOrganizationLostStartRecordsUnknownUsage(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows executor")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"initialize-lost", "start-lost"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("ORGANIZATION_PROTOCOL_TEST", mode)
			home := t.TempDir()
			p := OrganizationProfile{Executable: exe, Home: home, Model: "probe", Provider: "probe", Effort: "low", Reservation: "offline-fixture", MemoryMiB: 128, TimeoutSeconds: 5, MaxSubmissions: 1, MaxAttempts: 1, MaxTokens: 100}
			tool := &mcp.Tool{Name: "ownward_semantic_submit", InputSchema: map[string]any{"type": "object"}}
			u, err := RunOrganization(context.Background(), p, json.RawMessage(`[]`), tool,
				func(context.Context, json.RawMessage) (*mcp.CallToolResult, bool, error) {
					t.Error("unexpected submission")
					return nil, false, nil
				},
				func(OrganizationUsage) error { return nil })
			if err == nil {
				t.Fatal("fault did not interrupt")
			}
			_, markerErr := os.Stat(filepath.Join(home, "start-received"))
			started := mode == "start-lost"
			if (markerErr == nil) != started || u.UsageIncomplete != started || u.Turn != "" || u.TotalTokens != 0 {
				t.Fatal("incorrect uncertainty boundary", mode, markerErr, u)
			}
		})
	}
}
