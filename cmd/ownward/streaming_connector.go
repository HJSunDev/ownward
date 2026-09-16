package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/rpcstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type connectorTransport struct {
	base  http.RoundTripper
	scope atomic.Pointer[rpcstream.Scope]
}

func (t *connectorTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if scope := t.scope.Load(); scope != nil {
		return (&rpcstream.RoundTripper{Scope: scope, Next: t.base}).RoundTrip(r)
	}
	return t.base.RoundTrip(r)
}

// 仅与明确宣布有界存储协议的服务协商启用；旧用户服务维持既有接入路径。
func configureStreamingConnector(transport *connectorTransport, result *mcp.InitializeResult, dataDir string) (*rpcstream.Scope, error) {
	if result == nil || result.Capabilities == nil {
		return nil, nil
	}
	if _, ok := result.Capabilities.Experimental["ownward.bounded-storage"]; !ok {
		return nil, nil
	}
	budget, err := resourcebudget.New(2*resourcebudget.MiB, 128*1024)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "runtime"), 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(filepath.Join(dataDir, "runtime"), "stream-")
	if err != nil {
		return nil, err
	}
	scope := rpcstream.New(dir, budget, 256*resourcebudget.MiB)
	transport.scope.Store(scope)
	return scope, nil
}

func runConnectorIO(ctx context.Context, proxy *mcp.Server, scope *rpcstream.Scope, input io.ReadCloser, output io.WriteCloser) error {
	if scope == nil {
		return proxy.Run(ctx, &mcp.IOTransport{Reader: input, Writer: output})
	}
	defer scope.Close()
	defer os.Remove(scope.Dir)
	return proxy.Run(rpcstream.WithScope(ctx, scope), &rpcstream.IO{Scope: scope, Reader: input, Writer: output})
}
