package boundedstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"io"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
)

func (s *Store) freezeCopy(v bool) {
	s.readerMu.Lock()
	s.copyFrozen = v
	s.signalReadersLocked()
	s.readerMu.Unlock()
}
func (s *Store) copyPath(id string) (string, error) {
	if len(id) != 48 {
		return "", errors.New("快照身份无效")
	}
	for _, v := range id {
		if !(v >= '0' && v <= '9' || v >= 'a' && v <= 'f') {
			return "", errors.New("快照身份无效")
		}
	}
	return filepath.Join(s.directory, "snapshots", id+".sqlite"), nil
}

// Snapshot creates a controlled deployment snapshot, not a user asset export.
// It contains control and derived state and stays in the protected store.
// The freeze prevents repeated online-copy restarts under continuous writes;
// controls still commit and invalidate the copy before delivery.
type StorageSnapshot struct {
	Path            string
	SHA256          string
	ControlRevision uint64
	WorkRevision    uint64
}

func (s *Store) Snapshot(ctx context.Context) (StorageSnapshot, error) {
	if e := s.workAdmission.acquire(ctx, controlWork); e != nil {
		return StorageSnapshot{}, e
	}
	defer s.workAdmission.Unlock()
	s.copyMu.Lock()
	defer func() { s.copyMu.Unlock(); s.wakeMaintenance() }()
	ctx = workContext(ctx, controlWork)
	id, e := newID()
	if e != nil {
		return StorageSnapshot{}, e
	}
	path, e := s.copyPath(id)
	if e != nil {
		return StorageSnapshot{}, e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return StorageSnapshot{}, e
	}
	// Destination SQLite cache and copy buffers fit the background workspace.
	release, e := s.budget.Acquire(ctx, 3*1024*1024, true)
	if e != nil {
		return StorageSnapshot{}, e
	}
	defer release()
	var epoch, controlRevision, workRevision int64
	e = s.write(ctx, func(tx *sql.Tx) error {
		if e := management(ctx, tx); e != nil {
			return e
		}
		var pending bool
		if e := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM forget_operations WHERE state='cleaning')").Scan(&pending); e != nil {
			return e
		}
		if pending {
			return errors.New("遗忘清理尚未完成")
		}
		if e := tx.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='asset_epoch'").Scan(&epoch); e != nil {
			return e
		}
		if e := tx.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='owner_work_epoch'").Scan(&workRevision); e != nil {
			return e
		}
		if e := tx.QueryRowContext(ctx, "SELECT coalesce((SELECT revision FROM access_header WHERE singleton=1),0)").Scan(&controlRevision); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO controlled_copies VALUES(?,?,'building')", id, epoch); e != nil {
			return e
		}
		s.freezeCopy(true)
		return nil
	})
	if e != nil {
		s.freezeCopy(false)
		return StorageSnapshot{}, e
	}
	defer s.freezeCopy(false)
	complete := false
	defer func() {
		if !complete {
			_ = s.removeCopyFiles(id)
		}
	}()
	c, done, e := s.reader(ctx)
	if e != nil {
		return StorageSnapshot{}, e
	}
	defer done()
	e = c.Raw(func(raw any) error {
		b, e := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		}).NewBackup(filepath.ToSlash(path))
		if e != nil {
			return e
		}
		for {
			if e := c.ctx.Err(); e != nil {
				return errors.Join(e, b.Finish())
			}
			more, e := b.Step(64)
			if e != nil {
				return errors.Join(e, b.Finish())
			}
			if !more {
				return b.Finish()
			}
		}
	})
	if e != nil {
		return StorageSnapshot{}, e
	}
	file, e := os.Open(path)
	if e != nil {
		return StorageSnapshot{}, e
	}
	hash := sha256.New()
	_, e = copyContext(ctx, hash, file)
	e = errors.Join(e, file.Close())
	if e != nil {
		return StorageSnapshot{}, e
	}
	e = s.write(ctx, func(tx *sql.Tx) error {
		if e := management(ctx, tx); e != nil {
			return e
		}
		var now, controlNow, workNow int64
		if e := tx.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='asset_epoch'").Scan(&now); e != nil {
			return e
		}
		if e := tx.QueryRowContext(ctx, "SELECT coalesce((SELECT revision FROM access_header WHERE singleton=1),0)").Scan(&controlNow); e != nil {
			return e
		}
		if e := tx.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='owner_work_epoch'").Scan(&workNow); e != nil {
			return e
		}
		if now != epoch || controlNow != controlRevision || workNow != workRevision {
			return errors.New("备份期间控制状态变化，快照未发布")
		}
		_, e := tx.ExecContext(ctx, "UPDATE controlled_copies SET state='ready' WHERE id=?", id)
		return e
	})
	if e != nil {
		return StorageSnapshot{}, e
	}
	complete = true
	return StorageSnapshot{Path: path, SHA256: hex.EncodeToString(hash.Sum(nil)), ControlRevision: uint64(controlRevision), WorkRevision: uint64(workRevision)}, nil
}

func (s *Store) removeCopyFiles(id string) error {
	path, e := s.copyPath(id)
	if e != nil {
		return e
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if e = os.Remove(path + suffix); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	return nil
}
func (s *Store) cleanCopies(ctx context.Context, all bool) (bool, error) {
	if !s.copyMu.TryLock() {
		return false, nil
	}
	defer s.copyMu.Unlock()
	var id string
	e := s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT id FROM controlled_copies WHERE ? OR state='building' ORDER BY id LIMIT 1", all).Scan(&id)
	})
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if e = s.removeCopyFiles(id); e != nil {
		return false, e
	}
	e = s.write(workContext(ctx, controlWork), func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "DELETE FROM controlled_copies WHERE id=?", id)
		return e
	})
	return true, e
}

// RestoreSnapshot restores into a new directory only. The current authority's
// deletion ledger is overlaid before data becomes accessible. Ownership remains;
// old connection credentials are invalidated, never reactivated. Activation by
// the verified owner is a separate authority operation.
func (s *Store) RestoreSnapshot(ctx context.Context, source StorageSnapshot, destination string, options Options) (*Store, error) {
	if e := s.workAdmission.acquire(ctx, controlWork); e != nil {
		return nil, e
	}
	defer s.workAdmission.Unlock()
	ctx = workContext(ctx, controlWork)
	if !filepath.IsAbs(source.Path) || !filepath.IsAbs(destination) || filepath.Clean(destination) == filepath.Clean(s.path) {
		return nil, errors.New("恢复需要独立的绝对路径")
	}
	if _, e := os.Stat(destination); e == nil {
		return nil, errors.New("恢复目标已经存在")
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	s.copyMu.Lock()
	defer func() { s.copyMu.Unlock(); s.wakeMaintenance() }()
	s.freezeCopy(true)
	defer s.freezeCopy(false)
	var system string
	var revision uint64
	e := s.view(ctx, func(q queryer) error {
		if e := management(ctx, q); e != nil {
			return e
		}
		return q.QueryRowContext(ctx, "SELECT system,revision FROM access_header WHERE singleton=1").Scan(&system, &revision)
	})
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
		return nil, e
	}
	input, e := os.Open(source.Path)
	if e != nil {
		return nil, e
	}
	defer input.Close()
	output, e := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	complete := false
	defer func() {
		if !complete {
			for _, suffix := range []string{"", "-wal", "-shm"} {
				_ = os.Remove(destination + suffix)
			}
		}
	}()
	hash := sha256.New()
	_, e = copyContext(ctx, io.MultiWriter(output, hash), input)
	if e == nil && hex.EncodeToString(hash.Sum(nil)) != source.SHA256 {
		e = errors.New("snapshot checksum mismatch")
	}
	e = errors.Join(e, output.Sync(), output.Close())
	if e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", filepath.ToSlash(destination))
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	c, e := db.Conn(ctx)
	if e != nil {
		return nil, e
	}
	defer c.Close()
	if e = configure(ctx, c, 256, false); e != nil {
		return nil, e
	}
	var check, identity string
	if e = c.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); e != nil || check != "ok" {
		return nil, errors.Join(errors.New("恢复快照完整性校验失败"), e)
	}
	if e = c.QueryRowContext(ctx, "SELECT system FROM access_header WHERE singleton=1").Scan(&identity); e != nil || identity != system {
		return nil, errors.New("快照不属于当前信息体系")
	}
	var hasWork bool
	if e = c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='owner_draft_grants')").Scan(&hasWork); e != nil {
		return nil, e
	}
	if hasWork {
		if _, e = c.ExecContext(ctx, "DELETE FROM owner_draft_grants"); e != nil {
			return nil, e
		}
	}
	if _, e = c.ExecContext(ctx, "DELETE FROM controlled_copies; UPDATE access_header SET revision=revision+1,deletion_epoch=deletion_epoch+1,frozen=1,stopping=0; UPDATE access_principals SET credential='invalidated:'||id,permissions=0,revision=revision+1;"); e != nil {
		return nil, e
	}
	after := ""
	for {
		var id, principal, digest, state string
		var targets int
		e = s.view(ctx, func(q queryer) error {
			return q.QueryRowContext(ctx, "SELECT id,principal,digest,state,targets FROM forget_operations WHERE id>? AND state<>'staging' ORDER BY id LIMIT 1", after).Scan(&id, &principal, &digest, &state, &targets)
		})
		if errors.Is(e, sql.ErrNoRows) {
			break
		}
		if e != nil {
			return nil, e
		}
		// 旧快照中的暂存目标仍计入统计；发布覆盖屏障前先扣除这些可见目标。
		if _, e = c.ExecContext(ctx, "UPDATE lexical_stats SET documents=documents-(SELECT count(*) FROM live_assets a JOIN forget_targets t ON t.asset=a.id WHERE t.operation=?),terms=terms-coalesce((SELECT sum(d.length) FROM live_assets a JOIN lexical_documents d ON d.payload=a.payload JOIN forget_targets t ON t.asset=a.id WHERE t.operation=?),0) WHERE singleton=1", id, id); e != nil {
			return nil, e
		}
		if _, e = c.ExecContext(ctx, "INSERT INTO forget_operations(id,principal,digest,state,targets) VALUES(?,?,?,'cleaning',?) ON CONFLICT(id) DO UPDATE SET state='cleaning',targets=excluded.targets", id, principal, digest, targets); e != nil {
			return nil, e
		}
		assetAfter := ""
		for {
			var asset string
			var rev uint64
			e = s.view(ctx, func(q queryer) error {
				return q.QueryRowContext(ctx, "SELECT asset,revision FROM forget_targets WHERE operation=? AND asset>? ORDER BY asset LIMIT 1", id, assetAfter).Scan(&asset, &rev)
			})
			if errors.Is(e, sql.ErrNoRows) {
				break
			}
			if e != nil {
				return nil, e
			}
			if _, e = c.ExecContext(ctx, "UPDATE lexical_stats SET documents=documents-(SELECT count(*) FROM live_assets WHERE id=?),terms=terms-coalesce((SELECT d.length FROM live_assets a JOIN lexical_documents d ON d.payload=a.payload WHERE a.id=?),0) WHERE singleton=1", asset, asset); e != nil {
				return nil, e
			}
			if _, e = c.ExecContext(ctx, "INSERT OR REPLACE INTO forget_targets VALUES(?,?,?)", id, asset, rev); e != nil {
				return nil, e
			}
			assetAfter = asset
		}
		after = id
	}
	e = s.view(ctx, func(q queryer) error {
		var now uint64
		e := q.QueryRowContext(ctx, "SELECT revision FROM access_header WHERE singleton=1").Scan(&now)
		if e == nil && now != revision {
			return errors.New("恢复期间控制状态变化，未开放恢复副本")
		}
		return e
	})
	if e != nil {
		return nil, e
	}
	if _, e = c.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
		return nil, e
	}
	if e = c.Close(); e != nil {
		return nil, e
	}
	if e = db.Close(); e != nil {
		return nil, e
	}
	options.paused = true
	restored, e := Open(ctx, destination, options)
	if e != nil {
		return nil, e
	}
	var native bool
	if e = restored.writer.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='authority_header')").Scan(&native); e == nil && native {
		e = restored.invalidateRestoreCredentials(ctx)
	}
	if e != nil {
		restored.Close()
		return nil, e
	}
	reservation, e := options.Budget.Acquire(ctx, 3*resourcebudget.MiB, true)
	if e != nil {
		restored.Close()
		return nil, e
	}
	work, _ := resourcebudget.New(3*resourcebudget.MiB, 0)
	// 源端管理授权仍由源实例复核；目标内部清理不能复用已经失效的源租约。
	recoveryCtx := context.WithValue(ctx, accessKey{}, nil)
	recoveryCtx = context.WithValue(recoveryCtx, snapshotKey{}, nil)
	e = restored.DrainMaintenance(resourcebudget.WithContext(recoveryCtx, work))
	reservation()
	if e != nil {
		restored.Close()
		return nil, e
	}
	restored.startMaintenance()
	complete = true
	return restored, nil
}

func copyContext(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	buffer := make([]byte, ChunkBytes)
	var total int64
	for {
		if e := ctx.Err(); e != nil {
			return total, e
		}
		n, e := r.Read(buffer)
		if n > 0 {
			written, err := w.Write(buffer[:n])
			total += int64(written)
			if err != nil {
				return total, err
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if e == io.EOF {
			return total, nil
		}
		if e != nil {
			return total, e
		}
	}
}
