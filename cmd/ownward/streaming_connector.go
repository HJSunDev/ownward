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
	resourcebudget.LimitRuntime(12 * resourcebudget.MiB)
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

func runConnectorIO(ctx context.Context, proxy *mcp.Server, scope *rpcstream.Scope, input io.ReadCloser, output io.WriteCloser, stopOrganization ...func()) error {
	// 执行器先停止并释放凭据，再关闭共享通道、回收工作目录。
	defer func() {
		for _, stop := range stopOrganization {
			stop()
		}
		if scope != nil {
			scope.Close()
			os.Remove(scope.Dir)
		}
	}()
	if scope == nil {
		return proxy.Run(ctx, &mcp.IOTransport{Reader: input, Writer: output})
	}
	return proxy.Run(rpcstream.WithScope(ctx, scope), &rpcstream.IO{Scope: scope, Reader: input, Writer: output, SharedScope: true})
}
