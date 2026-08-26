package derived

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HJSunDev/ownward/internal/semantics"
)

const (
	recordSchema         = "ownward.derived/v4"
	previousRecordSchema = "ownward.derived/v3"
	legacyRecordSchema   = "ownward.derived/v2"
	LogFileName          = "organization.binlog"
	legacyLogFileName    = "organization.jsonl"
	headerSize           = 16
	footerSize           = 4
)

var (
	recordMagic = [4]byte{'O', 'W', 'D', '3'}
	commitMagic = [4]byte{'D', 'O', 'N', 'E'}
)

var ErrStaleRecord = errors.New("派生状态版本早于当前版本")

type Record struct {
	Schema                string                       `json:"schema"`
	AssetID               string                       `json:"asset_id"`
	AssetRevision         uint64                       `json:"asset_revision"`
	GeneratedAt           time.Time                    `json:"generated_at"`
	Provider              string                       `json:"provider"`
	Status                string                       `json:"status"`
	Error                 string                       `json:"error,omitempty"`
	Analysis              semantics.Analysis           `json:"analysis"`
	SemanticWorkReference *semantics.WorkReference     `json:"semantic_work_reference,omitempty"`
	SemanticReceipt       *semantics.SubmissionReceipt `json:"semantic_receipt,omitempty"`
	// SemanticWork and SemanticResult are accepted only as in-memory/legacy
	// inputs. Store canonicalization replaces them with compact durable forms.
	SemanticWork   *semantics.Work       `json:"semantic_work,omitempty"`
	SemanticResult *semantics.Submission `json:"semantic_result,omitempty"`
	EmbeddingSpace string                `json:"embedding_space,omitempty"`
	Embedding      []float32             `json:"embedding,omitempty"`
}

type persistedRecord struct {
	Schema         string                `json:"schema"`
	AssetID        string                `json:"asset_id"`
	AssetRevision  uint64                `json:"asset_revision"`
	GeneratedAt    time.Time             `json:"generated_at"`
	Provider       string                `json:"provider"`
	Status         string                `json:"status"`
	Error          string                `json:"error,omitempty"`
	Analysis       semantics.Analysis    `json:"analysis"`
	SemanticWork   *semantics.Work       `json:"semantic_work,omitempty"`
	SemanticResult *semantics.Submission `json:"semantic_result,omitempty"`
	EmbeddingSpace string                `json:"embedding_space,omitempty"`
	Embedding      []byte                `json:"embedding_f32le,omitempty"`
}

type recordMetadata struct {
	Schema                string                       `json:"schema"`
	AssetID               string                       `json:"asset_id"`
	AssetRevision         uint64                       `json:"asset_revision"`
	GeneratedAt           time.Time                    `json:"generated_at"`
	Provider              string                       `json:"provider"`
	Status                string                       `json:"status"`
	Error                 string                       `json:"error,omitempty"`
	Analysis              semantics.Analysis           `json:"analysis"`
	SemanticWorkReference *semantics.WorkReference     `json:"semantic_work_reference,omitempty"`
	SemanticReceipt       *semantics.SubmissionReceipt `json:"semantic_receipt,omitempty"`
	EmbeddingSpace        string                       `json:"embedding_space,omitempty"`
}

type previousRecordMetadata struct {
	Schema         string                `json:"schema"`
	AssetID        string                `json:"asset_id"`
	AssetRevision  uint64                `json:"asset_revision"`
	GeneratedAt    time.Time             `json:"generated_at"`
	Provider       string                `json:"provider"`
	Status         string                `json:"status"`
	Error          string                `json:"error,omitempty"`
	Analysis       semantics.Analysis    `json:"analysis"`
	SemanticWork   *semantics.Work       `json:"semantic_work,omitempty"`
	SemanticResult *semantics.Submission `json:"semantic_result,omitempty"`
	EmbeddingSpace string                `json:"embedding_space,omitempty"`
}

type Store struct {
	mu                  sync.RWMutex
	file                *os.File
	records             map[string]recordReference
	startupRecords      map[string]Record
	recoveredCorruption bool
	root                string
	directory           string
	generation          string
	sealed              bool
	needsCompaction     bool
}

type recordReference struct {
	offset   int64
	length   uint32
	revision uint64
}

type legacyCorruptionError struct {
	err error
}

func (e *legacyCorruptionError) Error() string { return e.err.Error() }
func (e *legacyCorruptionError) Unwrap() error { return e.err }

func Open(dir string) (*Store, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析派生状态目录: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("创建派生状态目录: %w", err)
	}
	generation, activeDir, sealed, err := currentGeneration(root)
	if err != nil {
		return nil, err
	}
	store, err := openStore(root, generation, activeDir, sealed, !sealed)
	if err != nil {
		return nil, err
	}
	if sealed {
		if err := store.verifyGeneration(); err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	if store.needsCompaction && !sealed {
		if err := store.Compact(); err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("迁移并压实派生状态: %w", err)
		}
	}
	return store, nil
}

func openStore(root, generation, dir string, sealed, allowLegacyMigration bool) (*Store, error) {
	recoveredLegacy := false
	if allowLegacyMigration {
		if err := migrateLegacy(dir); err != nil {
			var corruption *legacyCorruptionError
			if !errors.As(err, &corruption) {
				return nil, fmt.Errorf("迁移旧派生状态: %w", err)
			}
			legacyPath := filepath.Join(dir, legacyLogFileName)
			corruptPath := fmt.Sprintf("%s.corrupt-%s", legacyPath, time.Now().UTC().Format("20060102T150405.000000000Z"))
			if renameErr := os.Rename(legacyPath, corruptPath); renameErr != nil {
				return nil, fmt.Errorf("旧派生状态损坏且无法隔离: %v; %w", renameErr, err)
			}
			recoveredLegacy = true
		}
	}
	path := filepath.Join(dir, LogFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开派生状态: %w", err)
	}
	store := &Store{
		file: file, records: make(map[string]recordReference),
		startupRecords: make(map[string]Record), recoveredCorruption: recoveredLegacy,
		root: root, directory: dir, generation: generation, sealed: sealed,
	}
	if err := store.replay(); err != nil && sealed {
		store.records = make(map[string]recordReference)
		store.startupRecords = make(map[string]Record)
		if recoverErr := store.recoverSealedTail(); recoverErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("恢复派生世代的有效基线: %v; %w", recoverErr, err)
		}
		if replayErr := store.replay(); replayErr != nil {
			_ = file.Close()
			return nil, replayErr
		}
		store.recoveredCorruption = true
	} else if err != nil {
		_ = file.Close()
		corruptPath := fmt.Sprintf("%s.corrupt-%s", path, time.Now().UTC().Format("20060102T150405.000000000Z"))
		if renameErr := os.Rename(path, corruptPath); renameErr != nil {
			return nil, fmt.Errorf("派生状态损坏且无法隔离: %v; %w", renameErr, err)
		}
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if createErr != nil {
			return nil, fmt.Errorf("重建空白派生状态: %w", createErr)
		}
		store.file = file
		store.records = make(map[string]recordReference)
		store.startupRecords = make(map[string]Record)
		store.recoveredCorruption = true
	}
	return store, nil
}

func (s *Store) recoverSealedTail() error {
	encoded, err := os.ReadFile(filepath.Join(s.directory, manifestFileName))
	if err != nil {
		return err
	}
	var manifest generationManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil || manifest.Schema != generationSchema || manifest.Generation != s.generation || manifest.SealedBytes < 0 || !validDigest(manifest.LogSHA256) {
		return errors.New("派生世代基线清单无效")
	}
	actual, err := fileDigestPrefix(filepath.Join(s.directory, LogFileName), manifest.SealedBytes)
	if err != nil || !strings.EqualFold(actual, manifest.LogSHA256) {
		return errors.New("派生世代已封存基线损坏")
	}
	if err := s.file.Truncate(manifest.SealedBytes); err != nil {
		return err
	}
	if err := s.file.Sync(); err != nil {
		return err
	}
	_, err = s.file.Seek(0, io.SeekStart)
	return err
}

func migrateLegacy(dir string) error {
	currentPath := filepath.Join(dir, LogFileName)
	if _, err := os.Stat(currentPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	legacyPath := filepath.Join(dir, legacyLogFileName)
	legacy, err := os.Open(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer legacy.Close()
	temporary, err := os.CreateTemp(dir, ".organization-migrating-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		return err
	}
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	reader := bufio.NewReaderSize(legacy, 64*1024)
	line := 0
	for {
		encoded, readErr := reader.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			if len(encoded) > 0 {
				return &legacyCorruptionError{err: fmt.Errorf("旧派生状态第 %d 行被截断", line+1)}
			}
			break
		}
		if readErr != nil {
			return readErr
		}
		line++
		var persisted persistedRecord
		if err := json.Unmarshal(encoded, &persisted); err != nil {
			return &legacyCorruptionError{err: fmt.Errorf("旧派生状态第 %d 行损坏: %w", line, err)}
		}
		record, err := fromPersisted(persisted)
		if err != nil || persisted.Schema != legacyRecordSchema || record.AssetID == "" || record.AssetRevision == 0 {
			return &legacyCorruptionError{err: fmt.Errorf("旧派生状态第 %d 行无效", line)}
		}
		encoded, err = EncodeRecord(record)
		if err != nil {
			return fmt.Errorf("迁移旧派生状态第 %d 行: %w", line, err)
		}
		if _, err := temporary.Write(encoded); err != nil {
			return err
		}
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, currentPath); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

func (s *Store) Put(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	record, err = canonicalRecord(record)
	if err != nil {
		return err
	}
	if err := validateRecord(record); err != nil {
		return err
	}
	if current, exists := s.records[record.AssetID]; exists && current.revision > record.AssetRevision {
		return ErrStaleRecord
	}
	record.Schema = recordSchema
	encoded, err := encodeRecord(record)
	if err != nil {
		return err
	}
	if s.file == nil {
		return errors.New("派生状态已关闭")
	}
	start, err := s.file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("定位派生状态: %w", err)
	}
	written, err := s.file.Write(encoded)
	if err != nil || written != len(encoded) {
		_ = s.rollbackLocked(start)
		if err == nil {
			err = io.ErrShortWrite
		}
		return fmt.Errorf("写入派生状态: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		_ = s.rollbackLocked(start)
		return fmt.Errorf("持久化派生状态: %w", err)
	}
	s.records[record.AssetID] = recordReference{offset: start, length: uint32(len(encoded)), revision: record.AssetRevision}
	if s.startupRecords != nil {
		s.startupRecords[record.AssetID] = record
	}
	return nil
}

// PutBatch appends one bounded state batch with a single durability barrier.
// Records retain the exact on-disk representation and replay semantics used by
// Put; only the redundant per-record fsync is removed. A failed batch is
// rolled back to its original log boundary before any in-memory index changes.
func (s *Store) PutBatch(records []Record) error {
	if len(records) == 0 || len(records) > 20 {
		return errors.New("批量派生状态数量必须介于一和二十之间")
	}
	return s.putRecords(records, true, false)
}

func (s *Store) putRecords(records []Record, durable, requireEmptyGeneration bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return errors.New("派生状态已关闭")
	}
	if requireEmptyGeneration && (s.sealed || s.generation == "legacy" || len(s.records) != 0) {
		return errors.New("派生世代不处于空白构建状态")
	}
	canonical := make([]Record, len(records))
	seen := make(map[string]uint64, len(records))
	for index, record := range records {
		var err error
		record, err = canonicalRecord(record)
		if err != nil {
			return err
		}
		if err := validateRecord(record); err != nil {
			return err
		}
		if current, exists := s.records[record.AssetID]; exists && current.revision > record.AssetRevision {
			return ErrStaleRecord
		}
		if _, duplicate := seen[record.AssetID]; duplicate {
			return errors.New("批量派生状态包含重复信息标识")
		}
		seen[record.AssetID] = record.AssetRevision
		canonical[index] = record
	}
	start, err := s.file.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("定位派生状态: %w", err)
	}
	references := make([]recordReference, len(canonical))
	offset := start
	for index, record := range canonical {
		encoded, encodeErr := encodeRecord(record)
		if encodeErr != nil {
			_ = s.rollbackLocked(start)
			return encodeErr
		}
		written, writeErr := s.file.Write(encoded)
		if writeErr != nil || written != len(encoded) {
			_ = s.rollbackLocked(start)
			if writeErr == nil {
				writeErr = io.ErrShortWrite
			}
			return fmt.Errorf("批量写入派生状态: %w", writeErr)
		}
		references[index] = recordReference{offset: offset, length: uint32(len(encoded)), revision: record.AssetRevision}
		offset += int64(len(encoded))
	}
	if durable {
		if err := s.file.Sync(); err != nil {
			_ = s.rollbackLocked(start)
			return fmt.Errorf("批量持久化派生状态: %w", err)
		}
	}
	for index, record := range canonical {
		s.records[record.AssetID] = references[index]
		if s.startupRecords != nil {
			s.startupRecords[record.AssetID] = record
		}
	}
	return nil
}

// Compact rewrites a mutable derived log to one current, compact record per
// asset. Sealed generations are compacted by the existing atomic generation
// rebuild path and are therefore left unchanged here.
func (s *Store) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return errors.New("派生状态已关闭")
	}
	if s.sealed {
		return nil
	}
	records, err := s.allLocked(true)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(s.directory, ".organization-compacting-*")
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
	for _, record := range records {
		encoded, encodeErr := encodeRecord(record)
		if encodeErr != nil {
			return encodeErr
		}
		if _, err := temporary.Write(encoded); err != nil {
			return err
		}
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	path := filepath.Join(s.directory, LogFileName)
	if err := s.file.Close(); err != nil {
		return err
	}
	s.file = nil
	if err := replaceFile(temporaryPath, path); err != nil {
		s.file, _ = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		return err
	}
	committed = true
	s.file, err = os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	s.records = make(map[string]recordReference, len(records))
	s.startupRecords = make(map[string]Record, len(records))
	s.needsCompaction = false
	return s.replay()
}

func (s *Store) RecoveredCorruption() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.recoveredCorruption
}

func (s *Store) NeedsCompaction() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.file == nil || s.needsCompaction {
		return s.needsCompaction
	}
	info, err := s.file.Stat()
	if err != nil {
		return false
	}
	live := int64(0)
	for _, reference := range s.records {
		live += int64(reference.length)
	}
	obsolete := info.Size() - live
	return obsolete > 1024*1024 && (live == 0 || obsolete*4 > live)
}

func (s *Store) Sealed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sealed
}

func (s *Store) Get(id string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reference, ok := s.records[id]
	if !ok {
		return Record{}, false
	}
	record, err := s.readReferenceLocked(id, reference, false)
	return record, err == nil
}

func (s *Store) GetWithEmbedding(id string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	reference, ok := s.records[id]
	if !ok {
		return Record{}, false
	}
	record, err := s.readReferenceLocked(id, reference, true)
	return record, err == nil
}

func (s *Store) All() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, err := s.allLocked(false)
	if err != nil {
		return nil
	}
	return result
}

func (s *Store) AllWithEmbeddings() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startupRecords != nil {
		result := make([]Record, 0, len(s.startupRecords))
		for _, record := range s.startupRecords {
			result = append(result, record)
		}
		s.startupRecords = nil
		sort.Slice(result, func(left, right int) bool { return result[left].AssetID < result[right].AssetID })
		return result, nil
	}
	return s.allLocked(true)
}

func (s *Store) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.file.Truncate(0); err != nil {
		return fmt.Errorf("清空派生状态: %w", err)
	}
	if _, err := s.file.Seek(0, 0); err != nil {
		return fmt.Errorf("重置派生状态位置: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("持久化派生状态重置: %w", err)
	}
	s.records = make(map[string]recordReference)
	s.startupRecords = nil
	return nil
}

func (s *Store) replay() error {
	info, err := s.file.Stat()
	if err != nil {
		return err
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(s.file, 1024*1024)
	for offset, recordNumber := int64(0), 1; offset < info.Size(); recordNumber++ {
		remaining := info.Size() - offset
		if remaining < headerSize {
			return s.truncateTail(offset)
		}
		header := make([]byte, headerSize)
		if _, err := io.ReadFull(reader, header); err != nil {
			return err
		}
		if string(header[:4]) != string(recordMagic[:]) {
			return fmt.Errorf("派生状态第 %d 条记录头无效", recordNumber)
		}
		metadataLength := uint64(binary.LittleEndian.Uint32(header[4:8]))
		vectorLength := uint64(binary.LittleEndian.Uint32(header[8:12]))
		if metadataLength == 0 || metadataLength > 32*1024*1024 || vectorLength%4 != 0 || vectorLength/4 > 8192 {
			return fmt.Errorf("派生状态第 %d 条记录长度无效", recordNumber)
		}
		total := uint64(headerSize+footerSize) + metadataLength + vectorLength
		if total > uint64(^uint32(0)) {
			return fmt.Errorf("派生状态第 %d 条记录过大", recordNumber)
		}
		if uint64(remaining) < total {
			return s.truncateTail(offset)
		}
		encoded := make([]byte, int(total))
		copy(encoded, header)
		if _, err := io.ReadFull(reader, encoded[headerSize:]); err != nil {
			return err
		}
		record, previous, err := decodeRecordWithVersion(encoded, true)
		if err != nil {
			return fmt.Errorf("派生状态第 %d 条记录损坏: %w", recordNumber, err)
		}
		if previous {
			s.needsCompaction = true
		}
		s.records[record.AssetID] = recordReference{offset: offset, length: uint32(total), revision: record.AssetRevision}
		s.startupRecords[record.AssetID] = record
		offset += int64(total)
	}
	_, err = s.file.Seek(0, io.SeekEnd)
	return err
}

func (s *Store) truncateTail(offset int64) error {
	if err := s.file.Truncate(offset); err != nil {
		return fmt.Errorf("清理未提交的派生状态尾部: %w", err)
	}
	_, err := s.file.Seek(0, io.SeekEnd)
	return err
}

func (s *Store) allLocked(withEmbeddings bool) ([]Record, error) {
	result := make([]Record, 0, len(s.records))
	for id, reference := range s.records {
		record, err := s.readReferenceLocked(id, reference, withEmbeddings)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].AssetID < result[right].AssetID })
	return result, nil
}

func (s *Store) readReferenceLocked(id string, reference recordReference, withEmbedding bool) (Record, error) {
	encoded := make([]byte, reference.length)
	if _, err := s.file.ReadAt(encoded, reference.offset); err != nil {
		return Record{}, fmt.Errorf("读取派生状态 %q: %w", id, err)
	}
	record, err := decodeRecord(encoded, withEmbedding)
	if err != nil {
		return Record{}, fmt.Errorf("解析派生状态 %q: %w", id, err)
	}
	if record.Schema != recordSchema || record.AssetID != id || record.AssetRevision != reference.revision {
		return Record{}, fmt.Errorf("派生状态 %q 与索引不一致", id)
	}
	if !withEmbedding {
		record.Embedding = nil
	}
	return record, nil
}

func fromPersisted(value persistedRecord) (Record, error) {
	if len(value.Embedding)%4 != 0 || len(value.Embedding)/4 > 8192 {
		return Record{}, errors.New("派生向量编码无效")
	}
	embedding := make([]float32, len(value.Embedding)/4)
	for index := range embedding {
		embedding[index] = math.Float32frombits(binary.LittleEndian.Uint32(value.Embedding[index*4:]))
		if math.IsNaN(float64(embedding[index])) || math.IsInf(float64(embedding[index]), 0) {
			return Record{}, errors.New("派生向量包含非有限数值")
		}
	}
	return canonicalRecord(Record{
		Schema: value.Schema, AssetID: value.AssetID, AssetRevision: value.AssetRevision,
		GeneratedAt: value.GeneratedAt, Provider: value.Provider, Status: value.Status,
		Error: value.Error, Analysis: value.Analysis, SemanticWork: value.SemanticWork,
		SemanticResult: value.SemanticResult, EmbeddingSpace: value.EmbeddingSpace, Embedding: embedding,
	})
}

// EncodeRecord 生成 Store 使用的正式持久化表示，仅供性能夹具验证发布格式。
func EncodeRecord(record Record) ([]byte, error) {
	canonical, err := canonicalRecord(record)
	if err != nil {
		return nil, err
	}
	return encodeRecord(canonical)
}

func encodeRecord(record Record) ([]byte, error) {
	record.Schema = recordSchema
	if err := validateRecord(record); err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(recordMetadata{
		Schema: record.Schema, AssetID: record.AssetID, AssetRevision: record.AssetRevision,
		GeneratedAt: record.GeneratedAt, Provider: record.Provider, Status: record.Status,
		Error: record.Error, Analysis: record.Analysis, SemanticWorkReference: record.SemanticWorkReference,
		SemanticReceipt: record.SemanticReceipt, EmbeddingSpace: record.EmbeddingSpace,
	})
	if err != nil {
		return nil, err
	}
	if len(metadata) > 32*1024*1024 {
		return nil, errors.New("派生状态元数据超过限制")
	}
	vectorLength := len(record.Embedding) * 4
	encoded := make([]byte, headerSize+len(metadata)+vectorLength+footerSize)
	copy(encoded[:4], recordMagic[:])
	binary.LittleEndian.PutUint32(encoded[4:8], uint32(len(metadata)))
	binary.LittleEndian.PutUint32(encoded[8:12], uint32(vectorLength))
	payload := encoded[headerSize : len(encoded)-footerSize]
	copy(payload, metadata)
	vector := payload[len(metadata):]
	for index, value := range record.Embedding {
		binary.LittleEndian.PutUint32(vector[index*4:], math.Float32bits(value))
	}
	binary.LittleEndian.PutUint32(encoded[12:16], crc32.ChecksumIEEE(payload))
	copy(encoded[len(encoded)-footerSize:], commitMagic[:])
	return encoded, nil
}

func validateRecord(record Record) error {
	if strings.TrimSpace(record.AssetID) == "" || record.AssetRevision == 0 {
		return errors.New("派生状态缺少有效信息标识或版本")
	}
	if record.Status != "ready" && record.Status != "degraded" && record.Status != "pending" && record.Status != "uncertain" {
		return errors.New("派生状态值无效")
	}
	if record.SemanticWork != nil || record.SemanticResult != nil {
		return errors.New("派生状态尚未转换为紧凑语义引用")
	}
	if record.SemanticWorkReference != nil {
		if err := record.SemanticWorkReference.Validate(); err != nil || record.SemanticWorkReference.AssetID != record.AssetID || record.SemanticWorkReference.Revision != record.AssetRevision {
			return errors.New("派生状态包含无效语义工作")
		}
	}
	if record.SemanticReceipt != nil {
		if record.SemanticWorkReference == nil || record.SemanticReceipt.Validate() != nil || record.SemanticReceipt.WorkID != record.SemanticWorkReference.ID ||
			record.SemanticReceipt.AssetID != record.AssetID || record.SemanticReceipt.Revision != record.AssetRevision {
			return errors.New("派生状态包含无效语义结果")
		}
	}
	if len(record.Embedding) > 8192 {
		return errors.New("派生向量维度超过限制")
	}
	for _, value := range record.Embedding {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("派生向量包含非有限数值")
		}
	}
	for _, context := range record.Analysis.Contexts {
		if strings.TrimSpace(context.Key) == "" || strings.TrimSpace(context.Value) == "" || strings.TrimSpace(context.Evidence) == "" ||
			context.Confidence < 0.75 || context.Confidence > 1 || math.IsNaN(context.Confidence) || math.IsInf(context.Confidence, 0) {
			return errors.New("推导场景缺少可靠依据")
		}
	}
	for _, relation := range record.Analysis.Relations {
		if strings.TrimSpace(relation.Type) == "" || strings.TrimSpace(relation.TargetID) == "" || relation.TargetID == record.AssetID ||
			relation.Confidence <= 0 || relation.Confidence > 1 || math.IsNaN(relation.Confidence) || math.IsInf(relation.Confidence, 0) {
			return errors.New("语义关系结构无效")
		}
		if relation.TargetRevision > 0 && (relation.Confidence < 0.75 || strings.TrimSpace(relation.Evidence) == "") {
			return errors.New("推导关系缺少可靠依据")
		}
	}
	return nil
}

func decodeRecord(encoded []byte, withEmbedding bool) (Record, error) {
	record, _, err := decodeRecordWithVersion(encoded, withEmbedding)
	return record, err
}

func decodeRecordWithVersion(encoded []byte, withEmbedding bool) (Record, bool, error) {
	if len(encoded) < headerSize+footerSize || string(encoded[:4]) != string(recordMagic[:]) {
		return Record{}, false, errors.New("派生状态记录头无效")
	}
	metadataLength := int(binary.LittleEndian.Uint32(encoded[4:8]))
	vectorLength := int(binary.LittleEndian.Uint32(encoded[8:12]))
	if metadataLength <= 0 || vectorLength%4 != 0 || vectorLength/4 > 8192 || headerSize+metadataLength+vectorLength+footerSize != len(encoded) {
		return Record{}, false, errors.New("派生状态记录长度无效")
	}
	if string(encoded[len(encoded)-footerSize:]) != string(commitMagic[:]) {
		return Record{}, false, errors.New("派生状态记录未提交")
	}
	payload := encoded[headerSize : len(encoded)-footerSize]
	if crc32.ChecksumIEEE(payload) != binary.LittleEndian.Uint32(encoded[12:16]) {
		return Record{}, false, errors.New("派生状态记录校验失败")
	}
	var identity struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(payload[:metadataLength], &identity); err != nil {
		return Record{}, false, err
	}
	previous := identity.Schema == previousRecordSchema
	if identity.Schema != recordSchema && !previous {
		return Record{}, false, errors.New("派生状态记录格式无效")
	}
	var metadata recordMetadata
	var legacy previousRecordMetadata
	if previous {
		if err := json.Unmarshal(payload[:metadataLength], &legacy); err != nil {
			return Record{}, false, err
		}
		metadata = recordMetadata{
			Schema: legacy.Schema, AssetID: legacy.AssetID, AssetRevision: legacy.AssetRevision,
			GeneratedAt: legacy.GeneratedAt, Provider: legacy.Provider, Status: legacy.Status,
			Error: legacy.Error, Analysis: legacy.Analysis, EmbeddingSpace: legacy.EmbeddingSpace,
		}
	} else if err := json.Unmarshal(payload[:metadataLength], &metadata); err != nil {
		return Record{}, false, err
	}
	if strings.TrimSpace(metadata.AssetID) == "" || metadata.AssetRevision == 0 ||
		(metadata.Status != "ready" && metadata.Status != "degraded" && metadata.Status != "pending" && metadata.Status != "uncertain") {
		return Record{}, false, errors.New("派生状态记录元数据无效")
	}
	var embedding []float32
	if withEmbedding {
		embedding = make([]float32, vectorLength/4)
		vector := payload[metadataLength:]
		for index := range embedding {
			embedding[index] = math.Float32frombits(binary.LittleEndian.Uint32(vector[index*4:]))
			if math.IsNaN(float64(embedding[index])) || math.IsInf(float64(embedding[index]), 0) {
				return Record{}, false, errors.New("派生向量包含非有限数值")
			}
		}
	}
	record := Record{
		Schema: recordSchema, AssetID: metadata.AssetID, AssetRevision: metadata.AssetRevision,
		GeneratedAt: metadata.GeneratedAt, Provider: metadata.Provider, Status: metadata.Status,
		Error: metadata.Error, Analysis: metadata.Analysis, SemanticWorkReference: metadata.SemanticWorkReference,
		SemanticReceipt: metadata.SemanticReceipt, EmbeddingSpace: metadata.EmbeddingSpace, Embedding: embedding,
	}
	if previous {
		record.SemanticWork = legacy.SemanticWork
		record.SemanticResult = legacy.SemanticResult
		var err error
		record, err = canonicalRecord(record)
		if err != nil {
			return Record{}, false, err
		}
	}
	if err := validateRecord(record); err != nil {
		return Record{}, false, err
	}
	return record, previous, nil
}

func (s *Store) rollbackLocked(offset int64) error {
	if err := s.file.Truncate(offset); err != nil {
		return err
	}
	if _, err := s.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	return s.file.Sync()
}

func canonicalRecord(record Record) (Record, error) {
	if record.SemanticWorkReference == nil && record.SemanticWork != nil {
		reference, err := semantics.ReferenceWork(*record.SemanticWork)
		if err != nil {
			return Record{}, err
		}
		record.SemanticWorkReference = &reference
	}
	if record.SemanticReceipt == nil && record.SemanticResult != nil {
		receipt, err := semantics.NewSubmissionReceipt(*record.SemanticResult)
		if err != nil {
			return Record{}, err
		}
		record.SemanticReceipt = &receipt
	}
	record.SemanticWork = nil
	record.SemanticResult = nil
	record.Schema = recordSchema
	return record, nil
}

func (r Record) HasPendingSemanticWork() bool {
	return r.SemanticWorkReference != nil && r.SemanticReceipt == nil
}

func (r Record) HasSemanticResult() bool {
	return r.SemanticReceipt != nil
}

func clone(record Record) Record {
	record.Embedding = append([]float32(nil), record.Embedding...)
	record.Analysis.Cues = append([]semantics.Cue(nil), record.Analysis.Cues...)
	record.Analysis.Topics = append([]string(nil), record.Analysis.Topics...)
	record.Analysis.Contexts = append([]semantics.InferredContext(nil), record.Analysis.Contexts...)
	record.Analysis.Relations = append([]semantics.Relation(nil), record.Analysis.Relations...)
	if record.SemanticWorkReference != nil {
		work := *record.SemanticWorkReference
		work.Candidates = append([]semantics.CandidateReference(nil), work.Candidates...)
		if work.Previous != nil {
			previous := *work.Previous
			work.Previous = &previous
		}
		record.SemanticWorkReference = &work
	}
	if record.SemanticReceipt != nil {
		receipt := *record.SemanticReceipt
		record.SemanticReceipt = &receipt
	}
	return record
}
