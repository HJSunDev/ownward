package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/assetlog"
)

var ErrMigrationCancelled = errors.New("迁移已取消，原资料入口已恢复")

// CancelDeployment is an explicit maintenance action. Once activation is
// durable, rollback is refused even if no user has written to the new store.
func CancelDeployment(ctx context.Context, root string) error {
	if !filepath.IsAbs(root) {
		return errors.New("资料路径必须为绝对路径")
	}
	root = filepath.Clean(root)
	lock, e := assetlog.LockDirectory(filepath.Join(root, "assets", ".ownward.lock"))
	if e != nil {
		return e
	}
	defer lock.Close()
	var state migrationState
	if e = readSmallJSON(filepath.Join(root, "migration.json"), &state); e != nil {
		return e
	}
	if state.Schema != "ownward.storage-migration/v1" || !validStoreID(state.ID) {
		return errors.New("迁移身份无效")
	}
	if e = validateSourceNames(state.Sources); e != nil {
		return e
	}
	if state.Phase == "rollback" {
		return finishRollback(root, state)
	}
	switch state.Phase {
	case "intent", "building", "validated":
	case "cleaning":
		return errors.New("目标已激活，只允许向前恢复")
	default:
		return errors.New("迁移阶段无效，拒绝回退")
	}
	var pointer storagePointer
	pointerErr := readSmallJSON(filepath.Join(root, "storage.json"), &pointer)
	if pointerErr != nil && !errors.Is(pointerErr, os.ErrNotExist) {
		return pointerErr
	}
	if pointerErr == nil && (pointer.Format != storageFormat || pointer.ID != state.ID) {
		return errors.New("活动指针与迁移身份冲突")
	}
	path := filepath.Join(root, "stores", state.ID, "ownward.sqlite")
	if _, e = os.Stat(path); e == nil {
		db, e := sql.Open("sqlite", filepath.ToSlash(path))
		if e != nil {
			return e
		}
		var active bool
		var identity string
		e = db.QueryRowContext(ctx, "SELECT id,activated FROM storage_migration").Scan(&identity, &active)
		db.Close()
		if e != nil || identity != state.ID {
			return errors.New("无法确认目标激活状态，拒绝回退")
		}
		if active {
			return errors.New("目标已激活，只允许向前恢复")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	} else if pointerErr == nil || state.Phase == "validated" {
		// Activation commits after publishing the pointer but before advancing
		// the journal. A missing target cannot prove that activation never ran.
		return errors.New("无法确认目标激活状态，拒绝回退")
	}
	for name, want := range state.Sources {
		got, e := sourceDigest(ctx, filepath.Join(root, filepath.FromSlash(name)))
		if e != nil || got != want {
			return errors.New("原资料不完整，拒绝回退")
		}
	}
	state.Phase = "rollback"
	if e = writeDurableJSON(filepath.Join(root, "migration.json"), state); e != nil {
		return e
	}
	return finishRollback(root, state)
}

func finishRollback(root string, state migrationState) error {
	pointer := filepath.Join(root, "storage.json")
	if e := os.Remove(pointer); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	target := filepath.Join(root, "stores", state.ID)
	if !validStoreID(state.ID) {
		return errors.New("回退目标无效")
	}
	if e := rejectSymlinkTree(target); e != nil {
		return e
	}
	if e := os.RemoveAll(target); e != nil {
		return e
	}
	manifest := filepath.Join(root, "assets", "manifest.json")
	if len(state.Manifest) > 0 {
		if e := writeDurableJSON(manifest, state.Manifest); e != nil {
			return e
		}
	} else {
		if e := os.Remove(manifest); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if e := os.Remove(filepath.Join(root, "migration.json")); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return nil
}
