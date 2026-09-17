package boundedstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

const retiredFormat = "ownward.asset-log/retired-v1"
const storageFormat = "ownward.sqlite-storage/v1"

type storagePointer struct {
	Format string `json:"format"`
	ID     string `json:"id"`
}
type migrationState struct {
	Schema   string            `json:"schema"`
	ID       string            `json:"id"`
	Phase    string            `json:"phase"`
	Manifest json.RawMessage   `json:"original_manifest,omitempty"`
	Sources  map[string]string `json:"sources"`
}

// DeploymentOptions keeps initialization inside the closed migration write gate.
// Initialize may only reuse local state; it must not invoke a semantic model.
type DeploymentOptions struct {
	Options
	VectorSpace string
	Initialize  func(context.Context, *Store, string) error
	checkpoint  func(string) error
}

type borrowedLock struct{}

func (borrowedLock) Close() error { return nil }

func writeDurableJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".storage-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return replaceDurable(f.Name(), path)
}

func readSmallJSON(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("存储状态文件无效")
	}
	d := json.NewDecoder(f)
	if err = d.Decode(value); err != nil {
		return err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return errors.New("存储状态包含多余内容")
	}
	return nil
}

func validStoreID(id string) bool {
	_, err := hex.DecodeString(id)
	return len(id) == 48 && err == nil && strings.ToLower(id) == id
}

func sourceDigest(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("迁移来源不是普通文件")
	}
	h := sha256.New()
	if _, err = copyContext(ctx, h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// OpenDeployment holds the legacy lock from source isolation until the last
// SQLite connection closes. A target is never returned before activation.
func OpenDeployment(ctx context.Context, root string, options DeploymentOptions) (*Store, error) {
	if !filepath.IsAbs(root) || options.Budget == nil {
		return nil, errors.New("缺少绝对资料目录或共享预算")
	}
	root = filepath.Clean(root)
	assets := filepath.Join(root, "assets")
	if err := os.MkdirAll(assets, 0700); err != nil {
		return nil, err
	}
	lock, err := assetlog.LockDirectory(filepath.Join(assets, ".ownward.lock"))
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			lock.Close()
		}
	}()
	if _, e := os.Lstat(filepath.Join(root, "restore.json")); e == nil {
		return nil, errors.New("备份恢复尚未完成，请使用同一备份继续恢复")
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	var state migrationState
	migrationPath := filepath.Join(root, "migration.json")
	err = readSmallJSON(migrationPath, &state)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	hasMigration := err == nil
	var pointer storagePointer
	pointerErr := readSmallJSON(filepath.Join(root, "storage.json"), &pointer)
	if pointerErr != nil && !errors.Is(pointerErr, os.ErrNotExist) {
		return nil, pointerErr
	}
	if pointerErr == nil && (pointer.Format != storageFormat || !validStoreID(pointer.ID)) {
		return nil, errors.New("活动存储身份无效")
	}
	if hasMigration && (state.Schema != "ownward.storage-migration/v1" || !validStoreID(state.ID)) {
		return nil, errors.New("迁移身份无效")
	}
	if hasMigration {
		if err = validateSourceNames(state.Sources); err != nil {
			return nil, err
		}
	}
	if hasMigration && pointerErr == nil && pointer.ID != state.ID {
		return nil, errors.New("迁移与活动存储身份冲突")
	}
	if !hasMigration && pointerErr != nil {
		manifestPath := filepath.Join(assets, "manifest.json")
		var m struct {
			Format string `json:"format"`
		}
		manifestErr := readSmallJSON(manifestPath, &m)
		if manifestErr == nil && m.Format != "ownward.information/v1" && m.Format != "ownward.asset-log/v2" && m.Format != "ownward.asset-log/v3" {
			return nil, errors.New("旧入口已隔离但缺少活动或迁移身份，拒绝初始化")
		}
		if manifestErr != nil && !errors.Is(manifestErr, os.ErrNotExist) {
			return nil, manifestErr
		}
		if entries, e := os.ReadDir(filepath.Join(root, "stores")); e == nil && len(entries) != 0 {
			return nil, errors.New("存在未确认存储，拒绝初始化空库")
		} else if e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
		id, e := newID()
		if e != nil {
			return nil, e
		}
		state = migrationState{Schema: "ownward.storage-migration/v1", ID: id, Phase: "intent", Sources: map[string]string{}}
		if manifestErr == nil {
			state.Manifest, err = os.ReadFile(manifestPath)
			if err != nil {
				return nil, err
			}
		}
		if e = writeDurableJSON(migrationPath, state); e != nil {
			return nil, e
		}
		hasMigration = true
		if e = deploymentCheckpoint(options, "intent"); e != nil {
			return nil, e
		}
	}
	if hasMigration && state.Phase == "rollback" {
		if err = finishRollback(root, state); err != nil {
			return nil, err
		}
		return nil, ErrMigrationCancelled
	}
	if hasMigration && state.Phase == "intent" {
		// The old binary could have written before the isolation marker. Source
		// identity is captured only after that marker is durable under this lock.
		if err = writeDurableJSON(filepath.Join(assets, "manifest.json"), storagePointer{Format: retiredFormat, ID: state.ID}); err != nil {
			return nil, err
		}
		if err = deploymentCheckpoint(options, "isolated-marker"); err != nil {
			return nil, err
		}
		sources, e := legacySources(ctx, root)
		if e != nil {
			return nil, e
		}
		state.Sources, state.Phase = sources, "building"
		if e = writeDurableJSON(migrationPath, state); e != nil {
			return nil, e
		}
	}
	id := pointer.ID
	if hasMigration {
		id = state.ID
	}
	var marker storagePointer
	if err = readSmallJSON(filepath.Join(assets, "manifest.json"), &marker); err != nil || marker.Format != retiredFormat || marker.ID != id {
		return nil, errors.New("旧程序隔离标记与存储身份不一致")
	}
	path := filepath.Join(root, "stores", id, "ownward.sqlite")
	if !hasMigration || state.Phase != "building" {
		if _, err = os.Stat(path); err != nil {
			return nil, fmt.Errorf("活动存储缺失: %w", err)
		}
	}
	if hasMigration && state.Phase == "building" {
		var sourceBytes uint64
		for name, want := range state.Sources {
			if want != "absent" {
				info, e := os.Lstat(filepath.Join(root, filepath.FromSlash(name)))
				if e != nil {
					return nil, e
				}
				if !info.Mode().IsRegular() {
					return nil, errors.New("迁移来源必须为普通文件")
				}
				sourceBytes += uint64(info.Size())
			}
			got, e := sourceDigest(ctx, filepath.Join(root, filepath.FromSlash(name)))
			if e != nil || got != want {
				return nil, fmt.Errorf("迁移来源发生变化: %s", name)
			}
		}
		if err = resourcebudget.CheckFree(filepath.Join(root, "storage.json"), sourceBytes*6+256*uint64(resourcebudget.MiB)); err != nil {
			return nil, err
		}
	}
	opts := options.Options
	opts.lease, opts.paused = borrowedLock{}, true
	s, err := Open(ctx, path, opts)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !owned && s != nil {
			s.Close()
		}
	}()
	if hasMigration && state.Phase == "building" {
		if err = s.beginMigration(ctx, id); err != nil {
			return nil, err
		}
		if err = s.importLegacy(ctx, root, state.Sources, options.VectorSpace); err != nil {
			return nil, err
		}
		if options.Initialize != nil {
			if err = options.Initialize(ctx, s, root); err != nil {
				return nil, err
			}
		}
		if err = s.verifyMigrationSources(ctx, root, state.Sources); err != nil {
			return nil, err
		}
		if err = s.validateMigration(ctx, id); err != nil {
			return nil, err
		}
		if err = s.Close(); err != nil {
			return nil, err
		}
		s, err = Open(ctx, path, opts)
		if err != nil {
			return nil, err
		}
		if err = s.validateMigration(ctx, id); err != nil {
			return nil, err
		}
		state.Phase = "validated"
		if err = writeDurableJSON(migrationPath, state); err != nil {
			return nil, err
		}
		if err = deploymentCheckpoint(options, "validated"); err != nil {
			return nil, err
		}
	}
	if hasMigration && state.Phase == "validated" {
		if err = s.validateMigration(ctx, id); err != nil {
			return nil, err
		}
		if err = writeDurableJSON(filepath.Join(root, "storage.json"), storagePointer{storageFormat, id}); err != nil {
			return nil, err
		}
		if err = deploymentCheckpoint(options, "pointer"); err != nil {
			return nil, err
		}
		if err = s.activateMigration(ctx, id); err != nil {
			return nil, err
		}
		if err = deploymentCheckpoint(options, "activated"); err != nil {
			return nil, err
		}
		state.Phase = "cleaning"
		if err = writeDurableJSON(migrationPath, state); err != nil {
			return nil, err
		}
	}
	if err = s.checkActivated(ctx, id); err != nil {
		return nil, err
	}
	if hasMigration {
		if state.Phase != "cleaning" {
			return nil, errors.New("迁移阶段无效")
		}
		if err = cleanLegacy(root, state.Sources); err != nil {
			return nil, err
		}
		if _, err = s.writer.ExecContext(ctx, "DROP TABLE IF EXISTS migration_assets; DROP TABLE IF EXISTS migration_progress; DROP TABLE IF EXISTS migration_derived"); err != nil {
			return nil, err
		}
		if err = os.Remove(filepath.Join(s.directory, "legacy-json.binlog")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err = deploymentCheckpoint(options, "cleaned"); err != nil {
			return nil, err
		}
		if err = os.Remove(migrationPath); err != nil {
			return nil, err
		}
	}
	s.lock = lock
	s.startMaintenance()
	owned = true
	return s, nil
}

func deploymentCheckpoint(o DeploymentOptions, phase string) error {
	if o.checkpoint != nil {
		return o.checkpoint(phase)
	}
	return nil
}

func (s *Store) beginMigration(ctx context.Context, id string) error {
	_, err := s.writer.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS storage_migration(id TEXT PRIMARY KEY, activated INTEGER NOT NULL DEFAULT 0); INSERT OR IGNORE INTO storage_migration(id) VALUES(?)`, id)
	if err != nil {
		return err
	}
	var count int
	if err = s.writer.QueryRowContext(ctx, "SELECT count(*) FROM storage_migration").Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("目标含有其他迁移身份")
	}
	return nil
}
func (s *Store) checkActivated(ctx context.Context, id string) error {
	var active bool
	if err := s.writer.QueryRowContext(ctx, "SELECT activated FROM storage_migration WHERE id=?", id).Scan(&active); err != nil {
		return err
	}
	if !active {
		return errors.New("迁移目标尚未激活")
	}
	return nil
}
func (s *Store) activateMigration(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "UPDATE storage_migration SET activated=1 WHERE id=?", id)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err != nil || n != 1 {
			return errors.New("迁移身份不存在")
		}
		return nil
	})
}
