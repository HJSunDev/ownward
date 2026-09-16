package rpcstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testScope(t *testing.T) *Scope {
	t.Helper()
	b, _ := resourcebudget.New(8*resourcebudget.MiB, resourcebudget.MiB)
	s := New(t.TempDir(), b, 32*resourcebudget.MiB)
	t.Cleanup(func() { s.Close() })
	return s
}
func echoServer(t *testing.T) *mcp.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "stream-check", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "ownward_create", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"content": map[string]any{"type": "string"}}, "required": []string{"content"}}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		s := FromContext(ctx)
		if s == nil {
			return nil, fmt.Errorf("缺少请求级流式上下文")
		}
		call, err := s.Resolve(request.Params.Name, request.Params.Arguments)
		if err != nil {
			return nil, err
		}
		content, ok, err := call.Arguments.Field("content")
		if err != nil || !ok || content.Kind != '"' {
			return nil, fmt.Errorf("缺少正文")
		}
		return call.Result(ctx, func(w io.Writer) error {
			_, err := io.WriteString(w, `{"content":`)
			if err == nil {
				err = content.Copy(w)
			}
			if err == nil {
				_, err = io.WriteString(w, "}")
			}
			return err
		}, nil)
	})
	return server
}

func TestHTTPActualSDKAndClientStreaming(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s := testScope(t)
	server := echoServer(t)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	bridge := HTTP(h, t.TempDir(), s.Budget, 32*resourcebudget.MiB)
	server.AddReceivingMiddleware(bridge.Middleware)
	host := httptest.NewServer(bridge)
	defer host.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "stream-client", Version: "1"}, nil)
	connection, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: host.URL, HTTPClient: &http.Client{Transport: &RoundTripper{Scope: s, Next: host.Client().Transport}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	content := strings.Repeat("中文\"\\\n<>&😀", 150000)
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 17, "method": "tools/call", "params": map[string]any{"name": "ownward_create", "arguments": map[string]any{"content": content}}})
	projected, call, err := s.Project(ctx, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close()
	var wire struct {
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	json.Unmarshal(projected, &wire)
	result, err := connection.CallTool(ctx, &mcp.CallToolParams{Name: wire.Params.Name, Arguments: wire.Params.Arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool error: %+v", result.Content)
	}
	envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 17, "result": result})
	var output bytes.Buffer
	if err = call.Expand(ctx, &output, envelope); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Result struct {
			StructuredContent struct {
				Content string `json:"content"`
			} `json:"structuredContent"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err = json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Result.StructuredContent.Content != content {
		t.Fatal("structured正文改变")
	}
	var text struct {
		Content string `json:"content"`
	}
	if err = json.Unmarshal([]byte(decoded.Result.Content[0].Text), &text); err != nil || text.Content != content {
		t.Fatal("文本结果正文改变", err)
	}
	if bytes.Contains(output.Bytes(), []byte(call.token)) {
		t.Fatal("内部引用泄露到网络")
	}
}

func TestReferenceIsolationAndCanonicalDigest(t *testing.T) {
	s := testScope(t)
	other := testScope(t)
	ctx := context.Background()
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ownward_create","arguments":{"z":1.0,"content":"中文<&","a":[true,null]}}}`
	projected, c, err := s.Project(ctx, strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	var rpc struct {
		Params struct {
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	json.Unmarshal(projected, &rpc)
	if _, err = other.Resolve("ownward_create", rpc.Params.Arguments); err == nil {
		t.Fatal("跨连接引用被接受")
	}
	if _, err = s.Resolve("ownward_update", rpc.Params.Arguments); err == nil {
		t.Fatal("跨工具引用被接受")
	}
	digest, err := c.Digest(ctx)
	if err != nil || len(digest) != 64 {
		t.Fatal(err, digest)
	}
	fake := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ownward_create","arguments":` + string(rpc.Params.Arguments) + `}}`
	_, forged, err := s.Project(ctx, strings.NewReader(fake))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := forged.Arguments.Field("content"); ok {
		t.Fatal("外部伪造字段取得原始正文")
	}
	c.Close()
	if _, err = s.Resolve("ownward_create", rpc.Params.Arguments); err == nil {
		t.Fatal("已结束引用仍可读取")
	}
}

func TestDeliveryGuardRunsBeforeOutput(t *testing.T) {
	s := testScope(t)
	ctx := context.Background()
	_, c, err := s.Project(ctx, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ownward_read","arguments":{"id":"a"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Result(ctx, func(w io.Writer) error { return streamjson.WriteString(ctx, w, strings.NewReader("private")) }, func() error { return fmt.Errorf("revoked") })
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": 1, "result": r})
	var out bytes.Buffer
	if err = c.Expand(ctx, &out, raw); err == nil || out.Len() != 0 {
		t.Fatal("撤销后仍交付了内容")
	}
}

func TestCloseCancelsResultAndReleasesSpools(t *testing.T) {
	s := testScope(t)
	_, c, err := s.Project(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ownward_read","arguments":{"id":"a"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := c.Result(c.Context(), func(w io.Writer) error {
			close(started)
			_, err := io.WriteString(w, `"`)
			for err == nil {
				_, err = io.WriteString(w, strings.Repeat("a", 4096))
			}
			return err
		}, nil)
		done <- err
	}()
	<-started
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("已取消生成仍成功")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消未结束结果生成")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("取消后关闭未完成")
	}
	if s.Disk.Used() != 0 {
		t.Fatal("取消后暂存额度未释放", s.Disk.Used())
	}
}

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCloseCancelsInflightHTTP(t *testing.T) {
	s := testScope(t)
	raw, c, err := s.Project(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ownward_create","arguments":{"content":"正文"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	transport := RoundTripper{Scope: s, Next: testTransport(func(r *http.Request) (*http.Response, error) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	go func() {
		req, _ := http.NewRequest(http.MethodPost, "http://unused.invalid", bytes.NewReader(raw))
		_, err := transport.RoundTrip(req)
		done <- err
	}()
	<-started
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消未传递到HTTP")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP取消未收敛")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP取消后引用未关闭")
	}
}
