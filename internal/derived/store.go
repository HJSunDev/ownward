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

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

const (
	recordSchema       = "ownward.derived/v3"
	legacyRecordSchema = "ownward.derived/v2"
	LogFileName        = "organization.binlog"
	legacyLogFileName  = "organization.jsonl"
	headerSize         = 16
	footerSize         = 4
)

var (
	recordMagic = [4]byte{'O', 'W', 'D', '3'}
	commitMagic = [4]byte{'D', 'O', 'N', 'E'}
)

var ErrStaleRecord = errors.New("派生状态版本早于当前版本")

type Record struct {
	Schema        string             `json:"schema"`
	AssetID       string             `json:"asset_id"`
	AssetRevision uint64             `json:"asset_revision"`
	GeneratedAt   time.Time          `json:"generated_at"`
	Provider      string             `json:"provider"`
	Status        string             `json:"status"`
	Error         string             `json:"error,omitempty"`
	Analysis      semantics.Analysis `json:"analysis"`
	Embedding     []float32          `json:"embedding,omitempty"`
}

type persistedRecord struct {
	Schema        string             `json:"schema"`
	AssetID       string             `json:"asset_id"`
	AssetRevision uint64             `json:"asset_revision"`
	GeneratedAt   time.Time          `json:"generated_at"`
	Provider      string             `json:"provider"`
	Status        string             `json:"status"`
	Error         string             `json:"error,omitempty"`
	Analysis      semantics.Analysis `json:"analysis"`
	Embedding     []byte             `json:"embedding_f32le,omitempty"`
}

type recordMetadata struct {
	Schema        string             `json:"schema"`
	AssetID       string             `json:"asset_id"`
	AssetRevision uint64             `json:"asset_revision"`
	GeneratedAt   time.Time          `json:"generated_at"`
	Provider      string             `json:"provider"`
	Status        string             `json:"status"`
	Error         string             `json:"error,omitempty"`
	Analysis      semantics.Analysis `json:"analysis"`
}

type Store struct {
	mu                  sync.RWMutex
	file                *os.File
	records             map[string]recordReference
	startupRecords      map[string]Record
	recoveredCorruption bool
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建派生状态目录: %w", err)
	}
	recoveredLegacy := false
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
	path := filepath.Join(dir, LogFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开派生状态: %w", err)
	}
	store := &Store{
		file: file, records: make(map[string]recordReference),
		startupRecords: make(map[string]Record), recoveredCorruption: recoveredLegacy,
	}
	if err := store.replay(); err != nil {
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
		if err != nil || record.Schema != legacyRecordSchema || record.AssetID == "" || record.AssetRevision == 0 {
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
	if err := validateRecord(record); err != nil {
		return err
	}
	if current, exists := s.records[record.AssetID]; exists && current.revision > record.AssetRevision {
		return ErrStaleRecord
	}
	record.Schema = recordSchema
	encoded, err := EncodeRecord(record)
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

func (s *Store) RecoveredCorruption() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.recoveredCorruption
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
		record, err := decodeRecord(encoded, true)
		if err != nil {
			return fmt.Errorf("派生状态第 %d 条记录损坏: %w", recordNumber, err)
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
	return Record{
		Schema: value.Schema, AssetID: value.AssetID, AssetRevision: value.AssetRevision,
		GeneratedAt: value.GeneratedAt, Provider: value.Provider, Status: value.Status,
		Error: value.Error, Analysis: value.Analysis, Embedding: embedding,
	}, nil
}

// EncodeRecord produces the exact durable representation used by Store. It is
// exported only so the performance fixture can exercise the release format.
func EncodeRecord(record Record) ([]byte, error) {
	record.Schema = recordSchema
	if err := validateRecord(record); err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(recordMetadata{
		Schema: record.Schema, AssetID: record.AssetID, AssetRevision: record.AssetRevision,
		GeneratedAt: record.GeneratedAt, Provider: record.Provider, Status: record.Status,
		Error: record.Error, Analysis: record.Analysis,
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
	if record.Status != "ready" && record.Status != "degraded" && record.Status != "pending" {
		return errors.New("派生状态值无效")
	}
	if len(record.Embedding) > 8192 {
		return errors.New("派生向量维度超过限制")
	}
	for _, value := range record.Embedding {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("派生向量包含非有限数值")
		}
	}
	return nil
}

func decodeRecord(encoded []byte, withEmbedding bool) (Record, error) {
	if len(encoded) < headerSize+footerSize || string(encoded[:4]) != string(recordMagic[:]) {
		return Record{}, errors.New("派生状态记录头无效")
	}
	metadataLength := int(binary.LittleEndian.Uint32(encoded[4:8]))
	vectorLength := int(binary.LittleEndian.Uint32(encoded[8:12]))
	if metadataLength <= 0 || vectorLength%4 != 0 || vectorLength/4 > 8192 || headerSize+metadataLength+vectorLength+footerSize != len(encoded) {
		return Record{}, errors.New("派生状态记录长度无效")
	}
	if string(encoded[len(encoded)-footerSize:]) != string(commitMagic[:]) {
		return Record{}, errors.New("派生状态记录未提交")
	}
	payload := encoded[headerSize : len(encoded)-footerSize]
	if crc32.ChecksumIEEE(payload) != binary.LittleEndian.Uint32(encoded[12:16]) {
		return Record{}, errors.New("派生状态记录校验失败")
	}
	var metadata recordMetadata
	if err := json.Unmarshal(payload[:metadataLength], &metadata); err != nil {
		return Record{}, err
	}
	if metadata.Schema != recordSchema || strings.TrimSpace(metadata.AssetID) == "" || metadata.AssetRevision == 0 ||
		(metadata.Status != "ready" && metadata.Status != "degraded" && metadata.Status != "pending") {
		return Record{}, errors.New("派生状态记录元数据无效")
	}
	var embedding []float32
	if withEmbedding {
		embedding = make([]float32, vectorLength/4)
		vector := payload[metadataLength:]
		for index := range embedding {
			embedding[index] = math.Float32frombits(binary.LittleEndian.Uint32(vector[index*4:]))
			if math.IsNaN(float64(embedding[index])) || math.IsInf(float64(embedding[index]), 0) {
				return Record{}, errors.New("派生向量包含非有限数值")
			}
		}
	}
	return Record{
		Schema: metadata.Schema, AssetID: metadata.AssetID, AssetRevision: metadata.AssetRevision,
		GeneratedAt: metadata.GeneratedAt, Provider: metadata.Provider, Status: metadata.Status,
		Error: metadata.Error, Analysis: metadata.Analysis, Embedding: embedding,
	}, nil
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

func clone(record Record) Record {
	record.Embedding = append([]float32(nil), record.Embedding...)
	record.Analysis.Cues = append([]semantics.Cue(nil), record.Analysis.Cues...)
	record.Analysis.Topics = append([]string(nil), record.Analysis.Topics...)
	record.Analysis.Contexts = append([]domain.Context(nil), record.Analysis.Contexts...)
	record.Analysis.Relations = append([]semantics.Relation(nil), record.Analysis.Relations...)
	return record
}
