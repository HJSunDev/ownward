package semantics

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

func TestOpenAIRequiresGroundedSemanticOutput(t *testing.T) {
	var inputHasKind atomic.Bool
	var candidateUsesExplicitContexts atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/chat/completions":
			var body struct {
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			var semanticInput struct {
				Information map[string]any   `json:"information"`
				Candidates  []map[string]any `json:"existing_candidates"`
			}
			if len(body.Messages) > 1 {
				_ = json.Unmarshal([]byte(body.Messages[1].Content), &semanticInput)
			}
			_, hasKind := semanticInput.Information["kind"]
			inputHasKind.Store(hasKind)
			if len(semanticInput.Candidates) == 1 {
				_, explicit := semanticInput.Candidates[0]["explicit_contexts"]
				_, ambiguous := semanticInput.Candidates[0]["contexts"]
				candidateUsesExplicitContexts.Store(explicit && !ambiguous)
			}
			content, _ := json.Marshal(Analysis{
				Summary: "测试范围过大会浪费验证成本。",
				Cues:    []Cue{{Text: "最小测试", Kind: "method"}},
				Topics:  []string{"测试效率"},
				Contexts: []InferredContext{
					{Key: "project", Value: "Ownward", Confidence: 0.95, Evidence: "内容明确提及"},
					{Key: "mood", Value: "calm", Confidence: 0.5, Evidence: "猜测"},
				},
				Relations: []Relation{
					{Type: "supports", TargetID: "candidate-1", Confidence: 0.98, Evidence: "同一验证原则", Direction: "outgoing"},
					{Type: "supports", TargetID: "invented", Confidence: 0.99, Evidence: "无来源", Direction: "outgoing"},
					{Type: "related_to", TargetID: "candidate-1", Confidence: 0.6, Evidence: "不确定", Direction: "outgoing"},
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
	}, []Candidate{{ID: "candidate-1", Content: "完整测试最后运行。", Contexts: []domain.Context{{Key: "project", Value: "Ownward"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Contexts) != 1 || len(analysis.Relations) != 1 || analysis.Relations[0].TargetID != "candidate-1" {
		t.Fatalf("unexpected analysis: %#v", analysis)
	}
	if inputHasKind.Load() {
		t.Fatal("legacy information kind must not influence open-world semantic analysis")
	}
	if !candidateUsesExplicitContexts.Load() {
		t.Fatal("candidate contexts must retain explicit semantic provenance")
	}
	vectors, err := provider.Embed(context.Background(), []string{"query"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 1 || len(vectors[0]) != 2 || vectors[0][0] != 0.6 || vectors[0][1] != 0.8 {
		t.Fatalf("unexpected vectors: %#v", vectors)
	}
}

func TestOpenAIDoesNotReplaceUnavailableSemanticsWithContentRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(writer).Encode(map[string]any{"choices": []any{}})
	}))
	defer server.Close()
	previous := semanticAnalysisTimeout
	semanticAnalysisTimeout = 5 * time.Millisecond
	defer func() { semanticAnalysisTimeout = previous }()
	provider, err := NewOpenAI(OpenAIConfig{BaseURL: server.URL, ChatModel: "chat", EmbeddingModel: "embedding", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Analyze(context.Background(), domain.Information{ID: "current", Content: "任意领域的未知表达。"}, nil); err == nil {
		t.Fatal("unavailable semantics must remain pending instead of being guessed")
	}
}

func TestAnalysisSchemaHasNoMandatoryInformationClassification(t *testing.T) {
	properties := analysisSchema()["properties"].(map[string]any)
	if _, exists := properties["kind"]; exists {
		t.Fatal("semantic analysis must not force information into a closed classification")
	}
}

func TestNormalizeAnalysisRejectsNonFiniteContextConfidence(t *testing.T) {
	analysis := normalizeAnalysis(domain.Information{Content: "content"}, Analysis{
		Contexts: []InferredContext{{Key: "project", Value: "Ownward", Confidence: math.NaN(), Evidence: "evidence"}},
	})
	if len(analysis.Contexts) != 0 {
		t.Fatalf("non-finite context confidence was retained: %#v", analysis.Contexts)
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
