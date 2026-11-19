package boundedstore

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func writeLegacyAssetBackupZip(t *testing.T, archivePath string, entries map[string][]byte, format string) {
	t.Helper()
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	digests := map[string]string{}
	for name, content := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
		if name == "manifest.json" || name == "information.jsonl" {
			sum := sha256.Sum256(content)
			digests[name] = hex.EncodeToString(sum[:])
		}
	}
	if _, hasBackup := entries["backup.json"]; !hasBackup {
		meta, err := json.Marshal(struct {
			Format    string            `json:"format"`
			CreatedAt time.Time         `json:"created_at"`
			Files     map[string]string `json:"files"`
		}{Format: format, CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Files: digests})
		if err != nil {
			t.Fatal(err)
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: "backup.json", Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(append(meta, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreLegacyAssetArchiveImportsIntoSQLiteAndRejectsInvalidArchives(t *testing.T) {
	ctx := context.Background()
	budget, err := resourcebudget.New(20*resourcebudget.MiB, 4*resourcebudget.MiB)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Budget: budget}
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	manifestBytes, _ := json.Marshal(struct {
		Format    string    `json:"format"`
		CreatedAt time.Time `json:"created_at"`
	}{Format: domain.AssetSchema, CreatedAt: now})
	asset := domain.Information{
		Schema: domain.AssetSchema, ID: "legacy-backup-1", Revision: 1,
		CreatedAt: now, UpdatedAt: now, Kind: domain.KindKnowledge, Content: "Restored from ownward.backup/v1 archive.",
	}
	lineBytes, _ := json.Marshal(struct {
		Operation string             `json:"operation"`
		Recorded  time.Time          `json:"recorded_at"`
		Value     domain.Information `json:"value"`
	}{Operation: "create", Recorded: now, Value: asset})
	jsonlBytes := append(lineBytes, '\n')

	validZip := filepath.Join(t.TempDir(), "valid-v1.zip")
	writeLegacyAssetBackupZip(t, validZip, map[string][]byte{
		"manifest.json":     append(manifestBytes, '\n'),
		"information.jsonl": jsonlBytes,
	}, "ownward.backup/v1")

	dest := filepath.Join(t.TempDir(), "restored")
	if err := RestoreLegacyAssetArchive(ctx, validZip, dest, opts); err != nil {
		t.Fatalf("RestoreLegacyAssetArchive failed: %v", err)
	}
	store, err := OpenDeployment(ctx, dest, DeploymentOptions{Options: opts})
	if err != nil {
		t.Fatalf("OpenDeployment after legacy archive restore failed: %v", err)
	}
	defer store.Close()
	header, err := store.ReadAssetMeta(ctx, "legacy-backup-1", 0)
	if err != nil {
		t.Fatalf("ReadAssetMeta failed: %v", err)
	}
	if header.Revision != 1 || header.Kind != domain.KindKnowledge {
		t.Fatalf("unexpected restored header: %+v", header)
	}
	if got := readTest(t, store, "legacy-backup-1"); got != asset.Content {
		t.Fatalf("unexpected restored content: %q", got)
	}

	// Reject non-empty destination without altering existing files.
	if err := RestoreLegacyAssetArchive(ctx, validZip, dest, opts); err == nil {
		t.Fatal("expected non-empty destination to be rejected")
	}

	// Reject path traversal / undeclared entries.
	traversalZip := filepath.Join(t.TempDir(), "traversal.zip")
	writeLegacyAssetBackupZip(t, traversalZip, map[string][]byte{
		"manifest.json":     append(manifestBytes, '\n'),
		"information.jsonl": jsonlBytes,
		"../evil.txt":       []byte("nope"),
	}, "ownward.backup/v1")
	traversalDest := filepath.Join(t.TempDir(), "traversal-dest")
	if err := RestoreLegacyAssetArchive(ctx, traversalZip, traversalDest, opts); err == nil {
		t.Fatal("expected undeclared zip entry to be rejected")
	}
	if _, err := os.Stat(traversalDest); !os.IsNotExist(err) {
		t.Fatalf("failed restore touched destination: %v", err)
	}

	// Reject corrupt JSONL before touching destination.
	corruptZip := filepath.Join(t.TempDir(), "corrupt.zip")
	writeLegacyAssetBackupZip(t, corruptZip, map[string][]byte{
		"manifest.json":     append(manifestBytes, '\n'),
		"information.jsonl": []byte("{invalid-json\n"),
	}, "ownward.backup/v1")
	corruptDest := filepath.Join(t.TempDir(), "corrupt-dest")
	if err := RestoreLegacyAssetArchive(ctx, corruptZip, corruptDest, opts); err == nil {
		t.Fatal("expected corrupt JSONL to be rejected")
	}
	if _, err := os.Stat(corruptDest); !os.IsNotExist(err) {
		t.Fatalf("corrupt restore touched destination: %v", err)
	}
}
