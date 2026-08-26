package derived

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	currentSchema    = "ownward.derived-current/v1"
	generationSchema = "ownward.derived-generation/v2"
	currentFileName  = "current.json"
	manifestFileName = "manifest.json"
	generationsName  = "generations"
)

type GenerationMetadata struct {
	AssetCount     int
	AssetSnapshot  string
	EmbeddingSpace string
}

type generationPointer struct {
	Schema         string `json:"schema"`
	Generation     string `json:"generation"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type generationManifest struct {
	Schema         string    `json:"schema"`
	Generation     string    `json:"generation"`
	CreatedAt      time.Time `json:"created_at"`
	RecordCount    int       `json:"record_count"`
	AssetCount     int       `json:"asset_count"`
	AssetSnapshot  string    `json:"asset_snapshot"`
	EmbeddingSpace string    `json:"embedding_space,omitempty"`
	SealedBytes    int64     `json:"sealed_bytes"`
	LogSHA256      string    `json:"log_sha256"`
}

func NewGenerationID(now time.Time) (string, error) {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return fmt.Sprintf("gen-%s-%s", now.UTC().Format("20060102t150405.000000000z"), hex.EncodeToString(random)), nil
}

func CreateGeneration(root, generation string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	generation = strings.TrimSpace(generation)
	if !validGenerationID(generation) {
		return nil, errors.New("派生世代标识无效")
	}
	directory := filepath.Join(absolute, generationsName, generation)
	if err := os.MkdirAll(filepath.Dir(directory), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, fmt.Errorf("创建派生世代: %w", err)
	}
	store, err := openStore(absolute, generation, directory, false, false)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	return store, nil
}

// StageGeneration writes a complete rebuild candidate without imposing a
// durability barrier per record. The generation is isolated and invisible;
// CommitGeneration validates it, fsyncs the complete log, seals its manifest,
// and only then atomically switches the current pointer.
func (s *Store) StageGeneration(records []Record) error {
	return s.putRecords(records, false, true)
}

func (s *Store) Root() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.root
}

func (s *Store) Generation() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

func (s *Store) Discard() error {
	s.mu.Lock()
	directory := s.directory
	root := s.root
	file := s.file
	s.file = nil
	s.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
	if directory == "" || directory == root {
		return nil
	}
	return os.RemoveAll(directory)
}

func (s *Store) CommitGeneration(next *Store, metadata GenerationMetadata) error {
	if next == nil || next == s {
		return errors.New("待提交派生世代无效")
	}
	if s.Root() == "" || s.Root() != next.Root() || next.Generation() == "legacy" {
		return errors.New("待提交派生世代不属于当前状态目录")
	}
	manifestDigest, err := next.seal(metadata)
	if err != nil {
		return err
	}
	pointer := generationPointer{Schema: currentSchema, Generation: next.Generation(), ManifestSHA256: manifestDigest}
	encoded, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := writeAtomic(filepath.Join(s.Root(), currentFileName), encoded, 0o600); err != nil {
		return fmt.Errorf("切换当前派生世代: %w", err)
	}

	s.mu.Lock()
	next.mu.Lock()
	oldFile := s.file
	oldDirectory := s.directory
	s.file = next.file
	s.records = next.records
	s.startupRecords = next.startupRecords
	s.recoveredCorruption = next.recoveredCorruption
	s.directory = next.directory
	s.generation = next.generation
	s.sealed = true
	next.file = nil
	next.records = nil
	next.startupRecords = nil
	next.mu.Unlock()
	s.mu.Unlock()
	if oldFile != nil {
		_ = oldFile.Close()
	}
	if oldDirectory != "" && oldDirectory != s.Root() && oldDirectory != s.directory {
		_ = os.RemoveAll(oldDirectory)
	}
	if oldDirectory == s.Root() {
		_ = os.Remove(filepath.Join(oldDirectory, LogFileName))
	}
	return nil
}

func (s *Store) seal(metadata GenerationMetadata) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil || s.sealed || s.generation == "legacy" {
		return "", errors.New("派生世代不能封存")
	}
	if metadata.AssetCount < 0 || metadata.AssetCount != len(s.records) || !validDigest(metadata.AssetSnapshot) {
		return "", errors.New("派生世代与资产快照不一致")
	}
	for id, reference := range s.records {
		record, err := s.readReferenceLocked(id, reference, true)
		if err != nil {
			return "", err
		}
		if len(record.Embedding) > 0 && (strings.TrimSpace(metadata.EmbeddingSpace) == "" || record.EmbeddingSpace != metadata.EmbeddingSpace) {
			return "", errors.New("派生向量与待提交能力世代不一致")
		}
	}
	if err := s.file.Sync(); err != nil {
		return "", err
	}
	info, err := s.file.Stat()
	if err != nil {
		return "", err
	}
	sealedBytes := info.Size()
	logDigest, err := fileDigestPrefix(filepath.Join(s.directory, LogFileName), sealedBytes)
	if err != nil {
		return "", err
	}
	manifest := generationManifest{
		Schema: generationSchema, Generation: s.generation, CreatedAt: time.Now().UTC(),
		RecordCount: len(s.records), AssetCount: metadata.AssetCount,
		AssetSnapshot: strings.TrimSpace(metadata.AssetSnapshot), EmbeddingSpace: strings.TrimSpace(metadata.EmbeddingSpace),
		SealedBytes: sealedBytes, LogSHA256: logDigest,
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	encoded = append(encoded, '\n')
	manifestPath := filepath.Join(s.directory, manifestFileName)
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		return "", err
	}
	s.sealed = true
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func currentGeneration(root string) (string, string, bool, error) {
	pointerPath := filepath.Join(root, currentFileName)
	encoded, err := os.ReadFile(pointerPath)
	if errors.Is(err, os.ErrNotExist) {
		return "legacy", root, false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	var pointer generationPointer
	if err := json.Unmarshal(encoded, &pointer); err != nil || pointer.Schema != currentSchema || !validGenerationID(pointer.Generation) || !validDigest(pointer.ManifestSHA256) {
		return "", "", false, errors.New("当前派生世代指针无效")
	}
	directory := filepath.Join(root, generationsName, pointer.Generation)
	manifest, err := os.ReadFile(filepath.Join(directory, manifestFileName))
	if err != nil {
		return "", "", false, fmt.Errorf("读取当前派生世代清单: %w", err)
	}
	digest := sha256.Sum256(manifest)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), pointer.ManifestSHA256) {
		return "", "", false, errors.New("当前派生世代清单与指针不一致")
	}
	return pointer.Generation, directory, true, nil
}

func (s *Store) verifyGeneration() error {
	encoded, err := os.ReadFile(filepath.Join(s.directory, manifestFileName))
	if err != nil {
		return err
	}
	var manifest generationManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil || manifest.Schema != generationSchema || manifest.Generation != s.generation || manifest.SealedBytes < 0 || manifest.RecordCount < 0 || manifest.AssetCount != manifest.RecordCount || len(s.records) < manifest.RecordCount || !validDigest(manifest.AssetSnapshot) || !validDigest(manifest.LogSHA256) {
		return errors.New("当前派生世代清单无效")
	}
	info, err := s.file.Stat()
	if err != nil || info.Size() < manifest.SealedBytes {
		return errors.New("当前派生世代短于已封存基线")
	}
	actual, err := fileDigestPrefix(filepath.Join(s.directory, LogFileName), manifest.SealedBytes)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, manifest.LogSHA256) {
		return errors.New("当前派生世代内容与清单不一致")
	}
	return nil
}

func validGenerationID(value string) bool {
	if len(value) < 4 || len(value) > 96 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == sha256.Size
}

func fileDigest(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return fileDigestPrefix(path, info.Size())
}

func fileDigestPrefix(path string, length int64) (string, error) {
	if length < 0 {
		return "", errors.New("派生状态摘要长度无效")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	written, err := io.CopyN(hasher, file, length)
	if err != nil || written != length {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func writeAtomic(path string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".current-writing-*")
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
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}
