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
		"useFeature 用于同步外部系统。",
		nil,
		[]Relation{{Type: "related_to", TargetID: "neighbor", Direction: "outgoing", Confidence: 0.8}},
		candidates,
	)
	if len(result) != 1 || result[0].Type != "part_of" || result[0].TargetID != "system" || result[0].Direction != "outgoing" {
		t.Fatalf("API relation should converge on its system root: %#v", result)
	}

	result = completeHighConfidenceRelations(
		domain.KindLesson,
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
