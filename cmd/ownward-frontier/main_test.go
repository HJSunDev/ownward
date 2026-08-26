package main

import (
	"math"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestMetricsUseRepeatedMeasurements(t *testing.T) {
	runs := []collector{
		{"query_p95_ms": &sampleSpec{Dimension: "latency", Stage: "fusion", Direction: "lower", Values: []float64{9, 10}}},
		{"query_p95_ms": &sampleSpec{Dimension: "latency", Stage: "fusion", Direction: "lower", Values: []float64{10, 11}}},
		{"query_p95_ms": &sampleSpec{Dimension: "latency", Stage: "fusion", Direction: "lower", Values: []float64{11, 12}}},
	}
	metrics, err := metricsFromRuns(runs)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 {
		t.Fatalf("metrics = %d, want 1", len(metrics))
	}
	metric := metrics[0]
	if math.Abs(metric.Value-11) > 1e-9 || math.Abs(metric.RepeatabilityError-1) > 1e-9 {
		t.Fatalf("unexpected measured result: %#v", metric)
	}
	if metric.Materiality < 2*metric.RepeatabilityError {
		t.Fatalf("materiality must exceed measured noise: %#v", metric)
	}
}

func TestMetricsRejectInconsistentRepeatedMeasurements(t *testing.T) {
	runs := []collector{
		{"recall": &sampleSpec{Dimension: "quality", Stage: "fusion", Direction: "higher", Values: []float64{1}}},
		{},
	}
	if _, err := metricsFromRuns(runs); err == nil {
		t.Fatal("expected inconsistent repeated measurements to fail")
	}
}

func TestExpandedContentUsesFrozenPadding(t *testing.T) {
	value := assetValue{Content: "fact", Padding: "x", PaddingRepeat: 3}
	if got := expandedContent(value); got != "factxxx" {
		t.Fatalf("expanded content = %q", got)
	}
}

func TestBudgetedReadIDsMatchesLongMemEvalStopPolicy(t *testing.T) {
	assets := []domain.Information{
		{ID: "a", Content: strings.Repeat("a", 13_000)},
		{ID: "b", Content: strings.Repeat("b", 13_000)},
		{ID: "c", Content: "small"},
	}
	results := []core.SearchResult{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got := budgetedReadIDs(results, assets, 24_000, 8)
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("budgeted ids = %v", got)
	}
}

func TestBudgetedReadIDsCountsUnicodeCharactersNotBytes(t *testing.T) {
	assets := []domain.Information{
		{ID: "a", Content: strings.Repeat("汉", 12_000)},
		{ID: "b", Content: strings.Repeat("字", 12_000)},
	}
	got := budgetedReadIDs([]core.SearchResult{{ID: "a"}, {ID: "b"}}, assets, 24_000, 8)
	if len(got) != 2 {
		t.Fatalf("budgeted unicode ids = %v", got)
	}
}
