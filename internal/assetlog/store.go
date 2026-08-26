package assetlog

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

const (
	manifestName = "manifest.json"
	logName      = "information.jsonl"
)

type manifest struct {
	Format    string    `json:"format"`
	CreatedAt time.Time `json:"created_at"`
}

type backupManifest struct {
	Format    string            `json:"format"`
	CreatedAt time.Time         `json:"created_at"`
	Files     map[string]string `json:"sha256"`
}

type event struct {
	Operation string             `json:"operation"`
	Recorded  time.Time          `json:"recorded_at"`
	Value     domain.Information `json:"value"`
}

type Store struct {
	mu      sync.RWMutex
	dir     string
	logFile *os.File
	lock    *directoryLock
	items   map[string]domain.Information
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建信息资产目录: %w", err)
	}
	lock, err := acquireDirectoryLock(filepath.Join(dir, ".ownward.lock"))
	if err != nil {
		return nil, fmt.Errorf("锁定信息资产目录: %w", err)
	}
	releaseLock := true
	defer func() {
		if releaseLock {
			_ = lock.release()
		}
	}()
	if err := ensureManifest(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, logName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开信息资产日志: %w", err)
	}
	store := &Store{dir: dir, logFile: file, lock: lock, items: make(map[string]domain.Information)}
	if err := store.replay(); err != nil {
		_ = file.Close()
		return nil, err
	}
	releaseLock = false
	return store, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	if s.logFile != nil {
		first = s.logFile.Close()
		s.logFile = nil
	}
	if s.lock != nil {
		if err := s.lock.release(); first == nil {
			first = err
		}
		s.lock = nil
	}
	return first
}

func (s *Store) Create(value domain.Information) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[value.ID]; exists {
		return errors.New("信息标识已经存在")
	}
	if value.Revision != 1 {
		return errors.New("新信息必须从版本一开始")
	}
	return s.appendLocked("create", value)
}

func (s *Store) Update(value domain.Information, expectedRevision uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.items[value.ID]
	if !exists {
		return errors.New("信息不存在")
	}
	if current.Revision != expectedRevision {
		return fmt.Errorf("信息已被更新，当前版本为 %d", current.Revision)
	}
	if value.Revision != current.Revision+1 {
		return errors.New("信息版本不连续")
	}
	if value.CreatedAt != current.CreatedAt {
		return errors.New("信息创建时间不可变")
	}
	return s.appendLocked("update", value)
}

func (s *Store) Get(id string) (domain.Information, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.items[id]
	return clone(value), ok
}

func (s *Store) All() []domain.Information {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]domain.Information, 0, len(s.items))
	for _, value := range s.items {
		values = append(values, clone(value))
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.Before(values[j].CreatedAt)
	})
	return values
}

func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logFile == nil {
		return errors.New("信息资产日志已关闭")
	}
	return s.logFile.Sync()
}

// Compact replaces the append history with one durable snapshot event per
// current authoritative asset. Revision identity is preserved; obsolete full
// text versions remain recoverable through explicit backups rather than being
// retained forever in the active store.
func (s *Store) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logFile == nil {
		return errors.New("信息资产日志已关闭")
	}
	values := make([]domain.Information, 0, len(s.items))
	for _, value := range s.items {
		values = append(values, clone(value))
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	temporary, err := os.CreateTemp(s.dir, ".information-compacting-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	writer := bufio.NewWriterSize(temporary, 1024*1024)
	for _, value := range values {
		encoded, encodeErr := json.Marshal(event{Operation: "snapshot", Recorded: value.UpdatedAt.UTC(), Value: value})
		if encodeErr != nil {
			return encodeErr
		}
		if _, err := writer.Write(append(encoded, '\n')); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	path := filepath.Join(s.dir, logName)
	if err := s.logFile.Close(); err != nil {
		return err
	}
	s.logFile = nil
	if err := replaceLogFile(temporaryPath, path); err != nil {
		s.logFile, _ = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		return err
	}
	committed = true
	s.logFile, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	_, err = s.logFile.Seek(0, io.SeekEnd)
	return err
}

func (s *Store) Dir() string {
	return s.dir
}

func (s *Store) Backup(destination string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logFile == nil {
		return errors.New("信息资产日志已关闭")
	}
	if err := s.logFile.Sync(); err != nil {
		return fmt.Errorf("持久化信息资产: %w", err)
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("备份目标已经存在")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查备份目标: %w", err)
	}
	files := make(map[string][]byte, 2)
	for _, name := range []string{manifestName, logName} {
		content, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			return fmt.Errorf("读取备份内容 %s: %w", name, err)
		}
		files[name] = content
	}
	digests := make(map[string]string, len(files))
	for name, content := range files {
		digest := sha256.Sum256(content)
		digests[name] = fmt.Sprintf("%x", digest[:])
	}
	metadata, err := json.MarshalIndent(backupManifest{Format: "ownward.backup/v1", CreatedAt: time.Now().UTC(), Files: digests}, "", "  ")
	if err != nil {
		return err
	}
	metadata = append(metadata, '\n')
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("创建备份目录: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".ownward-backup-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时备份: %w", err)
	}
	temporaryName := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()
	archive := zip.NewWriter(temporary)
	for _, name := range []string{manifestName, logName} {
		writer, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		if _, err := writer.Write(files[name]); err != nil {
			return err
		}
	}
	writer, err := archive.CreateHeader(&zip.FileHeader{Name: "backup.json", Method: zip.Deflate})
	if err != nil {
		return err
	}
	if _, err := writer.Write(metadata); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("完成备份归档: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("持久化备份归档: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("提交备份归档: %w", err)
	}
	committed = true
	return nil
}

func Restore(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("打开备份归档: %w", err)
	}
	defer reader.Close()
	files := make(map[string][]byte, 3)
	for _, entry := range reader.File {
		if entry.Name != manifestName && entry.Name != logName && entry.Name != "backup.json" {
			return fmt.Errorf("备份归档包含未知文件 %q", entry.Name)
		}
		if entry.UncompressedSize64 > 16*1024*1024*1024 {
			return fmt.Errorf("备份文件 %q 超过允许大小", entry.Name)
		}
		if _, duplicate := files[entry.Name]; duplicate {
			return fmt.Errorf("备份归档重复包含文件 %q", entry.Name)
		}
		opened, err := entry.Open()
		if err != nil {
			return err
		}
		content, readErr := io.ReadAll(io.LimitReader(opened, int64(entry.UncompressedSize64)+1))
		closeErr := opened.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if uint64(len(content)) != entry.UncompressedSize64 {
			return fmt.Errorf("备份文件 %q 长度不匹配", entry.Name)
		}
		files[entry.Name] = content
	}
	if len(files) != 3 {
		return errors.New("备份归档不完整")
	}
	var metadata backupManifest
	if err := json.Unmarshal(files["backup.json"], &metadata); err != nil {
		return fmt.Errorf("解析备份清单: %w", err)
	}
	if metadata.Format != "ownward.backup/v1" {
		return errors.New("不支持的备份格式")
	}
	for _, name := range []string{manifestName, logName} {
		digest := sha256.Sum256(files[name])
		if !strings.EqualFold(metadata.Files[name], fmt.Sprintf("%x", digest[:])) {
			return fmt.Errorf("备份文件 %q 校验失败", name)
		}
	}
	if entries, err := os.ReadDir(destination); err == nil {
		if len(entries) > 0 {
			return errors.New("恢复目标不是空白目录")
		}
		if err := os.Remove(destination); err != nil {
			return fmt.Errorf("准备恢复目标: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查恢复目标: %w", err)
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, ".ownward-restore-*")
	if err != nil {
		return fmt.Errorf("创建临时恢复目录: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	for _, name := range []string{manifestName, logName} {
		if err := os.WriteFile(filepath.Join(temporary, name), files[name], 0o600); err != nil {
			return fmt.Errorf("恢复文件 %s: %w", name, err)
		}
	}
	if err := validateRestored(temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return fmt.Errorf("提交恢复结果: %w", err)
	}
	committed = true
	return nil
}

func validateRestored(dir string) error {
	store, err := Open(dir)
	if err != nil {
		return fmt.Errorf("验证恢复结果: %w", err)
	}
	return store.Close()
}

func (s *Store) appendLocked(operation string, value domain.Information) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if s.logFile == nil {
		return errors.New("信息资产日志已关闭")
	}
	encoded, err := json.Marshal(event{Operation: operation, Recorded: time.Now().UTC(), Value: value})
	if err != nil {
		return fmt.Errorf("编码信息资产: %w", err)
	}
	encoded = append(encoded, '\n')
	start, err := s.logFile.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("定位信息资产日志: %w", err)
	}
	written, err := s.logFile.Write(encoded)
	if err != nil || written != len(encoded) {
		rollbackErr := s.rollbackLocked(start)
		if err == nil {
			err = io.ErrShortWrite
		}
		if rollbackErr != nil {
			return fmt.Errorf("写入信息资产失败且回滚失败: %v; %w", rollbackErr, err)
		}
		return fmt.Errorf("写入信息资产: %w", err)
	}
	if err := s.logFile.Sync(); err != nil {
		if rollbackErr := s.rollbackLocked(start); rollbackErr != nil {
			return fmt.Errorf("持久化信息资产失败且回滚失败: %v; %w", rollbackErr, err)
		}
		return fmt.Errorf("持久化信息资产: %w", err)
	}
	s.items[value.ID] = clone(value)
	return nil
}

func (s *Store) replay() error {
	if _, err := s.logFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("读取信息资产日志: %w", err)
	}
	reader := bufio.NewReaderSize(s.logFile, 64*1024)
	line := 0
	committedEnd := int64(0)
	for {
		encoded, readErr := reader.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			if len(encoded) > 0 {
				if err := s.logFile.Truncate(committedEnd); err != nil {
					return fmt.Errorf("清理未提交的信息资产尾部: %w", err)
				}
			}
			break
		}
		if readErr != nil {
			return fmt.Errorf("读取信息资产日志: %w", readErr)
		}
		line++
		var entry event
		if err := json.Unmarshal(encoded, &entry); err != nil {
			return fmt.Errorf("信息资产日志第 %d 行损坏: %w", line, err)
		}
		if err := entry.Value.Validate(); err != nil {
			return fmt.Errorf("信息资产日志第 %d 行无效: %w", line, err)
		}
		current, exists := s.items[entry.Value.ID]
		switch entry.Operation {
		case "create":
			if exists || entry.Value.Revision != 1 {
				return fmt.Errorf("信息资产日志第 %d 行创建事件无效", line)
			}
		case "update":
			if !exists || entry.Value.Revision != current.Revision+1 || entry.Value.CreatedAt != current.CreatedAt {
				return fmt.Errorf("信息资产日志第 %d 行更新事件无效", line)
			}
		case "snapshot":
			if exists {
				return fmt.Errorf("信息资产日志第 %d 行快照事件重复", line)
			}
		default:
			return fmt.Errorf("信息资产日志第 %d 行操作未知", line)
		}
		s.items[entry.Value.ID] = clone(entry.Value)
		committedEnd += int64(len(encoded))
	}
	_, err := s.logFile.Seek(0, io.SeekEnd)
	return err
}

func (s *Store) rollbackLocked(offset int64) error {
	if err := s.logFile.Truncate(offset); err != nil {
		return err
	}
	if _, err := s.logFile.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	return s.logFile.Sync()
}

func ensureManifest(dir string) error {
	path := filepath.Join(dir, manifestName)
	data, err := os.ReadFile(path)
	if err == nil {
		var value manifest
		if err := json.Unmarshal(data, &value); err != nil {
			return fmt.Errorf("解析信息资产清单: %w", err)
		}
		if value.Format != domain.AssetSchema {
			return errors.New("不支持的信息资产清单格式")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取信息资产清单: %w", err)
	}
	data, err = json.MarshalIndent(manifest{Format: domain.AssetSchema, CreatedAt: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return fmt.Errorf("写入信息资产清单: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("提交信息资产清单: %w", err)
	}
	return nil
}

func clone(value domain.Information) domain.Information {
	value.Contexts = append([]domain.Context(nil), value.Contexts...)
	value.Relations = append([]domain.ExplicitRelation(nil), value.Relations...)
	return value
}
