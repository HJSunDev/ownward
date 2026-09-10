package assetlog

import (
	"archive/zip"
	"bytes"
	"github.com/HJSunDev/ownward/internal/domain"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestForgetAtomicRestartAndBackupBeforeCompaction(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "assets")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := domain.Information{Schema: domain.AssetSchema, ID: "forgotten", Revision: 1, Kind: domain.KindGeneral, Content: "private-forget-marker", CreatedAt: now, UpdatedAt: now}
	b := a
	b.ID = "kept"
	b.Content = "kept text"
	if err := s.Create(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(b); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete([]Deletion{{a.ID, 1}, {b.ID, 2}}); err == nil {
		t.Fatal("version mismatch accepted")
	}
	if _, ok := s.Get(a.ID); !ok {
		t.Fatal("partial deletion on rejected batch")
	}
	if err := s.Delete([]Deletion{{a.ID, 1}}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "snapshot.zip")
	if err := s.Backup(archive); err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(a.Content)) {
			t.Fatal("new backup contains forgotten history")
		}
	}
	z.Close()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.Get(a.ID); ok {
		t.Fatal("restart resurrected content")
	}
	if _, ok := s.Get(b.ID); !ok {
		t.Fatal("unrelated content lost")
	}
	if err := s.Create(a); err == nil {
		t.Fatal("deleted identity reused")
	}
	if err := s.Delete([]Deletion{{a.ID, 1}}); err != nil {
		t.Fatal("idempotent deletion", err)
	}
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, logName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(a.Content)) {
		t.Fatal("compaction retained forgotten history")
	}
	restored := filepath.Join(root, "restored")
	if err := Restore(archive, restored); err != nil {
		t.Fatal(err)
	}
	r, err := Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, ok := r.Get(a.ID); ok {
		t.Fatal("new backup restored deleted item")
	}
	if err := r.Create(a); err == nil {
		t.Fatal("restored tombstone lost")
	}
}
