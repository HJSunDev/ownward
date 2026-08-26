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

func TestRequestedStagesFullUsesOnlyFrozenCoreStages(t *testing.T) {
	stages, err := requestedStages("full", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"identity", "relations", "merge_split", "incremental_consistency", "organization", "indexing", "lexical", "vector", "graph", "context", "fusion"}
	if len(stages) != len(want) {
		t.Fatalf("full stages = %v, want %v", stages, want)
	}
	for _, stage := range want {
		if !stages[stage] {
			t.Fatalf("full stages omit %q: %v", stage, stages)
		}
	}
	for _, stage := range []string{"efficiency", "semantic_representation", "storage_architecture", "execution_state"} {
		if stages[stage] {
			t.Fatalf("full stages unexpectedly include targeted-only stage %q", stage)
		}
	}
}

func TestRequestedStagesTargetedAcceptsDirectionOnlyStages(t *testing.T) {
	want := []string{"efficiency", "semantic_representation", "storage_architecture", "execution_state"}
	stages, err := requestedStages("targeted", "efficiency,semantic_representation,storage_architecture,execution_state")
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != len(want) {
		t.Fatalf("targeted stages = %v, want %v", stages, want)
	}
	for _, stage := range want {
		if !stages[stage] {
			t.Fatalf("targeted stages omit %q: %v", stage, stages)
		}
	}
}

func TestObservationEnvironmentKeepsFormalComparisonStable(t *testing.T) {
	formal := observationEnvironment("environment", map[string]any{"kind": "candidate-commit", "candidate": "candidate"})
	if _, exists := formal["product_source"]; exists {
		t.Fatal("formal candidate provenance must not change the comparable environment identity")
	}
	worktreeProof := map[string]any{"kind": "worktree-product-source", "product_source_sha256": "source"}
	worktree := observationEnvironment("environment", worktreeProof)
	if worktree["product_source"] == nil {
		t.Fatal("non-formal worktree evidence must retain its source provenance")
	}
}

func TestFormalCandidateRequiresCleanMatchingBuild(t *testing.T) {
	const revision = "candidate"
	if err := verifyBuildIdentity(revision, revision, false, false); err != nil {
		t.Fatalf("clean formal candidate was rejected: %v", err)
	}
	if err := verifyBuildIdentity(revision, revision, true, false); err == nil {
		t.Fatal("modified build was accepted as a formal candidate")
	}
	if err := verifyBuildIdentity(revision, "other", false, false); err == nil {
		t.Fatal("mismatched build was accepted as a formal candidate")
	}
}

func TestModifiedObserverRequiresExplicitTargetedWorktreeBinding(t *testing.T) {
	const source = "source"
	if !modifiedObserverAllowed("targeted", true, source, "") {
		t.Fatal("explicit targeted worktree source must allow a modified observer")
	}
	if !modifiedObserverAllowed("targeted", true, "", "candidate") {
		t.Fatal("explicit targeted equivalent source must allow a modified observer")
	}
	for _, test := range []struct {
		name       string
		mode       string
		selfCheck  bool
		source     string
		equivalent string
	}{
		{name: "full self-check", mode: "full", selfCheck: true, source: source},
		{name: "unbound targeted self-check", mode: "targeted", selfCheck: true},
		{name: "targeted without self-check", mode: "targeted", source: source},
	} {
		t.Run(test.name, func(t *testing.T) {
			if modifiedObserverAllowed(test.mode, test.selfCheck, test.source, test.equivalent) {
				t.Fatal("modified observer was allowed without an explicit non-formal targeted binding")
			}
		})
	}
	if err := verifyBuildIdentity("candidate", "candidate", true, true); err != nil {
		t.Fatalf("bound targeted worktree observer was rejected: %v", err)
	}
}
