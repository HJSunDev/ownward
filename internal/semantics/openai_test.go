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
					{Type: "supports", TargetID: "candidate-1", Confidence: 0.98, Evidence: "同一验证原则"},
					{Type: "supports", TargetID: "invented", Confidence: 0.99, Evidence: "无来源"},
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
