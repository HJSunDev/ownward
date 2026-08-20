package mcpserver

import (
	"context"
	"net/http"

	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Server struct {
	service *core.Service
	server  *mcp.Server
}

type RulesInput struct{}

type RulesOutput struct {
	Rules string `json:"rules"`
}

type CreateInput struct {
	Content  string           `json:"content" jsonschema:"属于用户且可长期复用的完整信息"`
	Kind     string           `json:"kind,omitempty" jsonschema:"兼容既有资产的可选字段；通常省略，不参与自主语义组织"`
	Contexts []domain.Context `json:"contexts,omitempty" jsonschema:"仅在信息含义或适用性依赖场景时提供"`
	Source   domain.Source    `json:"source,omitempty" jsonschema:"信息来源"`
}

type CreateOutput struct {
	Result core.MutationResult `json:"result"`
}

type CreateBatchInput struct {
	Items []CreateInput `json:"items" jsonschema:"一到二十条彼此独立的完整信息"`
}

type CreateBatchOutput struct {
	Results []core.MutationBatchResult `json:"results"`
}

type ReadInput struct {
	ID string `json:"id" jsonschema:"稳定的信息标识"`
}

type ReadOutput struct {
	Information domain.Information `json:"information"`
}

type StatusInput struct {
	ID string `json:"id" jsonschema:"稳定的信息标识"`
}

type StatusOutput struct {
	Organization core.OrganizationState `json:"organization"`
}

type UpdateInput struct {
	ID               string            `json:"id" jsonschema:"稳定的信息标识"`
	ExpectedRevision uint64            `json:"expected_revision" jsonschema:"调用方最后读取到的版本，用于避免覆盖并发更新"`
	Content          *string           `json:"content,omitempty" jsonschema:"更新后的完整信息内容"`
	Kind             *string           `json:"kind,omitempty" jsonschema:"兼容既有资产的可选字段；通常省略，不参与自主语义组织"`
	Contexts         *[]domain.Context `json:"contexts,omitempty" jsonschema:"更新后的完整场景集合；空数组表示清除场景"`
	Source           *domain.Source    `json:"source,omitempty" jsonschema:"更新后的来源"`
}

type UpdateOutput struct {
	Result core.MutationResult `json:"result"`
}

type SearchInput struct {
	Query    string           `json:"query" jsonschema:"当前检索目标；复杂问题应随累计证据调整查询"`
	Contexts []domain.Context `json:"contexts,omitempty" jsonschema:"当前目的要求满足的场景约束"`
	Limit    int              `json:"limit,omitempty" jsonschema:"返回数量，一到一百；默认十"`
}

type SearchOutput struct {
	Results []core.SearchResult `json:"results"`
}

type NavigateInput struct {
	StartIDs      []string `json:"start_ids" jsonschema:"从检索结果获得的稳定信息标识"`
	RelationTypes []string `json:"relation_types,omitempty" jsonschema:"可选的关系类型约束"`
	Depth         int      `json:"depth,omitempty" jsonschema:"导航深度，一到五；默认一"`
	Limit         int      `json:"limit,omitempty" jsonschema:"最多返回的关系数量；默认五十"`
}

type NavigateOutput struct {
	Result core.NavigationResult `json:"result"`
}

type SemanticWorkInput struct {
	Limit    int      `json:"limit,omitempty" jsonschema:"本次最多取得的语义工作数量，一到二十；默认一"`
	AssetIDs []string `json:"asset_ids,omitempty" jsonschema:"需要定向取得的资产标识，一到二十条；提供后忽略 limit"`
}

type SemanticWorkOutput struct {
	Work []semantics.Work `json:"work"`
}

type SemanticSubmitInput struct {
	Submission semantics.Submission `json:"submission" jsonschema:"对语义工作形成的候选判断"`
}

type SemanticSubmitOutput struct {
	Organization core.OrganizationState `json:"organization"`
}

type SemanticSubmitBatchInput struct {
	Submissions []semantics.Submission `json:"submissions" jsonschema:"同一批有界语义工作对应的候选判断；一到二十条"`
}

type SemanticSubmitBatchOutput struct {
	Results []core.SemanticSubmissionResult `json:"results"`
}

func New(service *core.Service, version string) *Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "ownward", Version: version},
		&mcp.ServerOptions{Instructions: core.CollaborationRules, Capabilities: &mcp.ServerCapabilities{}},
	)
	value := &Server{service: service, server: server}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ownward_rules",
		Description: "读取 Ownward 的信息范围、使用、维护和主动补充规则。首次使用或不确定是否应保存、如何检索时调用。",
		Annotations: closedWorldAnnotations(true, false, true),
	}, value.rules)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ownward_create",
		Description: "创建属于用户且可长期复用的信息。体系负责组织结构，调用方不得为了保存信息而自行设计目录或关系图。",
		Annotations: closedWorldAnnotations(false, false, false),
	}, value.create)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ownward_create_batch",
		Description: "一次创建一批彼此独立的信息，复用同一向量处理批次以降低批量沉淀成本。每条结果独立返回，失败项不得被静默忽略。",
		Annotations: closedWorldAnnotations(false, false, false),
	}, value.createBatch)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ownward_read",
		Description: "按稳定标识读取一项个人信息及其当前版本。",
		Annotations: closedWorldAnnotations(true, false, true),
	}, value.read)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ownward_status",
		Description: "读取一项信息当前的组织状态，判断语义或向量处理是否仍待完成、已完成或明确不确定。",
		Annotations: closedWorldAnnotations(true, false, true),
	}, value.status)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ownward_update",
		Description: "更新现有个人信息并保留稳定标识；必须提供最后读取到的版本，避免覆盖并发变化。",
		Annotations: closedWorldAnnotations(false, true, false),
	}, value.update)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ownward_search",
		Description: "检索与当前目的相关的低成本线索。简单检索可以一次完成；复杂检索应依据累计证据继续调整查询或沿关系扩展，再按需读取完整内容。",
		Annotations: closedWorldAnnotations(true, false, true),
	}, value.search)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ownward_navigate",
		Description: "从已有信息标识沿语义关系继续获取线索。复杂检索用它探索层级、归属、交叉、组合和场景关联，再按需读取完整内容。",
		Annotations: closedWorldAnnotations(true, false, true),
	}, value.navigate)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ownward_semantic_work",
		Description: "以独立的语义能力角色取得待理解的有界工作。只分析工作中的资产和候选上下文，不使用当前任务意图，也不直接修改资产或关系图。",
		Annotations: closedWorldAnnotations(true, false, true),
	}, value.semanticWork)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ownward_semantic_submit",
		Description: "提交带能力来源、依据、置信度和不确定性的语义候选。Ownward 内核校验工作版本、证据与结构后决定如何进入派生组织状态；无法可靠判断时提交 uncertain，而不是猜测。",
		Annotations: closedWorldAnnotations(false, false, true),
	}, value.semanticSubmit)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "ownward_semantic_submit_batch",
		Description: "一次提交一批彼此独立的语义候选，减少批量沉淀和重建中的交互成本。每条结果独立校验并返回；失败项不会阻断有效项，也不得被静默忽略。",
		Annotations: closedWorldAnnotations(false, false, true),
	}, value.semanticSubmitBatch)
	return value
}

func closedWorldAnnotations(readOnly, destructive, idempotent bool) *mcp.ToolAnnotations {
	openWorld := false
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: &destructive,
		IdempotentHint:  idempotent,
		OpenWorldHint:   &openWorld,
	}
}

func (s *Server) MCP() *mcp.Server {
	return s.server
}

func (s *Server) Run(ctx context.Context, transport mcp.Transport) error {
	return s.server.Run(ctx, transport)
}

func (s *Server) HTTPHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s.server
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})
}

func (s *Server) rules(ctx context.Context, _ *mcp.CallToolRequest, _ RulesInput) (*mcp.CallToolResult, RulesOutput, error) {
	return nil, RulesOutput{Rules: s.service.Rules(ctx)}, nil
}

func (s *Server) create(ctx context.Context, _ *mcp.CallToolRequest, input CreateInput) (*mcp.CallToolResult, CreateOutput, error) {
	value, err := coreCreateInput(input)
	if err != nil {
		return nil, CreateOutput{}, err
	}
	result, err := s.service.Create(ctx, value)
	if err != nil {
		return nil, CreateOutput{}, err
	}
	return nil, CreateOutput{Result: result}, nil
}

func (s *Server) createBatch(ctx context.Context, _ *mcp.CallToolRequest, input CreateBatchInput) (*mcp.CallToolResult, CreateBatchOutput, error) {
	items := make([]core.CreateInput, len(input.Items))
	for index, item := range input.Items {
		value, err := coreCreateInput(item)
		if err != nil {
			return nil, CreateBatchOutput{}, err
		}
		items[index] = value
	}
	results, err := s.service.CreateBatch(ctx, items)
	if err != nil {
		return nil, CreateBatchOutput{}, err
	}
	return nil, CreateBatchOutput{Results: results}, nil
}

func coreCreateInput(input CreateInput) (core.CreateInput, error) {
	kind := domain.KindGeneral
	if input.Kind != "" {
		parsed, err := domain.ParseKind(input.Kind)
		if err != nil {
			return core.CreateInput{}, err
		}
		kind = parsed
	}
	return core.CreateInput{Kind: kind, Content: input.Content, Contexts: input.Contexts, Source: input.Source}, nil
}

func (s *Server) read(ctx context.Context, _ *mcp.CallToolRequest, input ReadInput) (*mcp.CallToolResult, ReadOutput, error) {
	value, err := s.service.Read(ctx, input.ID)
	if err != nil {
		return nil, ReadOutput{}, err
	}
	return nil, ReadOutput{Information: value}, nil
}

func (s *Server) status(ctx context.Context, _ *mcp.CallToolRequest, input StatusInput) (*mcp.CallToolResult, StatusOutput, error) {
	value, err := s.service.Organization(input.ID)
	if err != nil {
		return nil, StatusOutput{}, err
	}
	return nil, StatusOutput{Organization: value}, nil
}

func (s *Server) update(ctx context.Context, _ *mcp.CallToolRequest, input UpdateInput) (*mcp.CallToolResult, UpdateOutput, error) {
	update := core.UpdateInput{
		ID:               input.ID,
		ExpectedRevision: input.ExpectedRevision,
		Content:          input.Content,
		Contexts:         input.Contexts,
		Source:           input.Source,
	}
	if input.Kind != nil {
		kind, err := domain.ParseKind(*input.Kind)
		if err != nil {
			return nil, UpdateOutput{}, err
		}
		update.Kind = &kind
	}
	result, err := s.service.Update(ctx, update)
	if err != nil {
		return nil, UpdateOutput{}, err
	}
	return nil, UpdateOutput{Result: result}, nil
}

func (s *Server) search(ctx context.Context, _ *mcp.CallToolRequest, input SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
	values, err := s.service.Search(ctx, core.SearchInput{Query: input.Query, Contexts: input.Contexts, Limit: input.Limit})
	if err != nil {
		return nil, SearchOutput{}, err
	}
	return nil, SearchOutput{Results: values}, nil
}

func (s *Server) navigate(ctx context.Context, _ *mcp.CallToolRequest, input NavigateInput) (*mcp.CallToolResult, NavigateOutput, error) {
	result, err := s.service.Navigate(ctx, input.StartIDs, input.RelationTypes, input.Depth, input.Limit)
	if err != nil {
		return nil, NavigateOutput{}, err
	}
	return nil, NavigateOutput{Result: result}, nil
}

func (s *Server) semanticWork(ctx context.Context, _ *mcp.CallToolRequest, input SemanticWorkInput) (*mcp.CallToolResult, SemanticWorkOutput, error) {
	var work []semantics.Work
	var err error
	if len(input.AssetIDs) > 0 {
		work, err = s.service.SemanticWorkFor(ctx, input.AssetIDs)
	} else {
		work, err = s.service.SemanticWork(ctx, input.Limit)
	}
	if err != nil {
		return nil, SemanticWorkOutput{}, err
	}
	return nil, SemanticWorkOutput{Work: work}, nil
}

func (s *Server) semanticSubmit(ctx context.Context, _ *mcp.CallToolRequest, input SemanticSubmitInput) (*mcp.CallToolResult, SemanticSubmitOutput, error) {
	state, err := s.service.SubmitSemantic(ctx, input.Submission)
	if err != nil {
		return nil, SemanticSubmitOutput{}, err
	}
	return nil, SemanticSubmitOutput{Organization: state}, nil
}

func (s *Server) semanticSubmitBatch(ctx context.Context, _ *mcp.CallToolRequest, input SemanticSubmitBatchInput) (*mcp.CallToolResult, SemanticSubmitBatchOutput, error) {
	results, err := s.service.SubmitSemanticBatch(ctx, input.Submissions)
	if err != nil {
		return nil, SemanticSubmitBatchOutput{}, err
	}
	return nil, SemanticSubmitBatchOutput{Results: results}, nil
}
