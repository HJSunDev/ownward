package acceptance

import "testing"

func TestRelationKeyTreatsRelatedToAsSymmetric(t *testing.T) {
	forward := relationKey("I018", "related_to", "I019")
	reverse := relationKey("I019", "related_to", "I018")
	if forward != reverse {
		t.Fatalf("related_to must be symmetric: %q != %q", forward, reverse)
	}
	if relationKey("I018", "supports", "I019") == relationKey("I019", "supports", "I018") {
		t.Fatal("directional relations must preserve direction")
	}
}

func TestRelationCheckRequiresGraphEvidenceGain(t *testing.T) {
	var limits thresholds
	limits.Retrieval.RelationConstraint.Precision = 0.95
	limits.Retrieval.RelationConstraint.Recall = 0.9
	limits.Organization.RetrievalEvidenceGain = 0.05
	metrics := &queryMetrics{
		recalls: []float64{1}, ndcgs: []float64{1}, graphGains: []float64{1},
		evidenceCorrect: 1, evidenceTotal: 1,
	}
	checks := retrievalChecks(map[string]*queryMetrics{"relation_constraint": metrics}, limits)
	if !checks[2].Passed {
		t.Fatalf("correct graph evidence should pass: %#v", checks[2])
	}
	metrics.graphGains = []float64{0}
	checks = retrievalChecks(map[string]*queryMetrics{"relation_constraint": metrics}, limits)
	if checks[2].Passed {
		t.Fatalf("relation retrieval without graph evidence gain should fail: %#v", checks[2])
	}
}
