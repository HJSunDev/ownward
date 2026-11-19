package performance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func TestPrepareReleaseFixtureUsesProductionSQLiteFormats(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	rawBytes, derivedBytes, storageBytes, err := prepareReleaseFixture(t.Context(), dataDir, 16, 512)
	if err != nil {
		t.Fatal(err)
	}
	if rawBytes <= 0 || derivedBytes <= 0 || storageBytes <= derivedBytes {
		t.Fatalf("fixture sizes are invalid: raw=%d derived=%d storage=%d", rawBytes, derivedBytes, storageBytes)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "storage.json")); err != nil {
		t.Fatalf("expected active SQLite storage.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "assets", "information.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy information.jsonl to be cleaned up, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "state", "generations", "gen-production", "organization.binlog")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy state/.../organization.binlog to be cleaned up, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "state")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy state/ directory to be cleaned up, err=%v", err)
	}

	budget, err := resourcebudget.New(20*resourcebudget.MiB, 4*resourcebudget.MiB)
	if err != nil {
		t.Fatal(err)
	}
	store, err := boundedstore.OpenDeployment(t.Context(), dataDir, boundedstore.DeploymentOptions{
		Options: boundedstore.Options{Budget: budget},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	meta, err := store.ReadAssetMeta(t.Context(), "I000015", 0)
	if err != nil || meta.Revision != 1 {
		t.Fatalf("expected SQLite asset I000015, got %+v (%v)", meta, err)
	}
	gen, space, err := store.Generation(t.Context())
	if err != nil || space != "production-storage-space" {
		t.Fatalf("expected active vector space, got gen=%q space=%q err=%v", gen, space, err)
	}
	hits, stats, err := store.VectorSearch(t.Context(), gen, space, deterministicVector(15, 512), nil, 3)
	if err != nil || len(hits) == 0 || hits[0].ID != "I000015" {
		t.Fatalf("expected SQLite vector search hit I000015, got %+v (%v)", hits, err)
	}
	if stats.Blocks < 1 {
		t.Fatalf("expected vectors packed into active SQLite block, got stats=%+v", stats)
	}
}

func TestRunProductionStorageRejectsMissingInputs(t *testing.T) {
	if _, err := RunProductionStorage(t.Context(), ProductionStorageOptions{Scale: 3, Dimensions: 512}); err == nil {
		t.Fatal("expected missing binary/candidate/thresholds to fail")
	}
}
