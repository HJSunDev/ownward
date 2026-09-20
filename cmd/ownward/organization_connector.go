package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/rpcstream"
	"github.com/HJSunDev/ownward/internal/streamjson"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type organizationBuffer struct{ bytes.Buffer }

// 后台沿用前台的可信路由和会话恢复；重试保留原领取/提交身份。
func (h *hostConnector) remoteOrganizationCall(ctx context.Context, session **mcp.ClientSession, name string, args any) (*mcp.CallToolResult, error) {
	var used *mcp.ClientSession
	run := func() (*mcp.CallToolResult, error) {
		h.routeMu.RLock()
		defer h.routeMu.RUnlock()
		used = *session
		return organizationToolCall(ctx, h.streaming, used, name, args)
	}
	result, err := run()
	if err == nil || ctx.Err() != nil {
		return result, err
	}
	// 正常续期不刷新路由，不排在慢 work/submit 后；恢复也不等待在途调用。
	if !h.routeMu.TryLock() {
		return nil, err
	}
	if *session == used {
		err = h.reconnectRemote(ctx, session)
	} else {
		err = nil
	}
	h.routeMu.Unlock()
	if err != nil {
		return nil, err
	}
	return run()
}

func (h *hostConnector) refreshOrganizationRoute(ctx context.Context, session **mcp.ClientSession) error {
	if !h.routeMu.TryLock() {
		return errRemoteUnavailable
	}
	defer h.routeMu.Unlock()
	return h.refreshRemote(ctx, session)
}

func (b *organizationBuffer) Write(p []byte) (int, error) {
	if len(p) > 16<<20-b.Len() {
		return 0, errors.New("组织工作副本超过边界")
	}
	return b.Buffer.Write(p)
}

func organizationToolCall(ctx context.Context, scope *rpcstream.Scope, session *mcp.ClientSession, name string, args any) (*mcp.CallToolResult, error) {
	if scope == nil {
		return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	}
	id := connectionID()
	envelope, e := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	if e != nil {
		return nil, e
	}
	projected, call, e := scope.Project(ctx, bytes.NewReader(envelope))
	if e != nil {
		return nil, e
	}
	defer call.Close()
	var wire struct {
		Params struct {
			Arguments json.RawMessage `json:"arguments"`
		}
	}
	if e = json.Unmarshal(projected, &wire); e != nil {
		return nil, e
	}
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: wire.Params.Arguments})
	if e != nil {
		return nil, e
	}
	response, e := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if e != nil {
		return nil, e
	}
	var expanded organizationBuffer
	if e = call.Expand(ctx, &expanded, response); e != nil {
		return nil, e
	}
	var out struct {
		Result *mcp.CallToolResult `json:"result"`
	}
	if e = json.Unmarshal(expanded.Bytes(), &out); e != nil {
		return nil, e
	}
	if out.Result == nil {
		return nil, errors.New("组织工具未返回结果")
	}
	return out.Result, nil
}

// Rewrite only the optional execution mode; all source fields stream unchanged.
func (h *hostConnector) deferOrganization(ctx context.Context, request *mcp.CallToolRequest, enabled bool) (func(), error) {
	if !enabled || !isAssetMutation(request.Params.Name) {
		return func() {}, nil
	}
	var args streamjson.Node
	scope := rpcstream.FromContext(ctx)
	var call *rpcstream.Call
	if scope != nil {
		var e error
		call, e = scope.Resolve(request.Params.Name, request.Params.Arguments)
		if e != nil {
			return nil, e
		}
		args = call.Arguments
	} else {
		return func() {}, nil
	}
	doc, e := streamjson.Build(ctx, scope.Dir, scope.Budget, scope.DiskBytes, func(w io.Writer) error {
		if request.Params.Name != "ownward_create_batch" {
			return deferredObject(args, w)
		}
		return args.Object(w, map[string]func(io.Writer) error{"items": func(w io.Writer) error {
			n, ok, e := args.Field("items")
			if e != nil {
				return e
			}
			if !ok || n.Kind != '[' {
				return errors.New("批量资料必须为数组")
			}
			if _, e = io.WriteString(w, "["); e != nil {
				return e
			}
			children := n.Children()
			first := true
			for {
				item, e := children.Next()
				if e == io.EOF {
					break
				}
				if e != nil {
					return e
				}
				if !first {
					if _, e = io.WriteString(w, ","); e != nil {
						return e
					}
				}
				first = false
				if e = deferredObject(item, w); e != nil {
					return e
				}
			}
			_, e = io.WriteString(w, "]")
			return e
		}})
	})
	if e != nil {
		return nil, e
	}
	old := call.Arguments
	call.Arguments = doc.Root()
	return func() {
		call.Arguments = old
		doc.Close()
		if h.organization != nil {
			select {
			case h.organization.wake <- struct{}{}:
			default:
			}
		}
	}, nil
}

func deferredObject(n streamjson.Node, w io.Writer) error {
	if n.Kind != '{' {
		return errors.New("资料须为对象")
	}
	if _, ok, e := n.Field("organization_mode"); e != nil {
		return e
	} else if ok {
		return n.Copy(w)
	}
	if _, e := io.WriteString(w, `{"organization_mode":"`+contract.DeferredOrganizationV1+`"`); e != nil {
		return e
	}
	c := n.Children()
	for {
		field, e := c.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		key, e := field.Key()
		if e != nil {
			return e
		}
		b, _ := json.Marshal(key)
		if _, e = w.Write(append(append([]byte(","), b...), ':')); e != nil {
			return e
		}
		if e = field.Copy(w); e != nil {
			return e
		}
	}
	_, e := io.WriteString(w, "}")
	return e
}
