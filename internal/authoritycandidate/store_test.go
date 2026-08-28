//go:build ownward_migration

package authoritycandidate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestCandidateStoreIsDurableExclusiveAndDistinct(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "candidate")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("candidate store admitted a second writer")
	}
	first := testAsset("asset-1", 1, "candidate format is independently durable")
	if err := store.Seed([]domain.Information{first}); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Revision = 2
	second.UpdatedAt = second.UpdatedAt.Add(time.Minute)
	second.Content = "candidate format preserves accepted updates"
	if _, err := store.UpdateAsset(second, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, manifestName)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "information.jsonl")); !os.IsNotExist(err) {
		t.Fatal("candidate silently reused the assetlog file layout")
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actual, ok := reopened.ReadCurrent(first.ID)
	if !ok || !reflect.DeepEqual(actual, second) {
		t.Fatalf("candidate durable replay mismatch: %#v", actual)
	}
}

func TestCandidateBackupRestoreStreamsAndBindsControl(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	large := testAsset("large", 1, strings.Repeat("bounded-candidate-authority-", 120000))
	if err := store.Seed([]domain.Information{large}); err != nil {
		t.Fatal(err)
	}
	control := contract.ControlState{Schema: contract.ControlStateSchema, Revision: 7, ActiveComposition: strings.Repeat("a", 64), ActiveKernelGeneration: strings.Repeat("b", 64)}
	backup := filepath.Join(root, "candidate.ownward")
	if err := store.BackupAuthority(backup, control); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(root, "restored")
	restoredControl, err := RestoreAuthority(backup, restoredDir)
	if err != nil || restoredControl != control {
		t.Fatalf("candidate authority restore failed: %#v %v", restoredControl, err)
	}
	restored, err := Open(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	actual, ok := restored.ReadCurrent(large.ID)
	if !ok || actual.Content != large.Content {
		t.Fatal("large candidate authority backup lost content")
	}
	encoded, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	encoded[len(encoded)/2] ^= 0xff
	tampered := filepath.Join(root, "tampered.ownward")
	if err := os.WriteFile(tampered, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreAuthority(tampered, filepath.Join(root, "rejected")); err == nil {
		t.Fatal("tampered candidate authority backup was accepted")
	}
}

func TestCandidateBackupRejectsMissingOrInvalidControl(t *testing.T) {
	root := t.TempDir()
	store, err := Open(filepath.Join(root, "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Seed([]domain.Information{testAsset("asset", 1, "sealed")}); err != nil {
		t.Fatal(err)
	}
	for name, control := range map[string]contract.ControlState{
		"missing": {},
		"invalid": {Schema: contract.ControlStateSchema, Revision: 1},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name+".ownward")
			if err := store.BackupAuthority(path, control); err == nil {
				t.Fatal("candidate backup accepted invalid control state")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("invalid candidate backup published an artifact")
			}
		})
	}
}

func TestCandidateStoreRejectsLogTamper(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "candidate")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Seed([]domain.Information{testAsset("asset", 1, "sealed")}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, logName), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("{\"schema\":\"tampered\"}\n")
	_ = file.Close()
	if _, err := Open(dir); err == nil {
		t.Fatal("tampered candidate log was accepted")
	}
}

func testAsset(id string, revision uint64, content string) domain.Information {
	now := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	return domain.Information{Schema: domain.AssetSchema, ID: id, Revision: revision, CreatedAt: now, UpdatedAt: now, Kind: domain.KindKnowledge, Content: content}
}
