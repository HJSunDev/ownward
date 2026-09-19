package rpcstream

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestConnectorControlCallsKeepAuthenticationAndBody(t *testing.T) {
	for _, name := range []string{"ownward_manage", "ownward_management_status", "ownward_check"} {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{"id": "request"}}})
			transport := RoundTripper{Scope: testScope(t), Next: testTransport(func(r *http.Request) (*http.Response, error) {
				got, _ := io.ReadAll(r.Body)
				if !bytes.Equal(got, body) || r.Header.Get("X-Ownward-Principal") != "test-principal" {
					t.Fatal("control request changed")
				}
				return &http.Response{StatusCode: 403, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("denied"))}, nil
			})}
			req, _ := http.NewRequest(http.MethodPost, "http://unused.invalid", bytes.NewReader(body))
			req.Header.Set("X-Ownward-Principal", "test-principal")
			res, err := transport.RoundTrip(req)
			if err != nil || res.StatusCode != 403 {
				t.Fatalf("server denial not preserved: %v", err)
			}
			defer res.Body.Close()
		})
	}
}

func TestConnectorControlCannotBypassReferenceOrEnvelopeLimits(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		args      map[string]any
		oversized bool
	}{
		{"ownward_create", map[string]any{"content": "source"}, false},
		{"ownward_manage", map[string]any{inputField: "unknown"}, false},
		{"ownward_manage", map[string]any{"id": "operation"}, true},
	} {
		calls := 0
		transport := RoundTripper{Scope: testScope(t), Next: testTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", EnvelopeBytes+1)))}, nil
		})}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": scenario.name, "arguments": scenario.args}})
		req, _ := http.NewRequest(http.MethodPost, "http://unused.invalid", bytes.NewReader(body))
		if _, err := transport.RoundTrip(req); err == nil {
			t.Fatal("invalid request or oversized response accepted")
		}
		if !scenario.oversized && calls != 0 {
			t.Fatal("invalid reference sent to server")
		}
	}
}
