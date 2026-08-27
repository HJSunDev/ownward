package assetlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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

// CreateBatch durably appends one public create batch with a single storage
// barrier. The batch is bounded by the public service contract; acknowledged
// records are all durable before the method returns. A failed write is rolled
// back to the pre-batch log boundary.
func (s *Store) CreateBatch(values []domain.Information) error {
	if len(values) == 0 || len(values) > 20 {
		return errors.New("批量创建数量必须介于一和二十之间")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logFile == nil {
		return errors.New("信息资产日志已关闭")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return err
		}
		if value.Revision != 1 {
			return errors.New("新信息必须从版本一开始")
		}
		if _, exists := s.items[value.ID]; exists {
			return errors.New("信息标识已经存在")
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return errors.New("批量创建包含重复信息标识")
		}
		seen[value.ID] = struct{}{}
	}
	start, err := s.logFile.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("定位信息资产日志: %w", err)
	}
	for _, value := range values {
		encoded, encodeErr := json.Marshal(event{Operation: "create", Recorded: time.Now().UTC(), Value: value})
		if encodeErr != nil {
			_ = s.rollbackLocked(start)
			return fmt.Errorf("编码信息资产: %w", encodeErr)
		}
		encoded = append(encoded, '\n')
		written, writeErr := s.logFile.Write(encoded)
		if writeErr != nil || written != len(encoded) {
			rollbackErr := s.rollbackLocked(start)
			if writeErr == nil {
				writeErr = io.ErrShortWrite
			}
			if rollbackErr != nil {
				return fmt.Errorf("批量写入信息资产失败且回滚失败: %v; %w", rollbackErr, writeErr)
			}
			return fmt.Errorf("批量写入信息资产: %w", writeErr)
		}
	}
	if err := s.logFile.Sync(); err != nil {
		if rollbackErr := s.rollbackLocked(start); rollbackErr != nil {
			return fmt.Errorf("批量持久化信息资产失败且回滚失败: %v; %w", rollbackErr, err)
		}
		return fmt.Errorf("批量持久化信息资产: %w", err)
	}
	for _, value := range values {
		s.items[value.ID] = clone(value)
	}
	return nil
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
	return backupStreaming(s, destination)
}

func Restore(archivePath, destination string) error {
	return restoreStreaming(archivePath, destination)
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
