package mcpserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/rpcstream"
	"github.com/HJSunDev/ownward/internal/streamjson"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// StorageServer 装配新格式已经实现的存取能力；旧用户格式切换由迁移交付控制。
type StorageServer struct {
	server    *mcp.Server
	bridge    *rpcstream.HTTPBridge
	dir       string
	budget    *resourcebudget.Budget
	diskBytes int64
}

func NewStreamingStorage(service contract.StreamingProduct, version, dir string, budget *resourcebudget.Budget, diskBytes int64) *StorageServer {
	server := mcp.NewServer(&mcp.Implementation{Name: "ownward", Version: version}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Experimental: map[string]any{"ownward.bounded-storage": map[string]any{"version": 1}}}})
	s := &StorageServer{server: server, dir: dir, budget: budget, diskBytes: diskBytes}
	registerStream[CreateInput, CreateOutput](server, service, "ownward_create", "创建属于用户且可长期复用的信息。体系负责组织结构，调用方不得为了保存信息而自行设计目录或关系图；保存成功后可继续其他工作；organization.required_action 由宿主在可用工作时机接续。", false, false)
	registerStream[CreateBatchInput, CreateBatchOutput](server, service, "ownward_create_batch", "一次创建一批彼此独立的信息，复用同一向量处理批次以降低批量沉淀成本。每条结果独立返回，失败项不得被静默忽略。", false, false)
	registerStream[ReadInput, ReadOutput](server, service, "ownward_read", "按稳定标识读取一项个人信息及其当前版本。", true, false)
	registerStream[UpdateInput, UpdateOutput](server, service, "ownward_update", "更新现有个人信息并保留稳定标识；必须提供最后读取到的版本，避免覆盖并发变化；保存成功后可继续其他工作；organization.required_action 由宿主在可用工作时机接续。", false, true)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	s.bridge = rpcstream.HTTP(h, dir, budget, diskBytes)
	server.AddReceivingMiddleware(s.bridge.Middleware)
	return s
}

func registerStream[T, O any](server *mcp.Server, service contract.StreamingProduct, name, description string, read, destructive bool) {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(err)
	}
	output, err := jsonschema.For[O](nil)
	if err != nil {
		panic(err)
	}
	server.AddTool(&mcp.Tool{Name: name, OutputSchema: output, Description: description, InputSchema: schema, Annotations: closedWorldAnnotations(read, destructive, read || destructive)}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		scope := rpcstream.FromContext(ctx)
		if scope == nil {
			return nil, fmt.Errorf("缺少有界协议接入")
		}
		call, err := scope.Resolve(request.Params.Name, request.Params.Arguments)
		if err != nil {
			return nil, err
		}
		if err = call.Arguments.Validate(ctx, schema); err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
		}
		if !read {
			op, _ := contract.Operation(ctx)
			if value, ok := request.Params.Meta["ownward/operation"].(string); ok {
				op.ID = value
				op.Generation, _ = strconv.ParseUint(fmt.Sprint(request.Params.Meta["ownward/generation"]), 10, 64)
			}
			if request.Extra != nil && request.Extra.Header.Get("X-Ownward-Operation") != "" {
				op.ID = request.Extra.Header.Get("X-Ownward-Operation")
				op.Generation, _ = strconv.ParseUint(request.Extra.Header.Get("X-Ownward-Generation"), 10, 64)
			}
			op.Kind = name
			op.Digest, err = call.Digest(ctx)
			if err != nil {
				return nil, err
			}
			ctx = contract.WithOperation(ctx, op)
		}
		result, err := service.ExecuteStream(ctx, contract.StreamRequest{Operation: name, Arguments: streamjson.RawSource{Node: call.Arguments}})
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
		}
		defer result.Close()
		return call.Result(ctx, func(w io.Writer) error {
			r, e := result.Value.Open(ctx)
			if e != nil {
				return e
			}
			defer r.Close()
			_, e = io.CopyBuffer(w, r, make([]byte, streamjson.BufferBytes))
			return e
		}, func() error { return result.Check(call.Context()) })
	})
}

func (s *StorageServer) MCP() *mcp.Server          { return s.server }
func (s *StorageServer) HTTPHandler() http.Handler { return s.bridge }
func (s *StorageServer) RunIO(ctx context.Context, input io.ReadCloser, output io.WriteCloser) error {
	scope := rpcstream.New(s.dir, s.budget, s.diskBytes)
	defer scope.Close()
	return s.server.Run(rpcstream.WithScope(ctx, scope), &rpcstream.IO{Scope: scope, Reader: input, Writer: output})
}
