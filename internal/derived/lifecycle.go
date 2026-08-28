//go:build ownward_migration

package derived

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GenerationState is the durable identity of one sealed derived generation.
// These operations belong exclusively to offline capability replacement;
// CommitGeneration retains its established in-version maintenance behavior.
type GenerationState struct {
	Generation          string `json:"generation"`
	ManifestSHA256      string `json:"manifest_sha256"`
	AssetCount          int    `json:"asset_count"`
	AssetSnapshot       string `json:"asset_snapshot"`
	EmbeddingSpace      string `json:"embedding_space,omitempty"`
	RecordCount         int    `json:"record_count"`
	SealedBytes         int64  `json:"sealed_bytes"`
	LogBytes            int64  `json:"log_bytes"`
	LogSHA256           string `json:"log_sha256"`
	RecoveredCorruption bool   `json:"recovered_corruption"`
}

// CandidateRoot creates a non-open anchor used by a candidate binary to place
// an isolated generation without opening or reading the active generation.
func CandidateRoot(root string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Store{root: absolute}, nil
}

func (s *Store) SealGeneration(metadata GenerationMetadata) (GenerationState, error) {
	if _, err := s.seal(metadata); err != nil {
		return GenerationState{}, err
	}
	return InspectGeneration(s.Root(), s.Generation())
}

// ResealGeneration advances the durable identity of an existing lifecycle
// generation to its complete current log. It is never used by normal product
// maintenance; callers must subsequently rebind the active pointer when this
// generation is active.
func (s *Store) ResealGeneration(metadata GenerationMetadata) (GenerationState, error) {
	s.mu.Lock()
	if s.file == nil || !s.sealed || s.generation == "legacy" {
		s.mu.Unlock()
		return GenerationState{}, errors.New("派生世代不能重新封存")
	}
	if metadata.AssetCount < 0 || metadata.AssetCount != len(s.records) || !validDigest(metadata.AssetSnapshot) {
		s.mu.Unlock()
		return GenerationState{}, errors.New("派生世代与资产快照不一致")
	}
	for id, reference := range s.records {
		record, err := s.readReferenceLocked(id, reference, true)
		if err != nil {
			s.mu.Unlock()
			return GenerationState{}, err
		}
		if len(record.Embedding) > 0 && (metadata.EmbeddingSpace == "" || record.EmbeddingSpace != metadata.EmbeddingSpace) {
			s.mu.Unlock()
			return GenerationState{}, errors.New("派生向量与待提交能力世代不一致")
		}
	}
	if err := s.file.Sync(); err != nil {
		s.mu.Unlock()
		return GenerationState{}, err
	}
	info, err := s.file.Stat()
	if err != nil {
		s.mu.Unlock()
		return GenerationState{}, err
	}
	logBytes := info.Size()
	logDigest, err := fileDigestPrefix(filepath.Join(s.directory, LogFileName), logBytes)
	if err != nil {
		s.mu.Unlock()
		return GenerationState{}, err
	}
	createdAt := time.Time{}
	if encoded, readErr := os.ReadFile(filepath.Join(s.directory, manifestFileName)); readErr == nil {
		var existing generationManifest
		if json.Unmarshal(encoded, &existing) == nil && existing.Schema == generationSchema && existing.Generation == s.generation {
			createdAt = existing.CreatedAt
		}
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	manifest := generationManifest{
		Schema: generationSchema, Generation: s.generation, CreatedAt: createdAt,
		RecordCount: len(s.records), AssetCount: metadata.AssetCount,
		AssetSnapshot: metadata.AssetSnapshot, EmbeddingSpace: metadata.EmbeddingSpace,
		SealedBytes: logBytes, LogSHA256: logDigest,
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		s.mu.Unlock()
		return GenerationState{}, err
	}
	encoded = append(encoded, '\n')
	if err := writeAtomic(filepath.Join(s.directory, manifestFileName), encoded, 0o600); err != nil {
		s.mu.Unlock()
		return GenerationState{}, err
	}
	manifestDigest := sha256.Sum256(encoded)
	state := GenerationState{
		Generation: s.generation, ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
		AssetCount: metadata.AssetCount, AssetSnapshot: metadata.AssetSnapshot,
		EmbeddingSpace: metadata.EmbeddingSpace, RecordCount: len(s.records),
		SealedBytes: logBytes, LogBytes: logBytes, LogSHA256: logDigest, RecoveredCorruption: s.recoveredCorruption,
	}
	s.mu.Unlock()
	return state, nil
}

func ActiveGeneration(root string) (GenerationState, error) {
	generation, _, sealed, err := currentGeneration(root)
	if err != nil {
		return GenerationState{}, err
	}
	if !sealed || generation == "legacy" {
		return GenerationState{Generation: "legacy"}, nil
	}
	return InspectGeneration(root, generation)
}

func OpenGeneration(root, generation string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if !validGenerationID(generation) {
		return nil, errors.New("派生世代标识无效")
	}
	directory := filepath.Join(absolute, generationsName, generation)
	store, err := openStore(absolute, generation, directory, true, false)
	if err != nil {
		return nil, err
	}
	if err := store.verifyGeneration(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func InspectGeneration(root, generation string) (GenerationState, error) {
	store, err := OpenGeneration(root, generation)
	if err != nil {
		return GenerationState{}, err
	}
	defer store.Close()
	encoded, err := os.ReadFile(filepath.Join(store.directory, manifestFileName))
	if err != nil {
		return GenerationState{}, err
	}
	var manifest generationManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return GenerationState{}, err
	}
	manifestDigest := sha256.Sum256(encoded)
	info, err := store.file.Stat()
	if err != nil {
		return GenerationState{}, err
	}
	logDigest, err := fileDigestPrefix(filepath.Join(store.directory, LogFileName), info.Size())
	if err != nil {
		return GenerationState{}, err
	}
	return GenerationState{
		Generation: generation, ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
		AssetCount: manifest.AssetCount, AssetSnapshot: manifest.AssetSnapshot,
		EmbeddingSpace: manifest.EmbeddingSpace, RecordCount: len(store.records),
		SealedBytes: manifest.SealedBytes, LogBytes: info.Size(), LogSHA256: logDigest, RecoveredCorruption: store.RecoveredCorruption(),
	}, nil
}

func SwitchGeneration(root, expected, next string) (GenerationState, error) {
	state, err := InspectGeneration(root, next)
	if err != nil {
		return GenerationState{}, err
	}
	current, _, sealed, err := currentGeneration(root)
	if err != nil {
		return GenerationState{}, err
	}
	if !sealed {
		current = "legacy"
	}
	if current != expected {
		return GenerationState{}, fmt.Errorf("当前派生世代已更新为 %s", current)
	}
	pointer := generationPointer{Schema: currentSchema, Generation: next, ManifestSHA256: state.ManifestSHA256}
	encoded, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return GenerationState{}, err
	}
	if err := writeAtomic(filepath.Join(root, currentFileName), append(encoded, '\n'), 0o600); err != nil {
		return GenerationState{}, err
	}
	return state, nil
}

// RebindActiveGeneration updates only the manifest identity of the same
// active generation after an incremental tail is durably resealed. It also
// recovers idempotently when the manifest was written before a crash but the
// pointer update or lifecycle journal append was not.
func RebindActiveGeneration(root string, expected, next GenerationState) (bool, error) {
	if expected.Generation == "" || expected.Generation != next.Generation || expected.Generation == "legacy" ||
		!validDigest(expected.ManifestSHA256) || !validDigest(next.ManifestSHA256) {
		return false, errors.New("派生世代重绑定身份无效")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	pointerPath := filepath.Join(absolute, currentFileName)
	encoded, err := os.ReadFile(pointerPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var pointer generationPointer
	if json.Unmarshal(encoded, &pointer) != nil || pointer.Schema != currentSchema || !validGenerationID(pointer.Generation) || !validDigest(pointer.ManifestSHA256) {
		return false, errors.New("当前派生世代指针无效")
	}
	if pointer.Generation != expected.Generation {
		return false, nil
	}
	if pointer.ManifestSHA256 == next.ManifestSHA256 {
		return true, nil
	}
	if pointer.ManifestSHA256 != expected.ManifestSHA256 {
		return false, errors.New("当前派生世代身份已被并发更新")
	}
	manifest, err := os.ReadFile(filepath.Join(absolute, generationsName, next.Generation, manifestFileName))
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(manifest)
	if hex.EncodeToString(digest[:]) != next.ManifestSHA256 {
		return false, errors.New("重新封存的派生清单摘要不一致")
	}
	pointer = generationPointer{Schema: currentSchema, Generation: next.Generation, ManifestSHA256: next.ManifestSHA256}
	encoded, err = json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return false, err
	}
	if err := writeAtomic(pointerPath, append(encoded, '\n'), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func RetireGeneration(root, generation string) error {
	if generation == "legacy" || !validGenerationID(generation) {
		return errors.New("不能回收该派生世代")
	}
	active, err := ActiveGeneration(root)
	if err != nil {
		return err
	}
	if active.Generation == generation {
		return errors.New("不能回收当前派生世代")
	}
	directory := filepath.Join(root, generationsName, generation)
	if _, err := InspectGeneration(root, generation); err != nil {
		return err
	}
	return os.RemoveAll(directory)
}

// CandidateCheckpointPath returns the migration-only checkpoint location for
// one isolated generation. It never aliases the active pointer or generation
// contents.
func CandidateCheckpointPath(root, generation string) (string, error) {
	if !validGenerationID(generation) {
		return "", errors.New("派生世代标识无效")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(absolute, "candidate-lifecycle", generation+".json"), nil
}

// CandidateGenerationSealed distinguishes an adoptable completed build from
// an incomplete directory left before the manifest's atomic publication.
func CandidateGenerationSealed(root, generation string) (bool, error) {
	if !validGenerationID(generation) {
		return false, errors.New("派生世代标识无效")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(absolute, generationsName, generation, manifestFileName))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// DiscardInactiveGeneration removes only a non-active candidate directory.
// It is used to recover an interrupted build that never atomically sealed.
func DiscardInactiveGeneration(root, generation string) error {
	if generation == "legacy" || !validGenerationID(generation) {
		return errors.New("不能丢弃该派生世代")
	}
	active, err := ActiveGeneration(root)
	if err != nil {
		return err
	}
	if active.Generation == generation {
		return errors.New("不能丢弃当前派生世代")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(absolute, generationsName, generation))
}
