package boundedstore

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

type deploymentArchive struct {
	Format string `json:"format"`
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

const archiveFormat = "ownward.sqlite-backup/v1"

func IsDeploymentArchive(path string) (bool, error) {
	z, e := zip.OpenReader(path)
	if e != nil {
		return false, e
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name == "storage.json" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) ExportArchive(ctx context.Context, destination string) error {
	return s.exportArchive(ctx, destination, false)
}

func (s *Store) ExportDeploymentArchive(ctx context.Context, destination string) error {
	return s.exportArchive(ctx, destination, true)
}

func (s *Store) exportArchive(ctx context.Context, destination string, derived bool) error {
	if !filepath.IsAbs(destination) {
		return errors.New("备份路径必须为绝对路径")
	}
	if _, e := os.Lstat(destination); !errors.Is(e, os.ErrNotExist) {
		return errors.New("备份目标已经存在或不可访问")
	}
	snapshot, e := s.Snapshot(ctx)
	if e != nil {
		return e
	}
	s.copyMu.Lock()
	defer s.copyMu.Unlock()
	copyID := filepath.Base(snapshot.Path)
	copyID = copyID[:len(copyID)-len(".sqlite")]
	defer func() {
		_ = s.removeCopyFiles(copyID)
		_, _ = s.writer.ExecContext(context.WithoutCancel(ctx), "DELETE FROM controlled_copies WHERE id=?", copyID)
	}()
	revision := snapshot.ControlRevision
	if !derived {
		if e = stripArchiveDerived(ctx, snapshot.Path); e != nil {
			return e
		}
		snapshot.SHA256, e = sourceDigest(ctx, snapshot.Path)
		if e != nil {
			return e
		}
	}
	var id string
	if e = s.writer.QueryRowContext(ctx, "SELECT id FROM storage_migration WHERE activated=1").Scan(&id); e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(destination), ".backup-*.tmp")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	z := zip.NewWriter(f)
	metadata, e := z.Create("storage.json")
	if e == nil {
		e = json.NewEncoder(metadata).Encode(deploymentArchive{archiveFormat, id, snapshot.SHA256})
	}
	if e == nil {
		var w io.Writer
		w, e = z.CreateHeader(&zip.FileHeader{Name: "ownward.sqlite", Method: zip.Store})
		if e == nil {
			var r *os.File
			r, e = os.Open(snapshot.Path)
			if e == nil {
				_, e = copyContext(ctx, w, r)
				e = errors.Join(e, r.Close())
			}
		}
	}
	e = errors.Join(e, z.Close(), f.Sync(), f.Close())
	if e != nil {
		return e
	}
	// A control decision during the copy invalidates its protected source.
	if _, e = os.Stat(snapshot.Path); e != nil {
		return e
	}
	return s.write(workContext(ctx, controlWork), func(tx *sql.Tx) error {
		if e := management(ctx, tx); e != nil {
			return e
		}
		var current uint64
		if e := tx.QueryRowContext(ctx, "SELECT coalesce((SELECT revision FROM access_header WHERE singleton=1),0)").Scan(&current); e != nil {
			return e
		}
		var workRevision uint64
		if e := tx.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='owner_work_epoch'").Scan(&workRevision); e != nil {
			return e
		}
		if current != revision || workRevision != snapshot.WorkRevision {
			return errors.New("备份期间控制状态已改变")
		}
		// Linking publishes a complete file without replacing another backup.
		return os.Link(f.Name(), destination)
	})
}

// RestoreArchive installs a verified snapshot into an empty deployment. It is
// also used to stage a frozen handoff; that path preserves connection identity.
func RestoreArchive(ctx context.Context, path, destination string, options Options, preserveConnection ...bool) (contract.ControlState, error) {
	var zero contract.ControlState
	if !filepath.IsAbs(destination) || !filepath.IsAbs(path) {
		return zero, errors.New("恢复路径必须为绝对路径")
	}
	if e := os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
		return zero, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(destination), ".storage-restore-*")
	if e != nil {
		return zero, e
	}
	defer os.RemoveAll(stage)
	z, e := zip.OpenReader(path)
	if e != nil {
		return zero, e
	}
	defer z.Close()
	var meta deploymentArchive
	var db *zip.File
	seen := map[string]bool{}
	for _, f := range z.File {
		if seen[f.Name] || f.Mode()&os.ModeSymlink != 0 || f.FileInfo().IsDir() {
			return zero, errors.New("备份条目无效")
		}
		seen[f.Name] = true
		switch f.Name {
		case "storage.json":
			if f.UncompressedSize64 > 4096 {
				return zero, errors.New("备份清单过大")
			}
			r, e := f.Open()
			if e != nil {
				return zero, e
			}
			e = json.NewDecoder(r).Decode(&meta)
			r.Close()
			if e != nil {
				return zero, e
			}
		case "ownward.sqlite":
			db = f
		default:
			return zero, errors.New("备份含未声明条目")
		}
	}
	if db == nil || meta.Format != archiveFormat || !validStoreID(meta.ID) || len(meta.SHA256) != 64 {
		return zero, errors.New("备份身份无效")
	}
	if e = resourcebudget.CheckFree(filepath.Join(stage, "storage.json"), db.UncompressedSize64+64*uint64(resourcebudget.MiB)); e != nil {
		return zero, e
	}
	dbpath := filepath.Join(stage, "stores", meta.ID, "ownward.sqlite")
	if e = os.MkdirAll(filepath.Dir(dbpath), 0700); e != nil {
		return zero, e
	}
	in, e := db.Open()
	if e != nil {
		return zero, e
	}
	out, e := os.OpenFile(dbpath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		in.Close()
		return zero, e
	}
	h := sha256.New()
	_, e = copyContext(ctx, io.MultiWriter(out, h), in)
	e = errors.Join(e, in.Close(), out.Sync(), out.Close())
	if e != nil {
		return zero, e
	}
	if hex.EncodeToString(h.Sum(nil)) != meta.SHA256 {
		return zero, errors.New("备份正文摘要不匹配")
	}
	s, e := Open(ctx, dbpath, Options{Budget: options.Budget, paused: true})
	if e != nil {
		return zero, e
	}
	var integrity string
	e = s.writer.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity)
	if e == nil && integrity != "ok" {
		e = errors.New("备份数据库完整性检查失败")
	}
	if e == nil {
		e = s.checkActivated(ctx, meta.ID)
	}
	if e == nil {
		_, e = s.writer.ExecContext(ctx, "DELETE FROM controlled_copies")
	}
	if e == nil && (len(preserveConnection) == 0 || !preserveConnection[0]) {
		e = s.invalidateRestoreCredentials(ctx)
	}
	if e == nil {
		zero, e = (&ControlAuthority{s}).ReadSelectedControl(contract.ControlSelection{})
	}
	if e == nil && len(preserveConnection) > 0 && preserveConnection[0] && (zero.Access == nil || zero.Access.Handoff == nil || zero.Access.Handoff.Phase != "frozen") {
		e = errors.New("交接恢复只接受冻结的权威快照")
	}
	e = errors.Join(e, s.Close())
	if e != nil {
		return contract.ControlState{}, e
	}
	if e = os.MkdirAll(filepath.Join(stage, "assets"), 0700); e != nil {
		return zero, e
	}
	if e = writeDurableJSON(filepath.Join(stage, "assets", "manifest.json"), storagePointer{retiredFormat, meta.ID}); e != nil {
		return zero, e
	}
	if e = writeDurableJSON(filepath.Join(stage, "storage.json"), storagePointer{storageFormat, meta.ID}); e != nil {
		return zero, e
	}
	// Hold the original lock while installing entries into an existing empty
	// root, so another opener cannot observe a half-installed pointer.
	if e = os.MkdirAll(filepath.Join(destination, "assets"), 0700); e != nil {
		return zero, e
	}
	lock, e := assetlog.LockDirectory(filepath.Join(destination, "assets", ".ownward.lock"))
	if e != nil {
		return zero, e
	}
	defer lock.Close()
	journalPath := filepath.Join(destination, "restore.json")
	journal := struct {
		Archive            deploymentArchive `json:"archive"`
		PreserveConnection bool              `json:"preserve_connection"`
	}{meta, len(preserveConnection) > 0 && preserveConnection[0]}
	previous := journal
	journalErr := readSmallJSON(journalPath, &previous)
	if journalErr == nil {
		if previous != journal {
			return zero, errors.New("恢复中的备份身份不同")
		}
		if e = rejectSymlinkTree(filepath.Join(destination, "stores", meta.ID)); e != nil {
			return zero, e
		}
	} else if !errors.Is(journalErr, os.ErrNotExist) {
		return zero, journalErr
	} else {
		entries, e := os.ReadDir(destination)
		if e != nil {
			return zero, e
		}
		for _, entry := range entries {
			if entry.Name() != "assets" {
				return zero, errors.New("恢复目标必须为空")
			}
		}
		entries, e = os.ReadDir(filepath.Join(destination, "assets"))
		if e != nil {
			return zero, e
		}
		for _, entry := range entries {
			if entry.Name() != ".ownward.lock" {
				return zero, errors.New("恢复目标必须为空")
			}
		}
		if e = writeDurableJSON(journalPath, journal); e != nil {
			return zero, e
		}
	}
	// The journal closes the ordinary entry point until all durable entries are
	// installed. Retrying the identical archive replaces only its owned target.
	if e = writeDurableJSON(filepath.Join(destination, "assets", "manifest.json"), storagePointer{retiredFormat, meta.ID}); e != nil {
		return zero, e
	}
	if e = os.MkdirAll(filepath.Join(destination, "stores", meta.ID), 0700); e != nil {
		return zero, e
	}
	if e = replaceDurable(dbpath, filepath.Join(destination, "stores", meta.ID, "ownward.sqlite")); e != nil {
		return zero, e
	}
	if e = writeDurableJSON(filepath.Join(destination, "storage.json"), storagePointer{storageFormat, meta.ID}); e != nil {
		return zero, e
	}
	if e = os.Remove(journalPath); e != nil {
		return zero, e
	}
	return zero, nil
}

func ReadDeploymentControl(ctx context.Context, root string, options Options) (contract.ControlState, error) {
	s, e := OpenDeployment(ctx, root, DeploymentOptions{Options: options})
	if e != nil {
		return contract.ControlState{}, e
	}
	defer s.Close()
	return (&ControlAuthority{s}).ReadSelectedControl(contract.ControlSelection{Credential: contract.AuthenticationDigest(ctx)})
}

func ActivateDeploymentHandoff(ctx context.Context, root string, permit contract.Handoff, options Options) error {
	s, e := OpenDeployment(ctx, root, DeploymentOptions{Options: options})
	if e != nil {
		return e
	}
	defer s.Close()
	a := &ControlAuthority{s}
	state, e := a.ReadSelectedControl(contract.ControlSelection{Handoff: permit.ID})
	if e != nil {
		return e
	}
	if state.Access == nil || state.Access.Handoff == nil {
		return errors.New("目的地缺少交接状态")
	}
	h := state.Access.Handoff
	if h.Phase == "active" && h.ID == permit.ID && h.Snapshot == permit.Snapshot && h.Target == permit.Target {
		return nil
	}
	if h.Phase != "frozen" || permit.Phase != "retired" || h.ID != permit.ID || h.Target != permit.Target || permit.Revision != state.Revision+1 || !permit.LocationSaved || len(permit.Snapshot) != 64 {
		return errors.New("接管许可与候选不匹配")
	}
	next := permit
	next.Phase = "active"
	state.Access.Handoff = &next
	state.Revision = permit.Revision
	_, e = a.CompareAndSwapControl(state.Revision-1, state)
	return e
}
