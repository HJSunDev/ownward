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
