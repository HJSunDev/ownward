package mcpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type contractOnlyCapability struct {
	contract.ProductCapability
	rules string
}

func (value contractOnlyCapability) Rules(context.Context) string { return value.rules }

func TestServerDependsOnlyOnProductCapabilityAndUsesItsRules(t *testing.T) {
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := New(contractOnlyCapability{rules: "contract-owned-rules"}, "test")
	serverSession, err := server.MCP().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "contract-test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := clientSession.InitializeResult().Instructions; got != "contract-owned-rules" {
		t.Fatalf("server bypassed product rules: %q", got)
	}
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_rules", Arguments: map[string]any{}})
	if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "contract-owned-rules") {
		t.Fatalf("rules tool bypassed product capability: %#v %v", result, err)
	}
	if err := clientSession.Close(); err != nil {
		t.Fatal(err)
	}
	if err := serverSession.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestServerExposesUnifiedCoreOperations(t *testing.T) {
	ctx := context.Background()
	service := openCollaborativeForTest(t)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := New(service, "test")
	serverSession, err := server.MCP().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "acceptance"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result := clientSession.InitializeResult(); result == nil || result.Instructions != core.CollaborationRules {
		t.Fatalf("collaboration rules were not delivered by the server: %#v", result)
	}
	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 14 {
		t.Fatalf("unexpected tool count: %d", len(tools.Tools))
	}
	toolsByName := make(map[string]*mcp.Tool, len(tools.Tools))
	for _, tool := range tools.Tools {
		toolsByName[tool.Name] = tool
		if tool.Annotations == nil || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %s must declare its closed-world boundary: %#v", tool.Name, tool.Annotations)
		}
	}
	for _, name := range []string{"ownward_rules", "ownward_read", "ownward_evidence_search", "ownward_evidence_read", "ownward_status", "ownward_search", "ownward_navigate", "ownward_semantic_work"} {
		if tool := toolsByName[name]; tool == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
			t.Fatalf("tool %s must be declared read-only and idempotent: %#v", name, tool)
		}
	}
	semanticSchema, err := json.Marshal(toolsByName["ownward_semantic_submit"].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"ownward.semantic-submission/v1", "complete", "uncertain", "outgoing", "incoming", "以上类型均不准确时才用 related_to", "方向决定关系陈述的主语", "没有可靠的关系、场景或主题时在相应字段使用空数组", "后来观察到的结果支持此前结论", "不能因源内容在场景中被使用就改写成 part_of", "仅词语或宽泛主题相近不构成关系"} {
		if !strings.Contains(string(semanticSchema), required) {
			t.Fatalf("semantic tool schema does not explain %q: %s", required, semanticSchema)
		}
	}
	assertRelationEnums(t, semanticSchema)
	semanticBatchSchema, err := json.Marshal(toolsByName["ownward_semantic_submit_batch"].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	assertRelationEnums(t, semanticBatchSchema)
	if tool := toolsByName["ownward_create"]; tool == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
		t.Fatalf("create must be declared additive: %#v", tool)
	}
	if tool := toolsByName["ownward_create_batch"]; tool == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
		t.Fatalf("batch create must be declared additive: %#v", tool)
	}
	if tool := toolsByName["ownward_update"]; tool == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
		t.Fatalf("update must retain its write boundary: %#v", tool)
	}
	if tool := toolsByName["ownward_semantic_submit"]; tool == nil || tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
		t.Fatalf("semantic submission must be an idempotent write: %#v", tool)
	}
	if tool := toolsByName["ownward_semantic_submit_batch"]; tool == nil || tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
		t.Fatalf("semantic batch submission must be an idempotent write: %#v", tool)
	}
	created, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "ownward_create",
		Arguments: map[string]any{
			"content":  "先验证真实目标，再执行破坏性操作。",
			"contexts": []map[string]any{{"key": "platform", "value": "windows"}},
		},
	})
	if err != nil || created.IsError {
		t.Fatalf("create failed: result=%#v error=%v", created, err)
	}
	var createdOut CreateOutput
	if err := json.Unmarshal([]byte(created.Content[0].(*mcp.TextContent).Text), &createdOut); err != nil {
		t.Fatal(err)
	}
	id := createdOut.Result.Information.ID
	if _, err := service.Read(ctx, id); err != nil {
		t.Fatalf("create did not reach the authoritative store: %v", err)
	}
	read, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_read", Arguments: map[string]any{"id": id}})
	if err != nil || read.IsError {
		t.Fatalf("read failed: result=%#v error=%v", read, err)
	}
	updated, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "ownward_update", Arguments: map[string]any{"id": id, "expected_revision": 1, "content": "先验证并解析真实目标，再执行破坏性操作。"},
	})
	if err != nil || updated.IsError {
		t.Fatalf("update failed: result=%#v error=%v", updated, err)
	}
	current, err := service.Read(ctx, id)
	if err != nil || current.Revision != 2 {
		t.Fatalf("update did not reach the authoritative store: %#v %v", current, err)
	}
	searched, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "ownward_search",
		Arguments: map[string]any{"query": "破坏性操作", "limit": 10},
	})
	if err != nil || searched.IsError {
		t.Fatalf("search failed: result=%#v error=%v", searched, err)
	}
	navigated, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_navigate", Arguments: map[string]any{"start_ids": []string{id}}})
	if err != nil || navigated.IsError {
		t.Fatalf("navigate failed: result=%#v error=%v", navigated, err)
	}
	rules, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_rules", Arguments: map[string]any{}})
	if err != nil || rules.IsError {
		t.Fatalf("rules failed: result=%#v error=%v", rules, err)
	}
	// Model-facing optional fields must also be optional on the actual MCP
	// boundary, not merely in the host's output schema or native Go calls.
	work, err := service.SemanticWorkFor(ctx, []string{id})
	if err != nil || len(work) != 1 {
		t.Fatalf("semantic work unavailable: %v %#v", err, work)
	}
	submitted, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "ownward_semantic_submit",
		Arguments: map[string]any{"submission": map[string]any{
			"schema": semantics.SubmissionSchema, "work_id": work[0].ID,
			"asset_id": id, "asset_revision": current.Revision,
			"capability": map[string]any{"id": "test", "version": "v1"}, "status": "complete",
			"analysis": map[string]any{"summary": current.Content, "topics": []string{},
				"cues": []any{}, "relations": []any{}, "inferred_contexts": []any{},
				"organization": map[string]any{"schema": semantics.OrganizationSchema,
					"units": []any{map[string]any{"id": "u", "selector": map[string]any{"exact": current.Content},
						"mentions": []any{map[string]any{"id": "m", "name": "真实目标"}}}}, "links": []any{}}},
		}},
	})
	if err != nil || submitted.IsError {
		t.Fatalf("MCP rejected omitted optional organization fields: %#v %v", submitted, err)
	}
	if err := clientSession.Close(); err != nil {
		t.Fatal(err)
	}
	if err := serverSession.Wait(); err != nil {
		t.Fatal(err)
	}
}

func assertRelationEnums(t *testing.T, encoded []byte) {
	t.Helper()
	var schema any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	relation := findRelationSchema(schema)
	if relation == nil {
		t.Fatalf("semantic tool schema has no relation object: %s", encoded)
	}
	assertStringEnum(t, relation.properties, "type", []string{
		"same_as", "broader_than", "narrower_than", "part_of", "has_part", "supports", "contradicts", "derived_from", "applies_in", "related_to",
	})
	assertStringEnum(t, relation.properties, "direction", []string{"outgoing", "incoming"})
	assertRequiredProperties(t, relation.required, "type", "target_id", "confidence", "evidence", "direction")
}

type relationSchemaView struct {
	properties map[string]any
	required   map[string]struct{}
}

func assertRequiredProperties(t *testing.T, required map[string]struct{}, expected ...string) {
	t.Helper()
	for _, name := range expected {
		if _, exists := required[name]; !exists {
			t.Fatalf("relation property %q is not required: %#v", name, required)
		}
	}
}

func findRelationSchema(value any) *relationSchemaView {
	switch current := value.(type) {
	case map[string]any:
		if properties, ok := current["properties"].(map[string]any); ok {
			if _, hasTarget := properties["target_id"]; hasTarget {
				if _, hasDirection := properties["direction"]; hasDirection {
					required := make(map[string]struct{})
					if values, ok := current["required"].([]any); ok {
						for _, item := range values {
							if name, ok := item.(string); ok {
								required[name] = struct{}{}
							}
						}
					}
					return &relationSchemaView{properties: properties, required: required}
				}
			}
		}
		for _, child := range current {
			if found := findRelationSchema(child); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range current {
			if found := findRelationSchema(child); found != nil {
				return found
			}
		}
	}
	return nil
}

func assertStringEnum(t *testing.T, properties map[string]any, name string, expected []string) {
	t.Helper()
	property, ok := properties[name].(map[string]any)
	if !ok {
		t.Fatalf("relation property %q is missing: %#v", name, properties)
	}
	values, ok := property["enum"].([]any)
	if !ok {
		t.Fatalf("relation property %q has no enum: %#v", name, property)
	}
	actual := make([]string, len(values))
	for index, value := range values {
		actual[index], ok = value.(string)
		if !ok {
			t.Fatalf("relation property %q contains a non-string enum: %#v", name, values)
		}
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("relation property %q enum = %#v, want %#v", name, actual, expected)
	}
}

func TestStreamableHTTPUsesTheSameCore(t *testing.T) {
	ctx := context.Background()
	service := openCollaborativeForTest(t)
	httpServer := httptest.NewServer(New(service, "test").HTTPHandler())
	defer httpServer.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "streamable-http-test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	created, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "ownward_create", Arguments: map[string]any{"content": "同一常驻内核服务多个独立智能体会话。"},
	})
	if err != nil || created.IsError {
		t.Fatalf("streamable HTTP create failed: result=%#v error=%v", created, err)
	}
	var out CreateOutput
	if err := json.Unmarshal([]byte(created.Content[0].(*mcp.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if got, err := service.Read(ctx, out.Result.Information.ID); err != nil || got.Content != "同一常驻内核服务多个独立智能体会话。" {
		t.Fatalf("streamable HTTP did not reach the shared core: %#v %v", got, err)
	}
}

type preAuthenticatedProduct struct {
	product *informationcontrol.Product
	token   string
}

func (p preAuthenticatedProduct) auth(ctx context.Context) context.Context {
	return informationcontrol.Authenticate(ctx, p.token)
}
func (p preAuthenticatedProduct) Rules(ctx context.Context) string { return p.product.Rules(ctx) }
func (p preAuthenticatedProduct) Create(ctx context.Context, in contract.CreateInput) (contract.MutationResult, error) {
	return p.product.Create(p.auth(ctx), in)
}
func (p preAuthenticatedProduct) CreateBatch(ctx context.Context, in []contract.CreateInput) ([]contract.MutationBatchResult, error) {
	return p.product.CreateBatch(p.auth(ctx), in)
}
func (p preAuthenticatedProduct) Read(ctx context.Context, id string) (domain.Information, error) {
	return p.product.Read(p.auth(ctx), id)
}
func (p preAuthenticatedProduct) ReadInformation(ctx context.Context, id string, opt ...contract.ReadOptions) (contract.InformationRead, error) {
	return p.product.ReadInformation(p.auth(ctx), id, opt...)
}
func (p preAuthenticatedProduct) SearchEvidence(ctx context.Context, in contract.EvidenceSearchInput) ([]domain.EvidenceReference, error) {
	return p.product.SearchEvidence(p.auth(ctx), in)
}
func (p preAuthenticatedProduct) ReadEvidence(ctx context.Context, id string) (domain.Evidence, error) {
	return p.product.ReadEvidence(p.auth(ctx), id)
}
func (p preAuthenticatedProduct) ReadEvidenceWithBasis(ctx context.Context, id string) (contract.EvidenceRead, error) {
	return p.product.ReadEvidenceWithBasis(p.auth(ctx), id)
}
func (p preAuthenticatedProduct) CheckInformation(ctx context.Context, bases []string) ([]contract.InformationCheck, error) {
	return p.product.CheckInformation(p.auth(ctx), bases)
}
func (p preAuthenticatedProduct) Update(ctx context.Context, in contract.UpdateInput) (contract.MutationResult, error) {
	return p.product.Update(p.auth(ctx), in)
}
func (p preAuthenticatedProduct) Organization(id string) (contract.OrganizationState, error) {
	return p.product.Organization(id)
}
func (p preAuthenticatedProduct) Search(ctx context.Context, in contract.SearchInput) ([]contract.SearchResult, error) {
	return p.product.Search(p.auth(ctx), in)
}
func (p preAuthenticatedProduct) Navigate(ctx context.Context, start, rel []string, depth, limit int) (contract.NavigationResult, error) {
	return p.product.Navigate(p.auth(ctx), start, rel, depth, limit)
}
func (p preAuthenticatedProduct) SemanticWork(ctx context.Context, limit int) ([]semantics.Work, error) {
	return p.product.SemanticWork(p.auth(ctx), limit)
}
func (p preAuthenticatedProduct) SemanticWorkFor(ctx context.Context, ids []string) ([]semantics.Work, error) {
	return p.product.SemanticWorkFor(p.auth(ctx), ids)
}
func (p preAuthenticatedProduct) SubmitSemantic(ctx context.Context, sub semantics.Submission) (contract.OrganizationState, error) {
	return p.product.SubmitSemantic(p.auth(ctx), sub)
}
func (p preAuthenticatedProduct) SubmitSemanticBatch(ctx context.Context, subs []semantics.Submission) ([]contract.SemanticSubmissionResult, error) {
	return p.product.SubmitSemanticBatch(p.auth(ctx), subs)
}
func (p preAuthenticatedProduct) SemanticStatus() map[string]int {
	return p.product.SemanticStatus()
}

func openCollaborativeForTest(t *testing.T) contract.ProductCapability {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "data")
	scratch := filepath.Join(root, "scratch")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	budget, err := resourcebudget.New(20*resourcebudget.MiB, 4*resourcebudget.MiB)
	if err != nil {
		t.Fatal(err)
	}
	embedder := embedding.HashForTesting{Dimensions: 512}
	initState := contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "test"}
	store, err := boundedstore.OpenDeployment(ctx, root, boundedstore.DeploymentOptions{
		Options:     boundedstore.Options{Budget: budget},
		VectorSpace: embedder.Space().ID,
		Initialize: func(ctx context.Context, s *boundedstore.Store, _ string) error {
			if _, err := s.OpenControlAuthority(ctx, initState); err != nil {
				return err
			}
			return s.InitializeDeploymentSpace(ctx, embedder.Space().ID)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.OpenControlAuthority(ctx, initState)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	c := informationcontrol.New(a)
	token, err := c.InitializeOwner("owner")
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	kernel := &core.StreamingAssets{Store: store, Budget: budget, Scratch: scratch, DiskBytes: 256 * resourcebudget.MiB, Embedder: embedder}
	product := informationcontrol.NewProduct(kernel, c)
	t.Cleanup(func() {
		product.Close()
		_ = kernel.Close()
		_ = store.Close()
	})
	return preAuthenticatedProduct{product: product, token: token}
}
