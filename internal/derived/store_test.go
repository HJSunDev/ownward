package derived

import (
	"encoding/json"
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
	path := filepath.Join(dir, LogFileName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{'O', 'W', 'D'}); err != nil {
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
	path := filepath.Join(dir, LogFileName)
	if err := os.WriteFile(path, make([]byte, headerSize+footerSize), 0o600); err != nil {
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

func TestStoreMigratesLegacyStateWithoutRemovingIt(t *testing.T) {
	dir := t.TempDir()
	legacy := persistedRecord{
		Schema: legacyRecordSchema, AssetID: "legacy", AssetRevision: 1,
		Status: "ready", Analysis: semantics.Analysis{Summary: "legacy"},
		Embedding: []byte{0, 0, 128, 63},
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(dir, legacyLogFileName)
	if err := os.WriteFile(legacyPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records, err := store.AllWithEmbeddings()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].AssetID != "legacy" || len(records[0].Embedding) != 1 || records[0].Embedding[0] != 1 {
		t.Fatalf("unexpected migrated record: %#v", records)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy source was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, LogFileName)); err != nil {
		t.Fatalf("migrated state was not created: %v", err)
	}
}

func TestStoreQuarantinesCorruptLegacyState(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, legacyLogFileName)
	if err := os.WriteFile(legacyPath, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.RecoveredCorruption() {
		t.Fatal("corrupt legacy state was not reported as recovered")
	}
	matches, err := filepath.Glob(legacyPath + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("corrupt legacy state was not quarantined: matches=%v error=%v", matches, err)
	}
}

func TestStoreQuarantinesTruncatedLegacyState(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, legacyLogFileName)
	legacy := persistedRecord{
		Schema: legacyRecordSchema, AssetID: "legacy", AssetRevision: 1,
		Status: "ready", Analysis: semantics.Analysis{Summary: "legacy"},
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.RecoveredCorruption() || len(store.All()) != 0 {
		t.Fatalf("truncated legacy state was accepted: recovered=%v records=%d", store.RecoveredCorruption(), len(store.All()))
	}
	matches, err := filepath.Glob(legacyPath + ".corrupt-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("truncated legacy state was not quarantined: matches=%v error=%v", matches, err)
	}
}
