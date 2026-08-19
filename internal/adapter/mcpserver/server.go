package mcpserver

import (
	"context"

	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/domain"
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

type ReadInput struct {
	ID string `json:"id" jsonschema:"稳定的信息标识"`
}

type ReadOutput struct {
	Information domain.Information `json:"information"`
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
		Name:        "ownward_read",
		Description: "按稳定标识读取一项个人信息及其当前版本。",
		Annotations: closedWorldAnnotations(true, false, true),
	}, value.read)
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

func (s *Server) rules(ctx context.Context, _ *mcp.CallToolRequest, _ RulesInput) (*mcp.CallToolResult, RulesOutput, error) {
	return nil, RulesOutput{Rules: s.service.Rules(ctx)}, nil
}

func (s *Server) create(ctx context.Context, _ *mcp.CallToolRequest, input CreateInput) (*mcp.CallToolResult, CreateOutput, error) {
	kind := domain.KindGeneral
	if input.Kind != "" {
		parsed, err := domain.ParseKind(input.Kind)
		if err != nil {
			return nil, CreateOutput{}, err
		}
		kind = parsed
	}
	result, err := s.service.Create(ctx, core.CreateInput{Kind: kind, Content: input.Content, Contexts: input.Contexts, Source: input.Source})
	if err != nil {
		return nil, CreateOutput{}, err
	}
	return nil, CreateOutput{Result: result}, nil
}

func (s *Server) read(ctx context.Context, _ *mcp.CallToolRequest, input ReadInput) (*mcp.CallToolResult, ReadOutput, error) {
	value, err := s.service.Read(ctx, input.ID)
	if err != nil {
		return nil, ReadOutput{}, err
	}
	return nil, ReadOutput{Information: value}, nil
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
