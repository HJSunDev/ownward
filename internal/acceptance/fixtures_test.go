package acceptance

import (
	"path/filepath"
	"testing"
)

func TestFrozenV3FixturesAreCompleteAndInternallyConsistent(t *testing.T) {
	path := filepath.Join("..", "..", "benchmarks", "acceptance", "v3", "baseline.json")
	fixtures, err := loadFixtures(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures.Information) != 30 || len(fixtures.Kinds) != 30 || len(fixtures.Queries) != 10 {
		t.Fatalf("unexpected frozen fixture counts: information=%d kinds=%d queries=%d", len(fixtures.Information), len(fixtures.Kinds), len(fixtures.Queries))
	}
}

func TestFrozenV5UsesRelationEvidenceGainWithoutChangingFixtures(t *testing.T) {
	path := filepath.Join("..", "..", "benchmarks", "acceptance", "v5", "baseline.json")
	fixtures, err := loadFixtures(path)
	if err != nil {
		t.Fatal(err)
	}
	if fixtures.Descriptor.Schema != "ownward.acceptance-baseline/v5" {
		t.Fatalf("unexpected schema: %s", fixtures.Descriptor.Schema)
	}
	if fixtures.Thresholds.Organization.RetrievalEvidenceGain != 0.05 || fixtures.Thresholds.Organization.RetrievalRecallGain != 0 {
		t.Fatalf("unexpected relation gain thresholds: %#v", fixtures.Thresholds.Organization)
	}
	if len(fixtures.Information) != 30 || len(fixtures.Kinds) != 30 || len(fixtures.Relations) != 15 || len(fixtures.Queries) != 10 || len(fixtures.Updates) != 1 {
		t.Fatalf("v5 changed frozen fixture coverage")
	}
}
