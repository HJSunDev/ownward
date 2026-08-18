package derived

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestStoreRejectsAnOlderDerivedRevision(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Put(Record{AssetID: "one", AssetRevision: 2, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(Record{AssetID: "one", AssetRevision: 1, Status: "ready"}); !errors.Is(err, ErrStaleRecord) {
		t.Fatalf("unexpected stale write result: %v", err)
	}
	current, _ := store.Get("one")
	if current.AssetRevision != 2 {
		t.Fatalf("stale write replaced revision 2: %#v", current)
	}
}

func TestStoreTruncatesIncompleteTail(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(Record{AssetID: "one", AssetRevision: 1, Status: "ready", Analysis: semantics.Analysis{Summary: "one"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "organization.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"asset_id":"partial"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.RecoveredCorruption() {
		t.Fatal("an incomplete final write should be truncated, not quarantined")
	}
	if _, ok := reopened.Get("one"); !ok {
		t.Fatal("committed state was lost")
	}
}

func TestStorePersistsCompactEmbeddingLosslessly(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	expected := []float32{-0.25, 0, 0.5, 1}
	if err := store.Put(Record{AssetID: "vector", AssetRevision: 1, Status: "ready", Embedding: expected}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	all, err := reopened.AllWithEmbeddings()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("unexpected records: %#v", all)
	}
	actual := all[0]
	for index := range expected {
		if actual.Embedding[index] != expected[index] {
			t.Fatalf("embedding changed at %d: got %v want %v", index, actual.Embedding[index], expected[index])
		}
	}
}

func TestStoreQuarantinesCommittedCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "organization.jsonl")
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.RecoveredCorruption() || len(store.All()) != 0 {
		t.Fatalf("unexpected recovered store: recovered=%v records=%d", store.RecoveredCorruption(), len(store.All()))
	}
	matches, err := filepath.Glob(path + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("corrupt state was not quarantined: matches=%v error=%v", matches, err)
	}
}
