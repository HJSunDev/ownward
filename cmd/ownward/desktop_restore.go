package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/HJSunDev/ownward/internal/assembly"
	"github.com/HJSunDev/ownward/internal/desktop"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

type restoredEntry struct {
	ID      string    `json:"id"`
	Source  string    `json:"source"`
	System  string    `json:"system"`
	Created time.Time `json:"created"`
	Ready   bool      `json:"ready"`
	Opened  bool      `json:"opened"`
	Failed  bool      `json:"failed,omitempty"`
}

func restoredPath(dataDir, id string) (string, error) {
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 24 {
		return "", errors.New("恢复结果无效")
	}
	return filepath.Join(filepath.Dir(dataDir), filepath.Base(dataDir)+"-restored-"+id), nil
}
func restoreRecordPath(dataDir, id string) (string, error) {
	if _, err := restoredPath(dataDir, id); err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "runtime", "owner-restores", id+".json"), nil
}
func saveRestoreRecord(dataDir string, r restoredEntry) error {
	path, err := restoreRecordPath(dataDir, r.ID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return desktop.AtomicWrite(path, b, 0600)
}
func loadRestoreRecord(dataDir, id string) (restoredEntry, error) {
	path, err := restoreRecordPath(dataDir, id)
	if err != nil {
		return restoredEntry{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return restoredEntry{}, err
	}
	defer f.Close()
	var r restoredEntry
	err = json.NewDecoder(io.LimitReader(f, 4096)).Decode(&r)
	if err == nil && r.ID != id {
		err = errors.New("恢复结果身份不匹配")
	}
	return r, err
}

const restoreHistoryLimit = 64

func lockRestoreResults(dataDir string) (*serviceStartupLock, error) {
	dir := filepath.Join(dataDir, "runtime")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return acquireServiceStartupLock(filepath.Join(dir, "owner-restores.lock"), 10*time.Second)
}

func restoreEntries(dataDir, source string) ([]restoredEntry, error) {
	f, err := os.Open(filepath.Join(dataDir, "runtime", "owner-restores"))
	if errors.Is(err, os.ErrNotExist) {
		return []restoredEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	files, err := f.Readdirnames(restoreHistoryLimit + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(files) > restoreHistoryLimit {
		return nil, errors.New("恢复记录需要整理，请保留现有恢复资料")
	}
	result := []restoredEntry{}
	for _, name := range files {
		if filepath.Ext(name) != ".json" {
			continue
		}
		r, err := loadRestoreRecord(dataDir, name[:len(name)-5])
		if err != nil {
			return nil, err
		}
		if r.Source == source {
			result = append(result, r)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Created.After(result[j].Created) })
	return result, nil
}

// 最后一步回执写入中断时，从已经完整落盘的恢复目标核对完成状态。
func reconcileRestore(ctx context.Context, dataDir string, r restoredEntry) (restoredEntry, error) {
	if r.Ready {
		return r, nil
	}
	target, err := restoredPath(dataDir, r.ID)
	if err != nil {
		return r, err
	}
	state, err := assembly.ReadLocalIdentity(ctx, target)
	if err != nil || state.InformationControl == nil || state.InformationControl.SystemID == "" {
		return r, nil
	}
	r.Ready, r.Failed, r.System = true, false, state.InformationControl.SystemID
	return r, saveRestoreRecord(dataDir, r)
}

func reserveRestore(dataDir string, record restoredEntry) error {
	entries, err := restoreEntries(dataDir, record.Source)
	if err != nil {
		return err
	}
	for _, r := range entries {
		if r.ID == record.ID {
			return nil
		}
	}
	if len(entries) >= restoreHistoryLimit {
		// 只老化位置记录，不删除恢复出的用户资料。
		old := entries[len(entries)-1]
		path, err := restoreRecordPath(dataDir, old.ID)
		if err != nil {
			return err
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	}
	return saveRestoreRecord(dataDir, record)
}

func resumeRestore(ctx context.Context, control *informationcontrol.Control, dataDir, id, action string, expected uint64) (any, error) {
	path, err := desktop.StatePath()
	if err != nil {
		return nil, err
	}
	return resumeRestoreAt(ctx, control, dataDir, id, action, expected, path, func(ctx context.Context, target string) error {
		bundle, err := currentVectorBundleDirectory()
		if err != nil {
			return err
		}
		return openOwnerWindow(ctx, target, bundle, io.Discard, io.Discard)
	})
}

func resumeRestoreAt(ctx context.Context, control *informationcontrol.Control, dataDir, id, action string, expected uint64, settingsPath string, open func(context.Context, string) error) (any, error) {
	if _, err := control.Owner(ctx); err != nil {
		return nil, err
	}
	if action == "list" {
		lock, err := lockRestoreResults(dataDir)
		if err != nil {
			return nil, err
		}
		defer lock.release()
		entries, err := restoreEntries(dataDir, control.SystemID())
		if err != nil {
			return nil, err
		}
		for i, r := range entries {
			entries[i], err = reconcileRestore(ctx, dataDir, r)
			if err != nil {
				return nil, err
			}
		}
		return entries, nil
	}
	r, err := loadRestoreRecord(dataDir, id)
	if err != nil {
		return nil, err
	}
	if r.Source != control.SystemID() || !r.Ready {
		return nil, errors.New("恢复结果尚未就绪")
	}
	target, err := restoredPath(dataDir, id)
	if err != nil {
		return nil, err
	}
	state, err := assembly.ReadLocalIdentity(ctx, target)
	if err != nil || state.InformationControl == nil || state.InformationControl.SystemID != r.System {
		return nil, errors.New("恢复资料暂不可用，原资料保持不变")
	}
	if action == "open" {
		if _, err = control.Owner(ctx); err != nil {
			return nil, err
		}
		if err = open(ctx, target); err != nil {
			return nil, err
		}
		lock, err := lockRestoreResults(dataDir)
		if err != nil {
			return nil, err
		}
		defer lock.release()
		// 打开期间已老化的引用不得被迟到结果重新写回。
		r, err = loadRestoreRecord(dataDir, id)
		if err != nil {
			return map[string]any{"state": "opened", "default_available": false}, nil
		}
		r.Opened = true
		if err = saveRestoreRecord(dataDir, r); err != nil {
			return nil, err
		}
		settings, e := desktop.Load(settingsPath)
		available := e == nil && settings.Binding != nil && sameDataDirectory(settings.Binding.Data, dataDir) && settings.Binding.System == r.Source
		return map[string]any{"state": "opened", "default_available": available, "default_revision": settings.Revision}, nil
	}
	if action != "default" {
		return nil, errors.New("恢复操作无效")
	}
	lock, err := acquireServiceStartupLock(settingsPath+".lock", 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer lock.release()
	settings, err := desktop.Load(settingsPath)
	if err != nil {
		return nil, err
	}
	if !r.Opened || settings.Revision != expected || settings.Binding == nil || !sameDataDirectory(settings.Binding.Data, dataDir) || settings.Binding.System != r.Source {
		return nil, errors.New("日常资料已经变化，请从当前日常入口重新核对")
	}
	settings.Binding = &desktop.Binding{System: r.System, Data: target}
	settings.Revision++
	if _, err = control.Owner(ctx); err != nil {
		return nil, err
	}
	if err = desktop.Save(settingsPath, settings); err != nil {
		return nil, err
	}
	return map[string]string{"state": "default_updated"}, nil
}
