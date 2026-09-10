package authoritysubstrate

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/contract"
)

func ReadControlAt(dataDir string) (contract.ControlState, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, controlDirectory, controlFile))
	if err != nil {
		return contract.ControlState{}, err
	}
	return decodeControl(data)
}

// StageHandoff does not restore old credentials or activate a second authority.
// Its caller authenticates the source and the complete archive digest first.
func StageHandoff(archivePath, destination string) (contract.ControlState, error) {
	var zero contract.ControlState
	if !filepath.IsAbs(destination) {
		return zero, errors.New("目的地必须是绝对路径")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		return zero, errors.New("迁移接收目录须为空且尚未建立")
	}
	if err := os.MkdirAll(destination, 0700); err != nil {
		return zero, err
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return zero, err
	}
	defer archive.Close()
	seen := map[string]bool{}
	authorityArchive := ""
	for _, entry := range archive.File {
		name := entry.Name
		if !filepath.IsLocal(filepath.FromSlash(name)) || strings.Contains(name, "\\") || entry.Mode()&os.ModeSymlink != 0 || seen[name] || entry.FileInfo().IsDir() {
			return zero, errors.New("迁移包包含无效路径")
		}
		seen[name] = true
		if name != "authority.zip" && !strings.HasPrefix(name, "state/") {
			return zero, errors.New("迁移包包含非交接文件")
		}
		path := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return zero, err
		}
		reader, err := entry.Open()
		if err != nil {
			return zero, err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			reader.Close()
			return zero, err
		}
		_, err = copyBounded(file, reader)
		readClose := reader.Close()
		if err == nil {
			err = readClose
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return zero, err
		}
		if closeErr != nil {
			return zero, closeErr
		}
		if name == "authority.zip" {
			authorityArchive = path
		}
	}
	if authorityArchive == "" {
		return zero, errors.New("迁移包缺少唯一权威")
	}
	control, err := restoreAuthorityArchive(authorityArchive, filepath.Dir(destination), destination)
	if err != nil {
		return zero, err
	}
	state, err := decodeControl(control)
	if err != nil {
		return zero, err
	}
	if state.Access == nil || state.Access.Handoff == nil || state.Access.Handoff.Phase != "frozen" {
		return zero, errors.New("迁移包不是冻结中的权威快照")
	}
	if err := os.MkdirAll(filepath.Join(destination, controlDirectory), 0700); err != nil {
		return zero, err
	}
	if err := writeSyncedFile(filepath.Join(destination, controlDirectory, controlFile), control); err != nil {
		return zero, err
	}
	if err := os.Remove(authorityArchive); err != nil {
		return zero, err
	}
	return state, nil
}

// ActivateHandoff is called only after the adapter verifies the source's signed
// retirement permit. The durable phase precedes opening any product capability.
func ActivateHandoff(dataDir string, permit contract.Handoff) error {
	s, err := ReadControlAt(dataDir)
	if err != nil {
		return err
	}
	if s.Access == nil || s.Access.Handoff == nil {
		return errors.New("目的地缺少交接状态")
	}
	h := s.Access.Handoff
	if h.Phase == "active" && h.ID == permit.ID && h.Snapshot == permit.Snapshot && h.Target == permit.Target {
		return nil
	}
	if h.Phase != "frozen" || permit.Phase != "retired" || h.ID != permit.ID || h.Target != permit.Target || permit.Revision != s.Revision+1 || !permit.LocationSaved || len(permit.Snapshot) != 64 {
		return errors.New("接管许可与候选不匹配")
	}
	next := permit
	next.Phase = "active"
	s.Access.Handoff = &next
	s.Revision = permit.Revision
	store, err := openControl(filepath.Join(dataDir, controlDirectory), s)
	if err != nil {
		return err
	}
	return store.write(s, false)
}

func WriteHandoffArchive(root string, destination io.Writer) error {
	archive := zip.NewWriter(destination)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("迁移快照包含非普通文件")
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		if name != "authority.zip" && !strings.HasPrefix(name, "state/") {
			return errors.New("迁移快照包含未声明文件")
		}
		writer, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = copyBounded(writer, f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	if err != nil {
		archive.Close()
		return err
	}
	return archive.Close()
}
