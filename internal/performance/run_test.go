package performance

import "testing"

func TestEvaluateRejectsAnyExceededThreshold(t *testing.T) {
	var thresholds limits
	thresholds.Retrieval.ExplicitObject.P95 = 20
	thresholds.Retrieval.SemanticIntent.P95 = 75
	thresholds.Retrieval.RelationConstraint.P95 = 50
	thresholds.Retrieval.ContextApplicability.P95 = 75
	thresholds.Resources.RSSMiB = 768
	thresholds.Resources.IdleCPU = 0.1
	thresholds.Resources.DerivedRatio = 1.35
	thresholds.Ingestion.DurableWriteMS = 20
	thresholds.Ingestion.BasicSearchableMS = 30
	report := Report{RSSMiB: 769, IdleCPUPercent: 0, DerivedRatio: 1.3, Latency: map[string]Distribution{
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
	if failed != 1 || checks[7].Name != "十万条 384 维常驻内存" {
		t.Fatalf("unexpected checks: %#v", checks)
	}
}
