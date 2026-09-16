package rpcstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEscapedRPCIdentity(t *testing.T) {
	b, _ := resourcebudget.New(8*resourcebudget.MiB, resourcebudget.MiB)
	server := mcp.NewServer(&mcp.Implementation{Name: "review", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "ownward_create", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		call, err := FromContext(ctx).Resolve(r.Params.Name, r.Params.Arguments)
		if err != nil {
			return nil, err
		}
		return call.Result(ctx, func(w io.Writer) error { _, err := io.WriteString(w, `{"content":"retained"}`); return err }, nil)
	})
	next := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	bridge := HTTP(next, t.TempDir(), b, 32*resourcebudget.MiB)
	server.AddReceivingMiddleware(bridge.Middleware)
	host := httptest.NewServer(bridge)
	defer host.Close()
	session := ""
	post := func(body string) (int, []byte) {
		req, _ := http.NewRequest("POST", host.URL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		res, err := host.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if v := res.Header.Get("Mcp-Session-Id"); v != "" {
			session = v
		}
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, raw
	}
	code, body := post(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"review","version":"1"}}}`)
	if code != 200 {
		t.Fatalf("initialize: %d %s", code, body)
	}
	post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	for _, id := range []string{`"a"`, `"\u0062"`, `"<c>"`} {
		code, body = post(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"ownward_create","arguments":{"content":"retained"}}}`, id))
		var envelope map[string]any
		_ = json.Unmarshal(body, &envelope)
		t.Logf("id=%s status=%d body=%s", id, code, body)
		if code != 200 || !bytes.Contains(body, []byte("retained")) {
			t.Errorf("valid RPC identity lost result: %s", id)
		}
	}
}

func TestRPCIdentityEquivalenceAndIsolation(t *testing.T) {
	for _, pair := range []struct {
		a, b  string
		equal bool
	}{
		{`"\u0062"`, `"b"`, true}, {`"<c>"`, `"\u003cc\u003e"`, true},
		{`1e0`, `1`, true}, {`-0`, `0`, true}, {`"1"`, `1`, false},
		{`"b"`, `"c"`, false}, {`null`, `null`, false}, {`true`, `true`, false},
	} {
		if sameRPCIdentity([]byte(pair.a), []byte(pair.b)) != pair.equal {
			t.Fatalf("identity %s vs %s", pair.a, pair.b)
		}
	}
	s := testScope(t)
	project := func(id string) (*Call, error) {
		_, c, err := s.Project(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":`+id+`,"method":"tools/call","params":{"name":"ownward_create","arguments":{"content":"x"}}}`))
		return c, err
	}
	c, err := project(`"\u0062"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = project(`"b"`); err == nil {
		t.Fatal("equivalent active ID accepted twice")
	}
	c.Close()
	if _, err = project(`"b"`); err != nil {
		t.Fatal("finished ID could not be reused", err)
	}
	if _, err = project(`1`); err != nil {
		t.Fatal(err)
	}
	if _, err = project(`"1"`); err != nil {
		t.Fatal("string and numeric identities conflated", err)
	}
}

func TestIOEscapedRPCIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := testScope(t)
	incoming, send := io.Pipe()
	receive, outgoing := io.Pipe()
	defer send.Close()
	defer receive.Close()
	conn, err := (&IO{Scope: s, Reader: incoming, Writer: outgoing}).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		fmt.Fprintln(send, `{"jsonrpc":"2.0","id":"\u0062","method":"tools/call","params":{"name":"ownward_create","arguments":{"content":"retained"}}}`)
	}()
	message, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req := message.(*jsonrpc.Request)
	var params struct {
		Name      string
		Arguments json.RawMessage
	}
	if err = json.Unmarshal(req.Params, &params); err != nil {
		t.Fatal(err)
	}
	call, err := s.Resolve(params.Name, params.Arguments)
	if err != nil {
		t.Fatal(err)
	}
	result, err := call.Result(ctx, func(w io.Writer) error { _, err := io.WriteString(w, `{"content":"retained"}`); return err }, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	sent := make(chan error, 1)
	go func() { sent <- conn.Write(ctx, &jsonrpc.Response{ID: req.ID, Result: raw}) }()
	line, err := bufio.NewReader(receive).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err = <-sent; err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(line, []byte("retained")) || bytes.Contains(line, []byte(outputField)) || bytes.Contains(line, []byte(call.token)) {
		t.Fatalf("result not restored: %s", line)
	}
}

func TestClientEscapedRPCIdentity(t *testing.T) {
	s := testScope(t)
	raw, call, err := s.Project(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":"\u0062","method":"tools/call","params":{"name":"ownward_create","arguments":{"content":"retained"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer call.Close()
	tr := &RoundTripper{Scope: s, Next: testTransport(func(r *http.Request) (*http.Response, error) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":"b","result":{"structuredContent":{"content":"retained"},"content":[{"type":"text","text":"retained"}]}}`))}, nil
	})}
	req, _ := http.NewRequest(http.MethodPost, "http://unused.invalid", bytes.NewReader(raw))
	response, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	envelope, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = call.Expand(context.Background(), &out, envelope); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "retained") {
		t.Fatal("result lost")
	}
}
