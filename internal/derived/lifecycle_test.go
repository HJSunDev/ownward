//go:build ownward_migration

package derived

import (
	"strings"
	"testing"
)

func TestGenerationSwitchRetainsRollbackTargetUntilExplicitRetirement(t *testing.T) {
	root := t.TempDir()
	current, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := CreateGeneration(root, "gen-baseline")
	if err != nil {
		t.Fatal(err)
	}
	if err := baseline.StageGeneration([]Record{{AssetID: "asset", AssetRevision: 1, Status: "ready"}}); err != nil {
		t.Fatal(err)
	}
	if err := current.CommitGeneration(baseline, GenerationMetadata{AssetCount: 1, AssetSnapshot: strings.Repeat("1", 64)}); err != nil {
		t.Fatal(err)
	}
	candidate, err := CreateGeneration(root, "gen-candidate")
	if err != nil {
		t.Fatal(err)
	}
	if err := candidate.StageGeneration([]Record{{AssetID: "asset", AssetRevision: 1, Status: "ready"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := candidate.SealGeneration(GenerationMetadata{AssetCount: 1, AssetSnapshot: strings.Repeat("2", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchGeneration(root, "gen-baseline", "gen-candidate"); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	retained, err := OpenGeneration(root, "gen-baseline")
	if err != nil {
		t.Fatalf("rollback generation was deleted at switch: %v", err)
	}
	_ = retained.Close()
	if _, err := SwitchGeneration(root, "gen-baseline", "gen-candidate"); err == nil {
		t.Fatal("stale derived pointer compare-and-swap was accepted")
	}
	if err := RetireGeneration(root, "gen-candidate"); err == nil {
		t.Fatal("active generation was retired")
	}
	if err := RetireGeneration(root, "gen-baseline"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenGeneration(root, "gen-baseline"); err == nil {
		t.Fatal("explicitly retired generation remained readable")
	}
}

func TestGenerationInspectionBindsIncrementalTail(t *testing.T) {
	root := t.TempDir()
	store, err := CreateGeneration(root, "gen-tail")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StageGeneration([]Record{{AssetID: "asset", AssetRevision: 1, Status: "ready"}}); err != nil {
		t.Fatal(err)
	}
	before, err := store.SealGeneration(GenerationMetadata{AssetCount: 1, AssetSnapshot: strings.Repeat("3", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(Record{AssetID: "asset", AssetRevision: 2, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := InspectGeneration(root, "gen-tail")
	if err != nil {
		t.Fatal(err)
	}
	if before.ManifestSHA256 != after.ManifestSHA256 || before.LogSHA256 == after.LogSHA256 || before.LogBytes >= after.LogBytes {
		t.Fatalf("incremental tail identity was not separated from sealed baseline: before=%#v after=%#v", before, after)
	}
}
