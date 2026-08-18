package performance

import (
	"path/filepath"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/derived"
)

func TestPrepareReleaseFixtureUsesProductionFormats(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	rawBytes, derivedBytes, storageBytes, err := prepareReleaseFixture(t.Context(), dataDir, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	if rawBytes == 0 || derivedBytes == 0 || storageBytes < rawBytes+derivedBytes {
		t.Fatalf("fixture sizes are invalid: raw=%d derived=%d storage=%d", rawBytes, derivedBytes, storageBytes)
	}
	assets, err := assetlog.Open(filepath.Join(dataDir, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer assets.Close()
	if got := len(assets.All()); got != 3 {
		t.Fatalf("loaded %d assets, want 3", got)
	}
	state, err := derived.Open(filepath.Join(dataDir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	records, err := state.AllWithEmbeddings()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || len(records[0].Embedding) != 4 {
		t.Fatalf("loaded invalid derived records: %#v", records)
	}
}

func TestRunRequiresReleaseBinaryForThresholdEvaluation(t *testing.T) {
	_, err := Run(t.Context(), Options{Scale: 1, Dimensions: 1, Iterations: 1, Thresholds: "thresholds.json"})
	if err == nil {
		t.Fatal("threshold evaluation accepted a missing release binary")
	}
}

func TestEvaluateRejectsAnyExceededThreshold(t *testing.T) {
	var thresholds limits
	thresholds.Retrieval.ExplicitObject.P95 = 20
	thresholds.Retrieval.SemanticIntent.P95 = 75
	thresholds.Retrieval.RelationConstraint.P95 = 50
	thresholds.Retrieval.ContextApplicability.P95 = 75
	thresholds.Resources.RSSMiB = 768
	thresholds.Resources.ReleaseMiB = 25
	thresholds.Resources.IdleRSSMiB = 50
	thresholds.Resources.IdleCPU = 0.1
	thresholds.Resources.DerivedRatio = 1.35
	thresholds.Ingestion.DurableWriteMS = 20
	thresholds.Ingestion.BasicSearchableMS = 30
	report := Report{ReleaseMiB: 10, IdleRSSMiB: 20, RSSMiB: 769, IdleCPUPercent: 0, DerivedRatio: 1.3, Latency: map[string]Distribution{
		"explicit_object":        {P95MS: 10},
		"semantic_intent":        {P95MS: 20},
		"relation_navigation":    {P95MS: 30},
		"context_applicability":  {P95MS: 20},
		"semantic_concurrency_8": {P95MS: 25},
		"durable_write":          {P95MS: 5},
		"basic_searchable":       {P95MS: 8},
	}}
	checks := evaluate(report, thresholds)
	failed := 0
	for _, check := range checks {
		if !check.Passed {
			failed++
		}
	}
	if failed != 1 || checks[9].Name != "十万条 384 维常驻内存" {
		t.Fatalf("unexpected checks: %#v", checks)
	}
}
