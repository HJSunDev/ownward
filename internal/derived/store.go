package derived

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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

const recordSchema = "ownward.derived/v2"

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

type Store struct {
	mu                  sync.RWMutex
	path                string
	file                *os.File
	records             map[string]Record
	recoveredCorruption bool
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建派生状态目录: %w", err)
	}
	path := filepath.Join(dir, "organization.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开派生状态: %w", err)
	}
	store := &Store{path: path, file: file, records: make(map[string]Record)}
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
		store.records = make(map[string]Record)
		store.recoveredCorruption = true
	}
	return store, nil
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
	if current, exists := s.records[record.AssetID]; exists && current.AssetRevision > record.AssetRevision {
		return ErrStaleRecord
	}
	record.Schema = recordSchema
	encoded, err := json.Marshal(toPersisted(record))
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
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
	s.records[record.AssetID] = cloneMetadata(record)
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
	record, ok := s.records[id]
	return clone(record), ok
}

func (s *Store) All() []Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Record, 0, len(s.records))
	for _, record := range s.records {
		result = append(result, clone(record))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AssetID < result[j].AssetID })
	return result
}

func (s *Store) AllWithEmbeddings() ([]Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	file, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	latest := make(map[string]Record, len(s.records))
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var persisted persistedRecord
		if err := json.Unmarshal(scanner.Bytes(), &persisted); err != nil {
			return nil, fmt.Errorf("派生状态第 %d 行损坏: %w", line, err)
		}
		record, err := fromPersisted(persisted)
		if err != nil {
			return nil, fmt.Errorf("派生状态第 %d 行无效: %w", line, err)
		}
		latest[record.AssetID] = record
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(latest))
	for _, record := range latest {
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].AssetID < result[j].AssetID })
	return result, nil
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
	s.records = make(map[string]Record)
	return nil
}

func (s *Store) replay() error {
	if _, err := s.file.Seek(0, 0); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(s.file, 64*1024)
	line := 0
	committedEnd := int64(0)
	for {
		encoded, readErr := reader.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			if len(encoded) > 0 {
				if err := s.file.Truncate(committedEnd); err != nil {
					return fmt.Errorf("清理未提交的派生状态尾部: %w", err)
				}
			}
			break
		}
		if readErr != nil {
			return readErr
		}
		line++
		var persisted persistedRecord
		if err := json.Unmarshal(encoded, &persisted); err != nil {
			return fmt.Errorf("派生状态第 %d 行损坏: %w", line, err)
		}
		record, err := fromPersisted(persisted)
		if err != nil {
			return fmt.Errorf("派生状态第 %d 行无效: %w", line, err)
		}
		if record.Schema != recordSchema || record.AssetID == "" || record.AssetRevision == 0 {
			return fmt.Errorf("派生状态第 %d 行无效", line)
		}
		s.records[record.AssetID] = cloneMetadata(record)
		committedEnd += int64(len(encoded))
	}
	_, err := s.file.Seek(0, 2)
	return err
}

func toPersisted(record Record) persistedRecord {
	encoded := make([]byte, len(record.Embedding)*4)
	for index, value := range record.Embedding {
		binary.LittleEndian.PutUint32(encoded[index*4:], math.Float32bits(value))
	}
	return persistedRecord{
		Schema: record.Schema, AssetID: record.AssetID, AssetRevision: record.AssetRevision,
		GeneratedAt: record.GeneratedAt, Provider: record.Provider, Status: record.Status,
		Error: record.Error, Analysis: record.Analysis, Embedding: encoded,
	}
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

func cloneMetadata(record Record) Record {
	record = clone(record)
	record.Embedding = nil
	return record
}
