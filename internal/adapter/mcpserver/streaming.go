package mcpserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
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
	resourcebudget.LimitRuntime(20 * resourcebudget.MiB)
	server := mcp.NewServer(&mcp.Implementation{Name: "ownward", Version: version}, &mcp.ServerOptions{PageSize: 1, Instructions: core.CollaborationRules, Capabilities: &mcp.ServerCapabilities{Experimental: map[string]any{"ownward.bounded-storage": map[string]any{"version": 1}, "ownward.deferred-organization": map[string]any{"version": 1, "mode": contract.DeferredOrganizationV1}}}})
	s := &StorageServer{server: server, dir: dir, budget: budget, diskBytes: diskBytes}
	registerStream[RulesInput, RulesOutput](server, service, "ownward_rules", "取得信息存取与使用的协作规则。", true, false)
	registerStream[StatusInput, StatusOutput](server, service, "ownward_status", "查询一项资料的组织状态。", true, false)
	registerStream[CheckInput, CheckOutput](server, service, "ownward_check", "重新使用旧材料前批量核对实际取得的 basis；来源变化后重新取证。", true, false)
	registerStream[CreateInput, CreateOutput](server, service, "ownward_create", "创建属于用户且可长期复用的信息。体系负责组织结构，调用方不得为了保存信息而自行设计目录或关系图；保存成功后可继续其他工作；organization.required_action 由宿主在可用工作时机接续。", false, false)
	registerStream[CreateBatchInput, CreateBatchOutput](server, service, "ownward_create_batch", "一次创建一批彼此独立的信息，复用同一向量处理批次以降低批量沉淀成本。每条结果独立返回，失败项不得被静默忽略。", false, false)
	registerStream[ReadInput, ReadOutput](server, service, "ownward_read", "按稳定标识读取当前信息；original 提示留存原件，include_original 可按需取得原件正文与来源。basis 仅代表当前信息，原件是历史证据。", true, false)
	registerStream[SearchInput, SearchOutput](server, service, "ownward_search", "检索与当前目的相关的低成本线索。简单检索可以一次完成；复杂检索应依据累计证据继续调整查询或沿关系扩展，再按需读取完整内容。", true, false)
	registerStream[NavigateInput, NavigateOutput](server, service, "ownward_navigate", "从已有信息沿有据关系取得原文入口与条件；未穷尽时将 continuation 作为唯一导航起点接续。来源变化时从资产重新开始。", true, false)
	registerStream[EvidenceSearchInput, EvidenceSearchOutput](server, service, "ownward_evidence_search", "在已经命中的一项长信息内按当前问题即时定位可追溯原文区间；不创建子资产或持久化分段。返回引用须用 ownward_evidence_read 读取。", true, false)
	registerStream[EvidenceReadInput, EvidenceReadOutput](server, service, "ownward_evidence_read", "按证据引用读取当前原文区间，来源、版本、区间及内容均经校验。original 提示留存原件；核对原件时用 source_id 调 ownward_read 并设 include_original=true。", true, false)
	registerStream[contract.OrganizationJobRequest, contract.OrganizationJobResult](server, service, "ownward_semantic_jobs", "宿主接续延后组织：claim领取唯一待办，renew续期，release释放，wait有界等待可领取工作。凭据不授予额外权限；未领取不能执行。", false, false)
	registerStream[SemanticWorkInput, SemanticWorkOutput](server, service, "ownward_semantic_work", "以独立的语义能力角色取得待理解的有界工作。只分析工作中的资产和各自候选上下文，不使用当前任务意图，也不直接修改资产或关系图；关系涉及本项资产；引用同次调用额外提供的资料时，用 input_assets 声明完整输入的身份与版本，按提交契约判断。", true, false)
	registerStream[SemanticSubmitInput, SemanticSubmitOutput](server, service, "ownward_semantic_submit", "提交带能力来源、依据、置信度和不确定性的语义候选。Ownward 内核校验工作版本、证据与结构后决定如何进入派生组织状态；能可靠概括资产但没有可靠关系时提交 complete 和空关系，只有无法可靠理解资产基本含义时才提交 uncertain。", false, false)
	registerStream[SemanticSubmitBatchInput, SemanticSubmitBatchOutput](server, service, "ownward_semantic_submit_batch", "一次提交一批彼此独立的语义候选，减少批量沉淀和重建中的交互成本。每条结果独立校验并返回；失败项不会阻断有效项，调用方必须仅纠正并重试失败项，不得静默忽略。没有可靠关系不代表资产含义不确定，应提交 complete 和空关系。", false, false)
	registerStream[UpdateInput, UpdateOutput](server, service, "ownward_update", "更新现有个人信息并保留稳定标识；必须提供最后读取到的版本，避免覆盖并发变化；保存成功后可继续其他工作；organization.required_action 由宿主在可用工作时机接续。", false, true)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	s.bridge = rpcstream.HTTP(h, dir, budget, diskBytes)
	server.AddReceivingMiddleware(s.bridge.Middleware)
	return s
}

func registerStream[T, O any](server *mcp.Server, service contract.StreamingProduct, name, description string, read, destructive bool) {
	schema, err := jsonschema.For[T](nil)
	if name == "ownward_semantic_submit" || name == "ownward_semantic_submit_batch" {
		schema = semanticInputSchema[T]()
	}
	if err != nil {
		panic(err)
	}
	output, err := jsonschema.For[O](nil)
	if err != nil {
		panic(err)
	}
	server.AddTool(&mcp.Tool{Name: name, OutputSchema: output, Description: description, InputSchema: schema, Annotations: closedWorldAnnotations(read, destructive, read || name == "ownward_semantic_submit" || name == "ownward_semantic_submit_batch")}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
		}, func() error { return result.Check(call.Context()) }, result.Retain)
	})
}

func (s *StorageServer) MCP() *mcp.Server          { return s.server }
func (s *StorageServer) HTTPHandler() http.Handler { return s.bridge }
func (s *StorageServer) RunIO(ctx context.Context, input io.ReadCloser, output io.WriteCloser) error {
	scope := rpcstream.New(s.dir, s.budget, s.diskBytes)
	defer scope.Close()
	return s.server.Run(rpcstream.WithScope(ctx, scope), &rpcstream.IO{Scope: scope, Reader: input, Writer: output})
}
