package derived

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestStoreMigratesPreviousFullPayloadToCompactCurrentState(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 27, 11, 0, 0, 0, time.UTC)
	asset := domain.Information{
		Schema: domain.AssetSchema, ID: "asset", Revision: 1, CreatedAt: now, UpdatedAt: now,
		Kind: domain.KindKnowledge, Content: "current " + strings.Repeat("a", 256*1024),
	}
	candidate := domain.Information{
		Schema: domain.AssetSchema, ID: "candidate", Revision: 2, CreatedAt: now, UpdatedAt: now,
		Kind: domain.KindKnowledge, Content: "candidate " + strings.Repeat("b", 256*1024),
	}
	work, err := semantics.NewWork("generation-v3", asset, []semantics.Candidate{{
		ID: candidate.ID, Revision: candidate.Revision, Content: candidate.Content, Similarity: 0.9,
	}}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	submission, err := semantics.NormalizeSubmission(work, semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: asset.ID, Revision: asset.Revision,
		Capability: semantics.Capability{ID: "codex", Version: "gpt-5.6-luna"},
		Status:     semantics.SubmissionComplete, Analysis: semantics.Analysis{Summary: "accepted summary"},
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	pending := encodePreviousRecordForTest(t, previousRecordMetadata{
		Schema: previousRecordSchema, AssetID: asset.ID, AssetRevision: asset.Revision,
		GeneratedAt: now, Provider: "semantic", Status: "pending", SemanticWork: &work,
	})
	accepted := encodePreviousRecordForTest(t, previousRecordMetadata{
		Schema: previousRecordSchema, AssetID: asset.ID, AssetRevision: asset.Revision,
		GeneratedAt: now.Add(time.Minute), Provider: "semantic", Status: "ready",
		Analysis: submission.Analysis, SemanticWork: &work, SemanticResult: &submission,
	})
	path := filepath.Join(dir, LogFileName)
	legacyBytes := append(pending, accepted...)
	if err := os.WriteFile(path, legacyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	record, ok := store.Get(asset.ID)
	if !ok || record.SemanticWorkReference == nil || record.SemanticReceipt == nil ||
		record.SemanticWork != nil || record.SemanticResult != nil || !record.SemanticReceipt.Matches(submission) {
		t.Fatalf("previous state was not migrated losslessly: %#v", record)
	}
	compacted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted)*2 >= len(legacyBytes) {
		t.Fatalf("full semantic payload was not substantially reclaimed: before=%d after=%d", len(legacyBytes), len(compacted))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	afterReopen, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterReopen) != string(compacted) {
		t.Fatal("reopening compact state changed its durable bytes")
	}
}

func encodePreviousRecordForTest(t *testing.T, metadata previousRecordMetadata) []byte {
	t.Helper()
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	encoded := make([]byte, headerSize+len(encodedMetadata)+footerSize)
	copy(encoded[:4], recordMagic[:])
	binary.LittleEndian.PutUint32(encoded[4:8], uint32(len(encodedMetadata)))
	binary.LittleEndian.PutUint32(encoded[12:16], crc32.ChecksumIEEE(encodedMetadata))
	copy(encoded[headerSize:], encodedMetadata)
	copy(encoded[len(encoded)-footerSize:], commitMagic[:])
	return encoded
}

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

func TestStoreRejectsUngroundedDerivedSemantics(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	invalidContext := Record{AssetID: "context", AssetRevision: 1, Status: "ready", Analysis: semantics.Analysis{
		Contexts: []semantics.InferredContext{{Key: "project", Value: "unknown", Confidence: 0.5, Evidence: "guess"}},
	}}
	if err := store.Put(invalidContext); err == nil {
		t.Fatal("low-confidence inferred context must be rejected")
	}
	invalidRelation := Record{AssetID: "source", AssetRevision: 1, Status: "ready", Analysis: semantics.Analysis{
		Relations: []semantics.Relation{{Type: "related_to", TargetID: "target", TargetRevision: 1, Confidence: 0.9}},
	}}
	if err := store.Put(invalidRelation); err == nil {
		t.Fatal("inferred relation without evidence must be rejected")
	}
	if err := store.Put(Record{AssetID: "explicit", AssetRevision: 1, Status: "ready", Analysis: semantics.Analysis{
		Relations: []semantics.Relation{{Type: "supports", TargetID: "target", Confidence: 1}},
	}}); err != nil {
		t.Fatalf("explicit durable relation should remain valid: %v", err)
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
