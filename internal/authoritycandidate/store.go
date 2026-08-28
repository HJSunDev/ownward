//go:build ownward_migration

// Package authoritycandidate is the minimal isolated persistence adapter used
// to prove the authority-store replacement lifecycle. It is deliberately not
// linked into the normal product binary. Unlike assetlog it uses a distinct
// manifest and event envelope, so the lifecycle exercises a real storage
// implementation boundary rather than copying one directory in place.
package authoritycandidate

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

const (
	Format         = "ownward.authority-candidate-log/v1"
	manifestName   = "candidate-manifest.json"
	logName        = "authority-events.jsonl"
	backupSchema   = "ownward.authority-candidate-backup/v1"
	backupFileName = "candidate-backup.json"
	controlName    = "control.json"
)

type manifest struct {
	Schema    string    `json:"schema"`
	CreatedAt time.Time `json:"created_at"`
}

type event struct {
	Schema    string             `json:"schema"`
	Operation string             `json:"operation"`
	Recorded  time.Time          `json:"recorded_at"`
	Value     domain.Information `json:"value"`
}

type backupManifest struct {
	Schema string            `json:"schema"`
	Files  map[string]string `json:"sha256"`
}

// Store is a distinct, durable append-only candidate implementation. It is
// intentionally small, but it enforces the same public authority semantics.
type Store struct {
	mu      sync.RWMutex
	dir     string
	logFile *os.File
	lock    *directoryLock
	items   map[string]domain.Information
	history []domain.Information
}

var _ contract.AssetAuthority = (*Store)(nil)

func Open(dir string) (*Store, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("候选权威存储目录必须是绝对路径")
	}
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := acquireDirectoryLock(filepath.Join(dir, ".ownward-candidate.lock"))
	if err != nil {
		return nil, err
	}
	release := true
	defer func() {
		if release {
			_ = lock.release()
		}
	}()
	if err := ensureManifest(dir); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, logName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	store := &Store{dir: dir, logFile: file, lock: lock, items: make(map[string]domain.Information)}
	if err := store.replay(); err != nil {
		_ = file.Close()
		return nil, err
	}
	release = false
	return store, nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
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

func (s *Store) CreateAsset(value domain.Information) (contract.ChangeScope, error) {
	if err := s.apply([]domain.Information{value}, true); err != nil {
		return contract.ChangeScope{}, err
	}
	return changeScope([]domain.Information{value})
}

func (s *Store) CreateAssets(values []domain.Information) (contract.ChangeScope, error) {
	if len(values) == 0 || len(values) > 20 {
		return contract.ChangeScope{}, errors.New("批量创建数量必须介于一和二十之间")
	}
	if err := s.apply(values, true); err != nil {
		return contract.ChangeScope{}, err
	}
	return changeScope(values)
}

func (s *Store) UpdateAsset(value domain.Information, expected uint64) (contract.ChangeScope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.items[value.ID]
	if !exists {
		return contract.ChangeScope{}, errors.New("信息不存在")
	}
	if current.Revision != expected {
		return contract.ChangeScope{}, fmt.Errorf("信息已被更新，当前版本为 %d", current.Revision)
	}
	if value.Revision != expected+1 || value.CreatedAt != current.CreatedAt {
		return contract.ChangeScope{}, errors.New("信息版本或创建时间无效")
	}
	if err := s.appendLocked([]domain.Information{value}, "update"); err != nil {
		return contract.ChangeScope{}, err
	}
	s.items[value.ID] = clone(value)
	s.history = append(s.history, clone(value))
	return changeScope([]domain.Information{value})
}

func (s *Store) ReadCurrent(id string) (domain.Information, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.items[id]
	return clone(value), ok
}

func (s *Store) ReadVersion(id string, revision uint64) (domain.Information, bool) {
	value, ok := s.ReadCurrent(id)
	return value, ok && value.Revision == revision
}

func (s *Store) ListCurrent() []domain.Information {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedValues(s.items)
}

func (s *Store) CaptureCurrentForMigration() []domain.Information {
	s.mu.RLock()
	values := make([]domain.Information, 0, len(s.items))
	for _, value := range s.items {
		values = append(values, value)
	}
	s.mu.RUnlock()
	for index := range values {
		values[index] = clone(values[index])
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values
}

func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logFile == nil {
		return errors.New("候选权威日志已关闭")
	}
	return s.logFile.Sync()
}

// Compact is intentionally a no-op: the proof adapter remains append-only.
func (s *Store) Compact() error { return s.Sync() }

func (s *Store) Backup(destination string) error {
	return errors.New("候选权威备份必须由绑定控制状态的权威基座执行")
}

// Seed establishes one independent baseline. It cannot overwrite an existing
// candidate and therefore cannot silently replace accepted candidate work.
func (s *Store) Seed(values []domain.Information) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) != 0 {
		if equalAssets(sortedValues(s.items), values) {
			return nil
		}
		return errors.New("候选权威存储已经包含不同基线")
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.Revision == 0 || value.Validate() != nil {
			return errors.New("候选基线包含无效资产")
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return errors.New("候选基线包含重复资产")
		}
		seen[value.ID] = struct{}{}
	}
	if err := s.appendLocked(values, "seed"); err != nil {
		return err
	}
	for _, value := range values {
		s.items[value.ID] = clone(value)
		s.history = append(s.history, clone(value))
	}
	return nil
}

// ApplyChanges appends only versions newer than the candidate snapshot.
func (s *Store) ApplyChanges(values []domain.Information) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changes := make([]domain.Information, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return err
		}
		current, exists := s.items[value.ID]
		if exists && current.Revision == value.Revision {
			if !equalAsset(current, value) {
				return errors.New("同一资产版本内容不一致")
			}
			continue
		}
		if exists && value.Revision <= current.Revision {
			return errors.New("候选追平资产版本没有前进")
		}
		if !exists && value.Revision != 1 {
			return errors.New("候选追平缺少资产初始版本")
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return errors.New("候选追平包含重复资产")
		}
		seen[value.ID] = struct{}{}
		changes = append(changes, value)
	}
	if len(changes) == 0 {
		return nil
	}
	if err := s.appendLocked(changes, "catch-up"); err != nil {
		return err
	}
	for _, value := range changes {
		s.items[value.ID] = clone(value)
		s.history = append(s.history, clone(value))
	}
	return nil
}

// ChangesSince returns the exact accepted version sequence after one frozen
// version set. It is migration evidence, not a second public asset history.
func (s *Store) ChangesSince(versions []contract.AssetVersion) ([]domain.Information, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	baseline := make(map[string]uint64, len(versions))
	for _, version := range versions {
		if version.ID == "" || version.Revision == 0 {
			return nil, errors.New("变化基线版本无效")
		}
		baseline[version.ID] = version.Revision
	}
	changes := make([]domain.Information, 0)
	for _, value := range s.history {
		if value.Revision > baseline[value.ID] {
			changes = append(changes, clone(value))
		}
	}
	return changes, nil
}

func (s *Store) BackupAuthority(destination string, control contract.ControlState) error {
	if !filepath.IsAbs(destination) {
		return errors.New("候选权威备份路径必须是绝对路径")
	}
	if control.Validate() != nil {
		return errors.New("候选权威备份控制状态无效")
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("候选权威备份已经存在")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".candidate-backup-*.tmp")
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
	s.mu.Lock()
	if s.logFile == nil {
		s.mu.Unlock()
		return errors.New("候选权威日志已关闭")
	}
	if err := s.logFile.Sync(); err != nil {
		s.mu.Unlock()
		return err
	}
	archive := zip.NewWriter(temporary)
	files := make(map[string]string, 3)
	for _, name := range []string{manifestName, logName} {
		digest, streamErr := streamFileToZip(archive, name, filepath.Join(s.dir, name))
		if streamErr != nil {
			s.mu.Unlock()
			_ = archive.Close()
			return streamErr
		}
		files[name] = digest
	}
	s.mu.Unlock()
	controlBytes, err := json.Marshal(control)
	if err != nil {
		_ = archive.Close()
		return err
	}
	controlWriter, err := archive.CreateHeader(&zip.FileHeader{Name: controlName, Method: zip.Deflate})
	if err != nil {
		_ = archive.Close()
		return err
	}
	if _, err := controlWriter.Write(controlBytes); err != nil {
		_ = archive.Close()
		return err
	}
	controlDigest := sha256.Sum256(controlBytes)
	files[controlName] = hex.EncodeToString(controlDigest[:])
	metadata, err := json.MarshalIndent(backupManifest{Schema: backupSchema, Files: files}, "", "  ")
	if err != nil {
		_ = archive.Close()
		return err
	}
	metadataWriter, err := archive.CreateHeader(&zip.FileHeader{Name: backupFileName, Method: zip.Deflate})
	if err != nil {
		_ = archive.Close()
		return err
	}
	if _, err := metadataWriter.Write(append(metadata, '\n')); err != nil {
		_ = archive.Close()
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	committed = true
	return nil
}

func RestoreAuthority(backupPath, destination string) (contract.ControlState, error) {
	if !filepath.IsAbs(backupPath) || !filepath.IsAbs(destination) {
		return contract.ControlState{}, errors.New("候选权威恢复路径必须是绝对路径")
	}
	reader, err := zip.OpenReader(backupPath)
	if err != nil {
		return contract.ControlState{}, err
	}
	defer reader.Close()
	entries := make(map[string]*zip.File, 4)
	for _, entry := range reader.File {
		if entry.Name != manifestName && entry.Name != logName && entry.Name != controlName && entry.Name != backupFileName {
			return contract.ControlState{}, fmt.Errorf("候选权威备份包含未知文件 %q", entry.Name)
		}
		if entries[entry.Name] != nil {
			return contract.ControlState{}, errors.New("候选权威备份包含重复文件")
		}
		entries[entry.Name] = entry
	}
	if len(entries) != 4 {
		return contract.ControlState{}, errors.New("候选权威备份不完整")
	}
	metadataBytes, err := readSmallZip(entries[backupFileName], 1024*1024)
	if err != nil {
		return contract.ControlState{}, err
	}
	var metadata backupManifest
	if json.Unmarshal(metadataBytes, &metadata) != nil || metadata.Schema != backupSchema || len(metadata.Files) != 3 {
		return contract.ControlState{}, errors.New("候选权威备份清单无效")
	}
	controlBytes, err := readSmallZip(entries[controlName], 1024*1024)
	if err != nil {
		return contract.ControlState{}, err
	}
	controlDigest := sha256.Sum256(controlBytes)
	var control contract.ControlState
	if metadata.Files[controlName] != hex.EncodeToString(controlDigest[:]) || json.Unmarshal(controlBytes, &control) != nil || control.Validate() != nil {
		return contract.ControlState{}, errors.New("候选权威备份控制状态无效")
	}
	if entries, err := os.ReadDir(destination); err == nil && len(entries) != 0 {
		return contract.ControlState{}, errors.New("候选权威恢复目标不是空目录")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return contract.ControlState{}, err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return contract.ControlState{}, err
	}
	staged, err := os.MkdirTemp(parent, ".candidate-restore-*")
	if err != nil {
		return contract.ControlState{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staged)
		}
	}()
	for _, name := range []string{manifestName, logName} {
		digest, err := streamZipToFile(entries[name], filepath.Join(staged, name))
		if err != nil {
			return contract.ControlState{}, err
		}
		if !strings.EqualFold(metadata.Files[name], digest) {
			return contract.ControlState{}, fmt.Errorf("候选权威备份文件 %s 校验失败", name)
		}
	}
	store, err := Open(staged)
	if err != nil {
		return contract.ControlState{}, err
	}
	closeErr := store.Close()
	if closeErr != nil {
		return contract.ControlState{}, closeErr
	}
	if entries, err := os.ReadDir(destination); err == nil {
		if len(entries) != 0 {
			return contract.ControlState{}, errors.New("候选权威恢复目标在验证期间发生变化")
		}
		if err := os.Remove(destination); err != nil {
			return contract.ControlState{}, err
		}
	}
	if err := os.Rename(staged, destination); err != nil {
		return contract.ControlState{}, err
	}
	committed = true
	return control, nil
}

func streamFileToZip(archive *zip.Writer, name, path string) (string, error) {
	source, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer source.Close()
	destination, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	buffer := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(destination, io.TeeReader(source, hasher), buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func streamZipToFile(entry *zip.File, path string) (string, error) {
	opened, err := entry.Open()
	if err != nil {
		return "", err
	}
	defer opened.Close()
	destination, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	buffer := make([]byte, 64*1024)
	_, copyErr := io.CopyBuffer(io.MultiWriter(destination, hasher), opened, buffer)
	syncErr := destination.Sync()
	closeErr := destination.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func readSmallZip(entry *zip.File, limit int64) ([]byte, error) {
	if entry == nil || entry.UncompressedSize64 > uint64(limit) {
		return nil, errors.New("候选权威备份元数据缺失或过大")
	}
	opened, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	content, err := io.ReadAll(io.LimitReader(opened, limit+1))
	if err != nil || int64(len(content)) > limit || uint64(len(content)) != entry.UncompressedSize64 {
		return nil, errors.New("候选权威备份元数据长度无效")
	}
	return content, nil
}

func (s *Store) apply(values []domain.Information, creating bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return errors.New("批量创建包含重复信息标识")
		}
		seen[value.ID] = struct{}{}
		if _, exists := s.items[value.ID]; exists || !creating || value.Revision != 1 {
			return errors.New("候选创建资产状态无效")
		}
	}
	if err := s.appendLocked(values, "create"); err != nil {
		return err
	}
	for _, value := range values {
		s.items[value.ID] = clone(value)
		s.history = append(s.history, clone(value))
	}
	return nil
}

func (s *Store) appendLocked(values []domain.Information, operation string) error {
	if s.logFile == nil {
		return errors.New("候选权威日志已关闭")
	}
	start, err := s.logFile.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	for _, value := range values {
		encoded, err := json.Marshal(event{Schema: Format, Operation: operation, Recorded: time.Now().UTC(), Value: value})
		if err != nil {
			_ = s.rollback(start)
			return err
		}
		if _, err := s.logFile.Write(append(encoded, '\n')); err != nil {
			_ = s.rollback(start)
			return err
		}
	}
	if err := s.logFile.Sync(); err != nil {
		_ = s.rollback(start)
		return err
	}
	return nil
}

func (s *Store) rollback(offset int64) error {
	if err := s.logFile.Truncate(offset); err != nil {
		return err
	}
	_, err := s.logFile.Seek(0, io.SeekEnd)
	return err
}

func (s *Store) replay() error {
	if _, err := s.logFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(s.logFile, 64*1024)
	committed := int64(0)
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) != 0 {
				if truncateErr := s.logFile.Truncate(committed); truncateErr != nil {
					return truncateErr
				}
			}
			break
		}
		if err != nil {
			return err
		}
		var entry event
		if json.Unmarshal(line, &entry) != nil || entry.Schema != Format || entry.Value.Validate() != nil {
			return errors.New("候选权威日志损坏")
		}
		current, exists := s.items[entry.Value.ID]
		strictStep := entry.Operation == "create" || entry.Operation == "update"
		if (exists && (entry.Value.Revision <= current.Revision || strictStep && entry.Value.Revision != current.Revision+1)) || (!exists && entry.Value.Revision == 0) {
			return errors.New("候选权威日志版本不连续")
		}
		if exists && entry.Value.CreatedAt != current.CreatedAt {
			return errors.New("候选权威日志更改了创建时间")
		}
		s.items[entry.Value.ID] = clone(entry.Value)
		s.history = append(s.history, clone(entry.Value))
		committed += int64(len(line))
	}
	_, err := s.logFile.Seek(0, io.SeekEnd)
	return err
}

func ensureManifest(dir string) error {
	path := filepath.Join(dir, manifestName)
	encoded, err := os.ReadFile(path)
	if err == nil {
		var value manifest
		if json.Unmarshal(encoded, &value) != nil || value.Schema != Format {
			return errors.New("候选权威清单无效")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	encoded, err = json.MarshalIndent(manifest{Schema: Format, CreatedAt: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	return publishFile(path, append(encoded, '\n'))
}

func publishFile(path string, encoded []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".candidate-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func changeScope(values []domain.Information) (contract.ChangeScope, error) {
	scope := contract.ChangeScope{Schema: contract.AssetChangeScopeSchema, Assets: make([]contract.AssetVersion, len(values))}
	for index, value := range values {
		scope.Assets[index] = contract.AssetVersion{ID: value.ID, Revision: value.Revision}
	}
	return scope, scope.Validate()
}

func sortedValues(items map[string]domain.Information) []domain.Information {
	values := make([]domain.Information, 0, len(items))
	for _, value := range items {
		values = append(values, clone(value))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values
}

func clone(value domain.Information) domain.Information {
	value.Contexts = append([]domain.Context(nil), value.Contexts...)
	value.Relations = append([]domain.ExplicitRelation(nil), value.Relations...)
	return value
}

func equalAsset(left, right domain.Information) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func equalAssets(left, right []domain.Information) bool {
	if len(left) != len(right) {
		return false
	}
	rightMap := make(map[string]domain.Information, len(right))
	for _, value := range right {
		rightMap[value.ID] = value
	}
	for _, value := range left {
		other, ok := rightMap[value.ID]
		if !ok || !equalAsset(value, other) {
			return false
		}
	}
	return true
}
