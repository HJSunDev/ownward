package semantics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

func TestOpenAIAnalyzesAndEmbedsWithoutAcceptingInventedTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/chat/completions":
			content, _ := json.Marshal(Analysis{
				Kind:    domain.KindLesson,
				Summary: "测试范围过大会浪费验证成本。",
				Cues:    []Cue{{Text: "最小测试", Kind: "method"}},
				Topics:  []string{"测试效率"},
				Relations: []Relation{
					{Type: "supports", TargetID: "candidate-1", Confidence: 0.98, Evidence: "同一验证原则", Direction: "outgoing"},
					{Type: "supports", TargetID: "invented", Confidence: 0.99, Evidence: "无来源", Direction: "outgoing"},
				},
			})
			_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(content)}}}})
		case "/v1/embeddings":
			_ = json.NewEncoder(writer).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float32{3, 4}}}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL + "/v1", ChatModel: "chat", EmbeddingModel: "embedding", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := provider.Analyze(context.Background(), domain.Information{
		Schema: domain.AssetSchema, ID: "current", Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(), Kind: domain.KindGeneral, Content: "开发时只运行最小相关测试。",
	}, []Candidate{{ID: "candidate-1", Kind: domain.KindMethod, Content: "完整测试最后运行。"}})
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Kind != domain.KindLesson || len(analysis.Relations) != 1 || analysis.Relations[0].TargetID != "candidate-1" {
		t.Fatalf("unexpected analysis: %#v", analysis)
	}
	vectors, err := provider.Embed(context.Background(), []string{"query"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 1 || len(vectors[0]) != 2 || vectors[0][0] != 0.6 || vectors[0][1] != 0.8 {
		t.Fatalf("unexpected vectors: %#v", vectors)
	}
}

func TestOpenAIUsesDeterministicFallbackAtTheLatencyBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(50 * time.Millisecond)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{}})
	}))
	defer server.Close()
	previous := semanticAnalysisTimeout
	semanticAnalysisTimeout = 5 * time.Millisecond
	defer func() { semanticAnalysisTimeout = previous }()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL + "/v1", ChatModel: "chat", EmbeddingModel: "embedding", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := provider.Analyze(context.Background(), domain.Information{
		ID: "current", Kind: domain.KindGeneral, Content: "某次构建失败的根因是缺少匹配的编译环境。",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Kind != domain.KindLesson {
		t.Fatalf("timeout fallback lost the core semantic kind: %#v", analysis)
	}
}

func TestFallbackKindCoversCoreInformationSemantics(t *testing.T) {
	tests := []struct {
		content  string
		contexts []domain.Context
		want     domain.InformationKind
	}{
		{"林涛在 2022 年搬到深圳。", []domain.Context{{Key: "person", Value: "林涛"}}, domain.KindExperience},
		{"任务没有边界时林涛容易焦虑。", []domain.Context{{Key: "person", Value: "林涛"}}, domain.KindThought},
		{"与父亲沟通时先说明事实。", []domain.Context{{Key: "relationship", Value: "father"}}, domain.KindSocial},
		{"林涛擅长删掉无效抽象。", []domain.Context{{Key: "person", Value: "林涛"}}, domain.KindSkill},
		{"工作日上午不安排会议。", nil, domain.KindWork},
		{"先固定查询，再检查候选召回。", nil, domain.KindMethod},
		{"一次无结果不代表信息不存在。", nil, domain.KindLesson},
		{"排查质量下降时逐项定位。", nil, domain.KindPath},
		{"Fiber 是协调器的工作单元结构。", nil, domain.KindKnowledge},
	}
	for _, test := range tests {
		if got := fallbackKind(test.content, test.contexts); got != test.want {
			t.Errorf("fallbackKind(%q)=%s, want %s", test.content, got, test.want)
		}
	}
}

func TestNormalizeKindByRelationsDistinguishesDirectSolutionFromReusableSequence(t *testing.T) {
	relations := []Relation{{Type: "supports", TargetID: "risk", Direction: "outgoing"}}
	kinds := map[string]domain.InformationKind{"risk": domain.KindLesson}
	if got := normalizeKindByRelations(domain.KindMethod, "使用安全接口消除已知路径解析风险。", nil, relations, kinds); got != domain.KindSolution {
		t.Fatalf("direct corrective measure should be a solution, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindMethod, "先读取低成本线索，再按需读取完整内容。", nil, relations, kinds); got != domain.KindMethod {
		t.Fatalf("reusable sequence should remain a method, got %s", got)
	}
}

func TestSemanticNormalizationUsesDirectionAndOperationalContext(t *testing.T) {
	relations := []Relation{{Type: "supports", TargetID: "path", Direction: "incoming"}}
	kinds := map[string]domain.InformationKind{"path": domain.KindPath}
	normalized := normalizeRelationSemantics(domain.KindLesson, "一次结果不代表不存在。", relations, kinds, map[string]string{"path": "逐步排查检索问题。"})
	if normalized[0].Type != "related_to" {
		t.Fatalf("diagnostic path should relate to the lesson it diagnoses: %#v", normalized)
	}
	contexts := []domain.Context{{Key: "platform", Value: "linux"}}
	if got := normalizeKindByRelations(domain.KindKnowledge, "使用服务管理器管理进程、重启和日志。", contexts, nil, nil); got != domain.KindMethod {
		t.Fatalf("platform operation should be a method, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindMethod, "排查质量下降时，先固定条件，再逐项定位根因。", nil, nil, nil); got != domain.KindPath {
		t.Fatalf("diagnostic sequence should be a path, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindMethod, "执行前校验目标，防止变量展开扩大影响范围。", nil, nil, nil); got != domain.KindLesson {
		t.Fatalf("preventive risk invariant should be a lesson, got %s", got)
	}
	technical := []Relation{{Type: "supports", TargetID: "mechanism", Direction: "incoming"}}
	normalized = normalizeRelationSemantics(
		domain.KindKnowledge,
		"并发模式允许工作中断。",
		technical,
		map[string]domain.InformationKind{"mechanism": domain.KindKnowledge},
		map[string]string{"mechanism": "工作单元机制。"},
	)
	if normalized[0].Type != "related_to" || normalized[0].Direction != "outgoing" {
		t.Fatalf("technical dependency should be a direct relation from the current fact: %#v", normalized)
	}
	technical[0] = Relation{Type: "related_to", TargetID: "mechanism", Direction: "incoming"}
	normalized = normalizeRelationSemantics(
		domain.KindKnowledge,
		"并发模式允许工作中断。",
		technical,
		map[string]domain.InformationKind{"mechanism": domain.KindKnowledge},
		map[string]string{"mechanism": "工作单元机制。"},
	)
	if normalized[0].Direction != "outgoing" {
		t.Fatalf("technical relation direction should converge on the current fact: %#v", normalized)
	}
	method := []Relation{{Type: "applies_in", TargetID: "risk", Direction: "outgoing"}}
	normalized = normalizeRelationSemantics(
		domain.KindMethod,
		"开始任务前先写清楚完成边界。",
		method,
		map[string]domain.InformationKind{"risk": domain.KindThought},
		map[string]string{"risk": "任务边界不清会引发焦虑。"},
	)
	if normalized[0].Type != "supports" || normalized[0].Direction != "outgoing" {
		t.Fatalf("a method addressing a personal risk should support it: %#v", normalized)
	}
	method[0] = Relation{Type: "derived_from", TargetID: "risk", Direction: "outgoing"}
	normalized = normalizeRelationSemantics(
		domain.KindMethod,
		"开始任务前先写清楚完成边界。",
		method,
		map[string]domain.InformationKind{"risk": domain.KindThought},
		map[string]string{"risk": "任务边界不清会引发焦虑。"},
	)
	if normalized[0].Type != "supports" {
		t.Fatalf("a method derived from a personal risk should support it: %#v", normalized)
	}
	method[0] = Relation{Type: "derived_from", TargetID: "failure", Direction: "outgoing"}
	normalized = normalizeRelationSemantics(
		domain.KindMethod,
		"选择无原生依赖的实现。",
		method,
		map[string]domain.InformationKind{"failure": domain.KindLesson},
		map[string]string{"failure": "原生依赖导致构建失败。"},
	)
	if normalized[0].Type != "supports" || normalizeKindByRelations(domain.KindMethod, "选择无原生依赖的实现。", nil, normalized, map[string]domain.InformationKind{"failure": domain.KindLesson}) != domain.KindSolution {
		t.Fatalf("a direct method addressing a failure should become a supporting solution: %#v", normalized)
	}
}

func TestCompleteHighConfidenceRelationsUsesCandidateGraphAndSimilarity(t *testing.T) {
	candidates := []Candidate{
		{ID: "system", Kind: domain.KindKnowledge, Content: "框架核心。", Similarity: 0.6},
		{ID: "mechanism", Kind: domain.KindKnowledge, Content: "框架工作单元。", Similarity: 0.7, Relations: []Relation{{Type: "part_of", TargetID: "system"}}},
		{ID: "neighbor", Kind: domain.KindKnowledge, Content: "相邻阶段。", Similarity: 0.8},
	}
	result := completeHighConfidenceRelations(
		domain.KindKnowledge,
		"current",
		"useFeature 用于同步外部系统。",
		nil,
		[]Relation{{Type: "related_to", TargetID: "neighbor", Direction: "outgoing", Confidence: 0.8}},
		candidates,
	)
	if len(result) != 1 || result[0].Type != "part_of" || result[0].TargetID != "system" || result[0].Direction != "outgoing" {
		t.Fatalf("API relation should converge on its system root: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindKnowledge,
		"current",
		"Fiber 是 React 协调器的工作单元结构。",
		nil,
		nil,
		[]Candidate{{ID: "react", Kind: domain.KindKnowledge, Content: "React 协调渲染。", Similarity: 0.6}},
	)
	if len(result) != 1 || result[0].Type != "part_of" || result[0].TargetID != "react" {
		t.Fatalf("a named constituent should connect to its existing system: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindKnowledge,
		"current",
		"并发渲染允许 React 中断低优先级工作。",
		nil,
		[]Relation{{Type: "part_of", TargetID: "react", Direction: "outgoing", Confidence: 0.8}},
		[]Candidate{
			{ID: "react", Kind: domain.KindKnowledge, Similarity: 0.54},
			{ID: "fiber", Kind: domain.KindKnowledge, Similarity: 0.61, Relations: []Relation{{Type: "part_of", TargetID: "react"}}},
		},
	)
	if len(result) != 1 || result[0].Type != "related_to" || result[0].TargetID != "fiber" {
		t.Fatalf("a direct sibling mechanism should replace a generic root relation: %#v", result)
	}
	result = completeHighConfidenceRelations(
		domain.KindKnowledge,
		"current",
		"并发渲染允许 React 中断低优先级工作。",
		nil,
		nil,
		[]Candidate{
			{ID: "fiber", Kind: domain.KindKnowledge, Similarity: 0.61, Relations: []Relation{{Type: "part_of", TargetID: "react"}}},
			{ID: "react", Kind: domain.KindKnowledge, Content: "React 协调渲染。", Similarity: 0.54},
		},
	)
	if len(result) != 1 || result[0].Type != "related_to" || result[0].TargetID != "fiber" {
		t.Fatalf("an omitted sibling relation should be recovered from the existing graph: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindLesson,
		"current",
		"一次结果不能证明信息不存在。",
		nil,
		nil,
		[]Candidate{{ID: "diagnosis", Kind: domain.KindPath, Similarity: 0.7}},
	)
	if len(result) != 1 || result[0].Type != "related_to" || result[0].TargetID != "diagnosis" || result[0].Direction != "incoming" {
		t.Fatalf("new lesson should complete the earlier diagnostic relation: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindKnowledge,
		"current",
		"长期资产与派生状态相互独立。",
		nil,
		nil,
		[]Candidate{
			{ID: "schedule", Kind: domain.KindWork, Content: "上午不安排会议。", Similarity: 0.8},
			{ID: "boundary", Kind: domain.KindWork, Content: "产品只服务外部调用方，不承担界面职责。", Similarity: 0.5},
		},
	)
	if len(result) != 1 || result[0].TargetID != "boundary" || result[0].Direction != "incoming" {
		t.Fatalf("asset invariant should relate only to the product boundary candidate: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"优先使用无原生依赖的存储实现。",
		[]domain.Context{{Key: "runtime", Value: "go"}},
		nil,
		[]Candidate{{
			ID: "failure", Kind: domain.KindLesson, Similarity: 0.6,
			Contexts: []domain.Context{{Key: "runtime", Value: "go"}},
		}},
	)
	if len(result) != 1 || result[0].Type != "supports" || result[0].TargetID != "failure" {
		t.Fatalf("a same-context method should support the related failure: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"定向验证通过后，才执行完整验证。",
		nil,
		nil,
		[]Candidate{{ID: "focused", Kind: domain.KindMethod, Similarity: 0.58}},
	)
	if len(result) != 1 || result[0].Type != "supports" || result[0].TargetID != "focused" {
		t.Fatalf("a gated follow-up should connect to its preceding method: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"复杂检索先获取低成本线索，再按需读取完整内容。",
		nil,
		nil,
		[]Candidate{{ID: "lesson", Kind: domain.KindLesson, Content: "复杂问题应根据检索证据改变线索并继续搜索。", Similarity: 0.66}},
	)
	if len(result) != 1 || result[0].Type != "supports" || result[0].TargetID != "lesson" {
		t.Fatalf("a method should support its directly anchored lesson: %#v", result)
	}
	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"写正文时只表达事物本身，不再额外解释。",
		nil,
		nil,
		[]Candidate{{ID: "lesson", Kind: domain.KindLesson, Content: "把修改回应写进正文会产生多余解释。", Similarity: 0.596}},
	)
	if len(result) != 1 || result[0].Type != "supports" || result[0].TargetID != "lesson" {
		t.Fatalf("a lexically anchored corrective method should support its lesson: %#v", result)
	}
	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"开始长期任务前先写清楚结束条件。",
		nil,
		nil,
		[]Candidate{{ID: "thought", Kind: domain.KindThought, Content: "任务没有清晰完成边界时容易焦虑。", Similarity: 0.57}},
	)
	if len(result) != 1 || result[0].Type != "supports" || result[0].TargetID != "thought" {
		t.Fatalf("a method should support the directly anchored personal state it improves: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"Windows 使用任务计划程序管理登录后启动。",
		[]domain.Context{{Key: "platform", Value: "windows"}},
		nil,
		[]Candidate{{
			ID: "linux", Kind: domain.KindMethod, Content: "Linux 使用 systemd 管理进程和重启。", Similarity: 0.65,
			Contexts: []domain.Context{{Key: "platform", Value: "linux"}},
		}},
	)
	if len(result) != 1 || result[0].Type != "related_to" || result[0].TargetID != "linux" {
		t.Fatalf("equivalent operational methods on different platforms should be related: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"按关联线索继续检索。",
		nil,
		[]Relation{
			{Type: "supports", TargetID: "lesson", Direction: "outgoing", Confidence: 0.9},
			{Type: "applies_in", TargetID: "path", Direction: "outgoing", Confidence: 0.8},
		},
		[]Candidate{
			{ID: "lesson", Kind: domain.KindLesson},
			{ID: "path", Kind: domain.KindPath, Relations: []Relation{{Type: "related_to", TargetID: "lesson"}}},
		},
	)
	if len(result) != 1 || result[0].TargetID != "lesson" {
		t.Fatalf("a direct lesson relation should replace its diagnostic path indirection: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindLesson,
		"current",
		"一次结果不代表不存在。",
		nil,
		[]Relation{{Type: "related_to", TargetID: "method", Direction: "outgoing", Confidence: 0.8}},
		[]Candidate{{ID: "method", Kind: domain.KindMethod, Relations: []Relation{{Type: "supports", TargetID: "current"}}}},
	)
	if len(result) != 0 {
		t.Fatalf("a weaker reciprocal relation should not duplicate an existing directed relation: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"公开表达前先写核心判断会更从容。",
		[]domain.Context{{Key: "person", Value: "林涛"}, {Key: "activity", Value: "public-speaking"}},
		[]Relation{{Type: "supports", TargetID: "thought", Direction: "outgoing", Confidence: 0.8}},
		[]Candidate{{ID: "thought", Kind: domain.KindThought, Content: "任务边界不清会让林涛焦虑。", Contexts: []domain.Context{{Key: "person", Value: "林涛"}}}},
	)
	if len(result) != 0 {
		t.Fatalf("sharing only a person should not create a method-to-thought relation: %#v", result)
	}
	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"开始长期任务前先写清楚完成边界。",
		nil,
		[]Relation{{Type: "supports", TargetID: "thought", Direction: "outgoing", Confidence: 0.8}},
		[]Candidate{{ID: "thought", Kind: domain.KindThought, Content: "任务边界不清会引发焦虑。"}},
	)
	if len(result) != 1 {
		t.Fatalf("a method with a direct lexical anchor should retain the thought relation: %#v", result)
	}
	result = completeHighConfidenceRelations(
		domain.KindSkill,
		"current",
		"擅长删掉没有需求依据的抽象。",
		[]domain.Context{{Key: "person", Value: "林涛"}},
		[]Relation{{Type: "related_to", TargetID: "work", Direction: "outgoing", Confidence: 0.8}},
		[]Candidate{{ID: "work", Kind: domain.KindWork, Content: "产品不提供图形界面。"}},
	)
	if len(result) != 0 {
		t.Fatalf("a personal skill should not attach to unrelated project work by topic alone: %#v", result)
	}
	result = completeHighConfidenceRelations(
		domain.KindWork,
		"current",
		"林涛不在工作日上午安排会议，以保护连续思考时间。",
		[]domain.Context{{Key: "person", Value: "林涛"}},
		nil,
		[]Candidate{{ID: "thought", Kind: domain.KindThought, Content: "林涛上午适合连续思考。", Contexts: []domain.Context{{Key: "person", Value: "林涛"}}, Similarity: 0.67}},
	)
	if len(result) != 1 || result[0].Type != "derived_from" || result[0].TargetID != "thought" {
		t.Fatalf("a work arrangement should derive from the directly anchored personal state: %#v", result)
	}
	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"提交按最大终态子集拆分。",
		nil,
		[]Relation{{Type: "supports", TargetID: "tests", Direction: "outgoing", Confidence: 0.8}},
		[]Candidate{{ID: "tests", Kind: domain.KindMethod, Content: "开发时运行最小相关测试。"}},
	)
	if len(result) != 0 {
		t.Fatalf("independent methods should not be connected as a sequence: %#v", result)
	}
	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"按关联线索继续检索。",
		nil,
		[]Relation{{Type: "supports", TargetID: "path", Direction: "incoming", Confidence: 0.8}},
		[]Candidate{{ID: "path", Kind: domain.KindPath, Content: "排查检索质量下降。"}},
	)
	if len(result) != 0 {
		t.Fatalf("a diagnostic path should not be made to support a later method: %#v", result)
	}
	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"按低成本线索继续检索。",
		nil,
		[]Relation{{Type: "supports", TargetID: "path", Direction: "outgoing", Confidence: 0.8}},
		[]Candidate{{ID: "path", Kind: domain.KindPath, Content: "排查检索质量下降。"}},
	)
	if len(result) != 0 {
		t.Fatalf("a later retrieval method should not support its diagnostic path: %#v", result)
	}
	result = completeHighConfidenceRelations(
		domain.KindMethod,
		"current",
		"根据检索教训继续搜索。",
		nil,
		[]Relation{{Type: "supports", TargetID: "lesson", Direction: "incoming", Confidence: 0.8}},
		[]Candidate{{ID: "lesson", Kind: domain.KindLesson, Content: "一次无结果不代表不存在。"}},
	)
	if len(result) != 0 {
		t.Fatalf("a lesson should not be made to support a later method: %#v", result)
	}
}

func TestPersonalAffectiveStateRemainsThoughtWithoutConvertingConditionalMethods(t *testing.T) {
	contexts := []domain.Context{{Key: "person", Value: "林涛"}}
	if got := normalizeKindByRelations(domain.KindLesson, "任务没有边界时，林涛容易焦虑。", contexts, nil, nil); got != domain.KindThought {
		t.Fatalf("personal affective state should be thought, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindMethod, "公开表达前如果先写核心判断，林涛会更从容。", contexts, nil, nil); got != domain.KindMethod {
		t.Fatalf("conditional preparation should remain method, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindThought, "公开表达前如果先写核心判断，林涛会更从容。", contexts, nil, nil); got != domain.KindMethod {
		t.Fatalf("conditional preparation should normalize to method, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindKnowledge, "构建失败的根因是缺少匹配的编译环境。", nil, nil, nil); got != domain.KindLesson {
		t.Fatalf("a diagnosed failure cause should normalize to lesson, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindExperience, "某次构建失败的根因是缺少匹配的编译环境。", nil, nil, nil); got != domain.KindLesson {
		t.Fatalf("a diagnosed failure cause should outrank event phrasing, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindLesson, "useEffect 用于外部系统同步，不应当作数据推导工具。", nil, nil, nil); got != domain.KindKnowledge {
		t.Fatalf("API usage semantics should normalize to knowledge, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindMethod, "并发渲染允许 React 中断低优先级工作。", nil, nil, nil); got != domain.KindKnowledge {
		t.Fatalf("a named technical mechanism should normalize to knowledge, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindMethod, "Fiber 是 React 协调器的工作单元结构。", nil, nil, nil); got != domain.KindKnowledge {
		t.Fatalf("a named constituent structure should normalize to knowledge, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindLesson, "Effect 依赖项描述其读取的响应式值。", nil, nil, nil); got != domain.KindKnowledge {
		t.Fatalf("a technical dependency description should normalize to knowledge, got %s", got)
	}
	if got := normalizeKindByRelations(domain.KindKnowledge, "Ownward 只服务外部智能体，不提供图形界面。", nil, nil, nil); got != domain.KindWork {
		t.Fatalf("a product responsibility boundary should normalize to work, got %s", got)
	}
}

func TestNewOpenAIRejectsNonHTTPModelAddress(t *testing.T) {
	if _, err := NewOpenAI(OpenAIConfig{BaseURL: "file:///tmp/model", ChatModel: "chat", EmbeddingModel: "embedding"}); err == nil {
		t.Fatal("expected invalid model address error")
	}
}

func TestOpenAIRetriesTransientResponses(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			writer.Header().Set("Retry-After", "0")
			http.Error(writer, "temporary", http.StatusTooManyRequests)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float32{1, 0}}}})
	}))
	defer server.Close()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, ChatModel: "chat", EmbeddingModel: "embedding", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Embed(context.Background(), []string{"query"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 calls, got %d", calls.Load())
	}
}

func TestOpenAIDoesNotRetryPermanentResponses(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(writer, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, ChatModel: "chat", EmbeddingModel: "embedding", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Embed(context.Background(), []string{"query"}); err == nil {
		t.Fatal("expected model error")
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", calls.Load())
	}
}
