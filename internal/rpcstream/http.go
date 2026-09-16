package rpcstream

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type scopeKey struct{}

func FromContext(ctx context.Context) *Scope { s, _ := ctx.Value(scopeKey{}).(*Scope); return s }
func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// HTTP 在现有认证中间件之后、SDK之前装配。非POST会话操作仍由SDK处理。
func HTTP(next http.Handler, dir string, budget *resourcebudget.Budget, diskBytes int64) *HTTPBridge {
	bridge := &HTTPBridge{active: map[string]context.Context{}}
	disk := resourcebudget.NewDisk(diskBytes)
	admission := make(chan struct{}, 4)
	bridge.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		select {
		case admission <- struct{}{}:
			defer func() { <-admission }()
		default:
			http.Error(w, "在途请求已满", 503)
			return
		}
		s := New(dir, budget, diskBytes)
		s.Disk = disk
		defer s.Close()
		body, call, err := s.Project(r.Context(), r.Body)
		r.Body.Close()
		if err != nil {
			http.Error(w, "无效或无法暂存的RPC输入", http.StatusBadRequest)
			return
		}
		projected := r.Clone(WithScope(resourcebudget.WithDisk(r.Context(), disk), s))
		var nonce [32]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			http.Error(w, "无法绑定请求", 500)
			return
		}
		key := hex.EncodeToString(nonce[:])
		bridge.mu.Lock()
		if len(bridge.active) >= 32 {
			bridge.mu.Unlock()
			http.Error(w, "在途请求已满", 503)
			return
		}
		bridge.active[key] = projected.Context()
		bridge.mu.Unlock()
		defer func() { bridge.mu.Lock(); delete(bridge.active, key); bridge.mu.Unlock() }()
		projected.Header.Set(scopeHeader, key)
		projected.Body = io.NopCloser(bytes.NewReader(body))
		projected.ContentLength = int64(len(body))
		projected.Header.Set("Content-Length", "")
		capture := &response{header: make(http.Header), status: 200}
		next.ServeHTTP(capture, projected)
		if capture.err != nil {
			http.Error(w, "RPC输出超过信封预算", http.StatusInternalServerError)
			return
		}
		if call != nil && call.check != nil {
			if err = call.check(); err != nil {
				http.Error(w, "交付前权限或来源已变化", http.StatusConflict)
				return
			}
		}
		for k, values := range capture.header {
			if k == "Content-Length" {
				continue
			}
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		if call == nil || capture.status != 200 {
			w.WriteHeader(capture.status)
			_, _ = w.Write(capture.body.Bytes())
			return
		}
		// 网络写失败由HTTP上下文收敛；操作回执仍保留，原操作可以接续。
		out := &deliveryWriter{ResponseWriter: w, status: capture.status}
		if err := call.Expand(r.Context(), out, capture.body.Bytes()); err != nil && !out.started {
			http.Error(w, "结果交付失败", http.StatusConflict)
		}
	})
	return bridge
}

type deliveryWriter struct {
	http.ResponseWriter
	status  int
	started bool
}

func (w *deliveryWriter) Write(p []byte) (int, error) {
	if !w.started {
		w.started = true
		w.WriteHeader(w.status)
	}
	return w.ResponseWriter.Write(p)
}

const scopeHeader = "X-Ownward-Local-Stream"

type HTTPBridge struct {
	handler http.Handler
	mu      sync.Mutex
	active  map[string]context.Context
}

func (b *HTTPBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) { b.handler.ServeHTTP(w, r) }

// SDK 会话上下文来自初始化；每次工具调用须恢复本次认证HTTP请求的上下文。
func (b *HTTPBridge) Middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		extra := request.GetExtra()
		if extra == nil || extra.Header == nil {
			return next(ctx, method, request)
		}
		b.mu.Lock()
		current := b.active[extra.Header.Get(scopeHeader)]
		b.mu.Unlock()
		if current == nil {
			return nil, errors.New("流式HTTP请求已结束或未绑定")
		}
		combined, cancel := context.WithCancel(current)
		defer cancel()
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		return next(combined, method, request)
	}
}

type response struct {
	header  http.Header
	status  int
	body    limitBuffer
	err     error
	started bool
}

func (r *response) Header() http.Header { return r.header }
func (r *response) WriteHeader(status int) {
	if !r.started {
		r.status = status
		r.started = true
	}
}
func (r *response) Write(p []byte) (int, error) {
	r.started = true
	n, err := r.body.Write(p)
	if err != nil {
		r.err = err
	}
	return n, err
}
func (r *response) Flush() {}
