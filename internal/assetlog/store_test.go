package assetlog

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

func TestCompactReclaimsAuthorityHistoryAndIsByteStable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "assets")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	value := domain.Information{
		Schema: domain.AssetSchema, ID: "long-lived", Revision: 1,
		CreatedAt: now, UpdatedAt: now, Kind: domain.KindKnowledge,
		Content: "revision-1 " + strings.Repeat("authority ", 32768),
	}
	if err := store.Create(value); err != nil {
		t.Fatal(err)
	}
	for revision := uint64(2); revision <= 4; revision++ {
		value.Revision = revision
		value.UpdatedAt = now.Add(time.Duration(revision) * time.Minute)
		value.Content = string(rune('0'+revision)) + strings.Repeat("authority ", 32768)
		if err := store.Update(value, revision-1); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, logName)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(first))*2 >= before.Size() {
		t.Fatalf("authority history was not substantially reclaimed: before=%d after=%d", before.Size(), len(first))
	}
	if err := store.Compact(); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("repeated authority maintenance was not byte stable")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actual, ok := reopened.Get(value.ID)
	if !ok || !reflect.DeepEqual(actual, value) {
		t.Fatalf("compaction changed the current authoritative revision: %#v", actual)
	}
}

func TestStorePersistsAndReplaysRevisions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "assets")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 1, 2, 3, 0, time.UTC)
	value := domain.Information{Schema: domain.AssetSchema, ID: "item-1", Revision: 1, CreatedAt: now, UpdatedAt: now, Kind: domain.KindKnowledge, Content: "第一版内容"}
	if err := store.Create(value); err != nil {
		t.Fatal(err)
	}
	value.Revision = 2
	value.UpdatedAt = now.Add(time.Minute)
	value.Content = "第二版内容"
	if err := store.Update(value, 1); err != nil {
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
	actual, ok := reopened.Get(value.ID)
	if !ok || actual.Revision != 2 || actual.Content != "第二版内容" {
		t.Fatalf("unexpected replayed value: %#v", actual)
	}
}

func TestBackupRestoresOnlyAuthoritativeAssets(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	store, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 2, 3, 4, 0, time.UTC)
	value := domain.Information{
		Schema: domain.AssetSchema, ID: "item-1", Revision: 1, CreatedAt: now, UpdatedAt: now,
		Kind: domain.KindMethod, Content: "只运行覆盖当前变更的最小充分测试。",
		Contexts: []domain.Context{{Key: "phase", Value: "development"}},
	}
	if err := store.Create(value); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup.ownward")
	if err := store.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(root, "restored")
	if err := Restore(backup, restoredDir); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	actual, ok := restored.Get(value.ID)
	if !ok || !reflect.DeepEqual(actual, value) {
		t.Fatalf("restored value mismatch: %#v", actual)
	}
	if _, err := os.Stat(filepath.Join(restoredDir, "organization.jsonl")); !os.IsNotExist(err) {
		t.Fatal("derived state must not be part of an asset backup")
	}
}

func TestStoreRejectsStaleUpdate(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	value := domain.Information{Schema: domain.AssetSchema, ID: "item-1", Revision: 1, CreatedAt: now, UpdatedAt: now, Kind: domain.KindKnowledge, Content: "内容"}
	if err := store.Create(value); err != nil {
		t.Fatal(err)
	}
	value.Revision = 2
	if err := store.Update(value, 0); err == nil {
		t.Fatal("expected stale update error")
	}
}

func TestStorePreventsConcurrentProcessesFromOpeningSameAssets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "assets")
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := Open(dir); err == nil {
		t.Fatal("expected the second store to be rejected while the first owns the asset directory")
	}
}

func TestStoreDiscardsOnlyUncommittedTail(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "assets")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := domain.Information{Schema: domain.AssetSchema, ID: "item-1", Revision: 1, CreatedAt: now, UpdatedAt: now, Kind: domain.KindKnowledge, Content: "已提交内容"}
	if err := store.Create(value); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, logName)
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"operation":"create"`); err != nil {
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
	actual, ok := reopened.Get(value.ID)
	if !ok || !reflect.DeepEqual(actual, value) {
		t.Fatalf("committed information was not preserved: %#v", actual)
	}
}
