package derived

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStagedGenerationBecomesDurableOnlyAtAtomicCommit(t *testing.T) {
	root := t.TempDir()
	current, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	records := make([]Record, 64)
	for index := range records {
		records[index] = Record{AssetID: fmt.Sprintf("staged-%03d", index), AssetRevision: 1, Status: "ready", EmbeddingSpace: "staged-test", Embedding: []float32{1, float32(index + 1)}}
	}
	next, err := CreateGeneration(root, "gen-staged-batch")
	if err != nil {
		t.Fatal(err)
	}
	if err := next.StageGeneration(records); err != nil {
		t.Fatal(err)
	}
	if err := next.StageGeneration(records[:1]); err == nil {
		t.Fatal("non-empty generation accepted a second staged batch")
	}
	if _, exists := current.Get(records[0].AssetID); exists {
		t.Fatal("uncommitted generation became visible")
	}
	if err := current.CommitGeneration(next, GenerationMetadata{AssetCount: len(records), AssetSnapshot: strings.Repeat("e", 64), EmbeddingSpace: "staged-test"}); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actual, err := reopened.AllWithEmbeddings()
	if err != nil || len(actual) != len(records) {
		t.Fatalf("staged generation did not recover: count=%d err=%v", len(actual), err)
	}
}

func TestGenerationCommitSwitchesCompleteStateAndReloads(t *testing.T) {
	root := t.TempDir()
	current, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Put(Record{AssetID: "old", AssetRevision: 1, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	generation, err := NewGenerationID(time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	next, err := CreateGeneration(root, generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Put(Record{AssetID: "new", AssetRevision: 2, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := current.CommitGeneration(next, GenerationMetadata{AssetCount: 1, AssetSnapshot: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	if current.Generation() != generation {
		t.Fatalf("unexpected generation %q", current.Generation())
	}
	if _, exists := current.Get("old"); exists {
		t.Fatal("previous generation remained visible")
	}
	if value, exists := current.Get("new"); !exists || value.AssetRevision != 2 {
		t.Fatalf("new generation is not visible: %#v %v", value, exists)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Generation() != generation {
		t.Fatalf("reopened generation %q", reopened.Generation())
	}
	if _, exists := reopened.Get("new"); !exists {
		t.Fatal("committed generation was not restored")
	}
}

func TestCommittedGenerationAcceptsDurableIncrementalWritesAndReloads(t *testing.T) {
	root := t.TempDir()
	current, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	next, err := CreateGeneration(root, "gen-incremental")
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Put(Record{AssetID: "base", AssetRevision: 1, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := current.CommitGeneration(next, GenerationMetadata{AssetCount: 1, AssetSnapshot: strings.Repeat("c", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := current.Put(Record{AssetID: "later", AssetRevision: 1, Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, exists := reopened.Get("base"); !exists {
		t.Fatal("sealed baseline disappeared")
	}
	if _, exists := reopened.Get("later"); !exists {
		t.Fatal("valid incremental record disappeared")
	}
}

func TestCommittedGenerationDiscardsCorruptIncrementalTailWithoutLosingBaseline(t *testing.T) {
	root := t.TempDir()
	current, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	next, err := CreateGeneration(root, "gen-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Put(Record{AssetID: "base", AssetRevision: 1, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := current.CommitGeneration(next, GenerationMetadata{AssetCount: 1, AssetSnapshot: strings.Repeat("d", 64)}); err != nil {
		t.Fatal(err)
	}
	directory := current.directory
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(directory, LogFileName), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("corrupt-complete-record")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.RecoveredCorruption() {
		t.Fatal("corrupt incremental tail was not reported")
	}
	if _, exists := reopened.Get("base"); !exists {
		t.Fatal("sealed baseline was lost while recovering the tail")
	}
}

func TestGenerationRejectsIncompleteCandidateWithoutChangingCurrent(t *testing.T) {
	root := t.TempDir()
	current, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if err := current.Put(Record{AssetID: "current", AssetRevision: 1, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	next, err := CreateGeneration(root, "gen-incomplete")
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Put(Record{AssetID: "candidate", AssetRevision: 1, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := current.CommitGeneration(next, GenerationMetadata{AssetCount: 2, AssetSnapshot: strings.Repeat("b", 64)}); err == nil {
		t.Fatal("incomplete generation was committed")
	}
	if _, exists := current.Get("current"); !exists || current.Generation() != "legacy" {
		t.Fatal("failed generation changed the current state")
	}
	if err := next.Discard(); err != nil {
		t.Fatal(err)
	}
}
