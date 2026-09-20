package codexplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOrganizationOfficialAppServer(t *testing.T) {
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("no_proxy", "127.0.0.1,localhost")
	exe := os.Getenv("OWNWARD_TEST_CODEX_EXECUTABLE")
	if exe == "" {
		t.Skip("requires explicitly selected local Codex binary; no live model")
	}
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if e != nil {
			t.Error(e)
			return
		}
		var request struct {
			Tools []struct{ Type, Name string }
			Model string
		}
		if json.Unmarshal(body, &request) != nil {
			t.Error("bad model request")
			return
		}
		for _, tool := range request.Tools {
			if tool.Name != "ownward_semantic_submit" {
				t.Errorf("unexpected model-visible tool %s/%s", tool.Type, tool.Name)
			}
		}
		step := calls.Add(1)
		item := map[string]any{"id": fmt.Sprintf("msg_%d", step), "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Done", "annotations": []any{}}}}
		if step <= 2 {
			item = map[string]any{"id": fmt.Sprintf("fc_%d", step), "call_id": fmt.Sprintf("call_%d", step), "type": "function_call", "name": "ownward_semantic_submit", "arguments": fmt.Sprintf(`{"submission":{"value":%d}}`, step), "status": "completed"}
		}
		response := map[string]any{"id": fmt.Sprintf("resp_%d", step), "object": "response", "created_at": time.Now().Unix(), "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 10, "total_tokens": 20, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, packet := range []map[string]any{{"type": "response.created", "response": map[string]any{"id": response["id"], "status": "in_progress", "output": []any{}}}, {"type": "response.output_item.added", "output_index": 0, "item": item}, {"type": "response.output_item.done", "output_index": 0, "item": item}, {"type": "response.completed", "response": response}} {
			b, _ := json.Marshal(packet)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", packet["type"], b)
		}
	}))
	defer model.Close()
	home := t.TempDir()
	cfg := fmt.Sprintf("model=\"probe\"\nmodel_provider=\"probe\"\n[model_providers.probe]\nname=\"Local fixture\"\nbase_url=\"%s/v1\"\nwire_api=\"responses\"\nrequires_openai_auth=false\nrequest_max_retries=0\nstream_max_retries=0\n", model.URL)
	if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0600); e != nil {
		t.Fatal(e)
	}
	p := OrganizationProfile{Executable: exe, Home: home, Model: "probe", Provider: "probe", Effort: "low", Reservation: "local-controlled-model", MemoryMiB: 512, TimeoutSeconds: 40, MaxSubmissions: 3, MaxAttempts: 2, MaxTokens: 10000}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	tool := &mcp.Tool{Name: "ownward_semantic_submit", Description: "Submit organized information", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"submission": map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "integer"}}, "required": []string{"value"}}}, "required": []string{"submission"}}}
	submissions := 0
	u, e := RunOrganization(ctx, p, json.RawMessage(`[{"content":"Synthetic source"}]`), tool, func(ctx context.Context, args json.RawMessage) (*mcp.CallToolResult, bool, error) {
		submissions++
		if !strings.Contains(string(args), fmt.Sprintf(`"value":%d`, submissions)) {
			t.Fatal("altered AI submission", string(args))
		}
		return &mcp.CallToolResult{IsError: submissions == 1, Content: []mcp.Content{&mcp.TextContent{Text: "validation feedback"}}}, submissions == 2, nil
	}, func(u OrganizationUsage) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	if submissions != 2 || calls.Load() != 2 || u.ModelRequests != 2 || u.TotalTokens != 40 || u.UsageIncomplete || u.Submissions != 2 || u.PeakBytes == 0 || u.ProcessCount == 0 {
		t.Fatal("incomplete official host lifecycle", u, calls.Load())
	}
	t.Logf("actual App Server; local model requests=%d submissions=%d peak=%d processes=%d tokens=%d seconds=%.3f", calls.Load(), submissions, u.PeakBytes, u.ProcessCount, u.TotalTokens, u.Seconds)
	entries, _ := filepath.Glob(filepath.Join(home, "sessions", "*"))
	if len(entries) > 0 {
		t.Fatal("ephemeral source context persisted", entries)
	}
}
