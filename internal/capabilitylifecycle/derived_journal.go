//go:build ownward_migration

package capabilitylifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/HJSunDev/ownward/internal/derived"
)

const (
	DerivedRecordSchema       = "ownward.derived-capability-lifecycle-record/v1"
	DerivedPhaseReady         = "ready"
	DerivedPhaseSwitching     = "switching"
	DerivedPhaseObserving     = "observing"
	DerivedPhaseRollbackReady = "rollback-ready"
	DerivedPhaseAccepted      = "accepted"
	DerivedPhaseRolledBack    = "rolled-back"
	derivedEnvelopeSchema     = "ownward.derived-capability-lifecycle-envelope/v1"
)

type DerivedRecord struct {
	Schema            string                  `json:"schema"`
	Revision          uint64                  `json:"revision"`
	Plan              string                  `json:"plan"`
	Phase             string                  `json:"phase"`
	Baseline          derived.GenerationState `json:"baseline_generation"`
	Candidate         derived.GenerationState `json:"candidate_generation"`
	BaselineSnapshot  AuthoritySnapshot       `json:"baseline_snapshot"`
	CandidateSnapshot AuthoritySnapshot       `json:"candidate_snapshot"`
	ObservationSHA256 string                  `json:"observation_sha256,omitempty"`
}

type DerivedJournal interface {
	Read() (DerivedRecord, bool, error)
	Append(uint64, DerivedRecord) (DerivedRecord, error)
}

type derivedEnvelope struct {
	Schema string        `json:"schema"`
	Record DerivedRecord `json:"record"`
	SHA256 string        `json:"sha256"`
}

type FileDerivedJournal struct {
	mu  sync.Mutex
	dir string
}

func OpenDerivedJournal(dir string) (*FileDerivedJournal, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("派生候选生命周期目录必须是绝对路径")
	}
	return &FileDerivedJournal{dir: filepath.Clean(dir)}, nil
}

func (journal *FileDerivedJournal) Read() (DerivedRecord, bool, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.read()
}

func (journal *FileDerivedJournal) Append(expected uint64, next DerivedRecord) (DerivedRecord, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	current, exists, err := journal.read()
	if err != nil {
		return DerivedRecord{}, err
	}
	actual := uint64(0)
	if exists {
		actual = current.Revision
	}
	if actual != expected || next.Revision != expected+1 {
		return DerivedRecord{}, fmt.Errorf("派生候选检查点冲突，当前修订为 %d", actual)
	}
	if err := validateDerivedTransition(current, exists, next); err != nil {
		return DerivedRecord{}, err
	}
	encoded, err := encodeDerivedRecord(next)
	if err != nil {
		return DerivedRecord{}, err
	}
	if err := os.MkdirAll(journal.dir, 0o700); err != nil {
		return DerivedRecord{}, err
	}
	temporary, err := os.CreateTemp(journal.dir, ".derived-record-*.tmp")
	if err != nil {
		return DerivedRecord{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return DerivedRecord{}, err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return DerivedRecord{}, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return DerivedRecord{}, err
	}
	if err := temporary.Close(); err != nil {
		return DerivedRecord{}, err
	}
	if err := os.Link(temporaryPath, filepath.Join(journal.dir, derivedRecordName(next.Revision))); err != nil {
		return DerivedRecord{}, fmt.Errorf("提交派生候选检查点: %w", err)
	}
	return next, nil
}

func (journal *FileDerivedJournal) read() (DerivedRecord, bool, error) {
	entries, err := os.ReadDir(journal.dir)
	if errors.Is(err, os.ErrNotExist) {
		return DerivedRecord{}, false, nil
	}
	if err != nil {
		return DerivedRecord{}, false, err
	}
	var revisions []uint64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		revision, parseErr := strconv.ParseUint(strings.TrimSuffix(entry.Name(), ".json"), 10, 64)
		if parseErr != nil || revision == 0 || entry.Name() != derivedRecordName(revision) {
			return DerivedRecord{}, false, fmt.Errorf("派生候选检查点名称无效: %s", entry.Name())
		}
		revisions = append(revisions, revision)
	}
	if len(revisions) == 0 {
		return DerivedRecord{}, false, nil
	}
	sort.Slice(revisions, func(left, right int) bool { return revisions[left] < revisions[right] })
	var previous DerivedRecord
	for index, revision := range revisions {
		if revision != uint64(index+1) {
			return DerivedRecord{}, false, errors.New("派生候选检查点不连续")
		}
		encoded, readErr := os.ReadFile(filepath.Join(journal.dir, derivedRecordName(revision)))
		if readErr != nil {
			return DerivedRecord{}, false, readErr
		}
		record, decodeErr := decodeDerivedRecord(encoded)
		if decodeErr != nil || record.Revision != revision {
			return DerivedRecord{}, false, errors.New("派生候选检查点损坏")
		}
		if err := validateDerivedTransition(previous, index > 0, record); err != nil {
			return DerivedRecord{}, false, err
		}
		previous = record
	}
	return previous, true, nil
}

func (record DerivedRecord) validate() error {
	if record.Schema != DerivedRecordSchema || record.Revision == 0 || !isSHA256(record.Plan) ||
		record.Baseline.Generation == "" || record.Candidate.Generation == "" || record.Baseline.Generation == record.Candidate.Generation ||
		!isSHA256(record.Baseline.ManifestSHA256) || !isSHA256(record.Baseline.LogSHA256) ||
		!isSHA256(record.Candidate.ManifestSHA256) || !isSHA256(record.Candidate.LogSHA256) ||
		!isSHA256(record.Baseline.AssetSnapshot) || !isSHA256(record.Candidate.AssetSnapshot) ||
		record.Baseline.AssetCount < 0 || record.Candidate.AssetCount < 0 ||
		record.Baseline.RecordCount < record.Baseline.AssetCount || record.Candidate.RecordCount < record.Candidate.AssetCount ||
		record.Baseline.SealedBytes < 0 || record.Candidate.SealedBytes < 0 ||
		record.Baseline.LogBytes < record.Baseline.SealedBytes || record.Candidate.LogBytes < record.Candidate.SealedBytes ||
		record.Baseline.RecoveredCorruption || record.Candidate.RecoveredCorruption ||
		validateSnapshot(record.BaselineSnapshot) != nil || validateSnapshot(record.CandidateSnapshot) != nil {
		return errors.New("派生候选检查点内容无效")
	}
	switch record.Phase {
	case DerivedPhaseReady, DerivedPhaseSwitching, DerivedPhaseObserving:
		if record.ObservationSHA256 != "" {
			return errors.New("未结束的派生候选包含观察结论")
		}
	case DerivedPhaseRollbackReady, DerivedPhaseAccepted, DerivedPhaseRolledBack:
		if !isSHA256(record.ObservationSHA256) {
			return errors.New("派生候选阶段缺少观察证据")
		}
	default:
		return errors.New("派生候选阶段无效")
	}
	return nil
}

func validateDerivedTransition(current DerivedRecord, exists bool, next DerivedRecord) error {
	if err := next.validate(); err != nil {
		return err
	}
	if !exists {
		if next.Phase != DerivedPhaseReady {
			return errors.New("派生候选必须从可晋升阶段开始")
		}
		return nil
	}
	if current.Phase == DerivedPhaseAccepted || current.Phase == DerivedPhaseRolledBack {
		if next.Phase != DerivedPhaseReady || next.Plan == current.Plan {
			return errors.New("已结束的派生生命周期只能开始另一计划")
		}
		return nil
	}
	if next.Plan != current.Plan || next.Baseline.Generation != current.Baseline.Generation || next.Candidate.Generation != current.Candidate.Generation {
		return errors.New("派生候选检查点身份发生变化")
	}
	allowed := false
	switch current.Phase {
	case DerivedPhaseReady:
		allowed = next.Phase == DerivedPhaseReady || next.Phase == DerivedPhaseSwitching
	case DerivedPhaseSwitching:
		allowed = next.Phase == DerivedPhaseObserving
	case DerivedPhaseObserving:
		allowed = next.Phase == DerivedPhaseObserving || next.Phase == DerivedPhaseRollbackReady || next.Phase == DerivedPhaseAccepted
	case DerivedPhaseRollbackReady:
		allowed = next.Phase == DerivedPhaseRollbackReady || next.Phase == DerivedPhaseRolledBack
	}
	if !allowed {
		return fmt.Errorf("非法派生候选阶段迁移: %s -> %s", current.Phase, next.Phase)
	}
	return nil
}

func encodeDerivedRecord(record DerivedRecord) ([]byte, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	encoded, err := json.MarshalIndent(derivedEnvelope{Schema: derivedEnvelopeSchema, Record: record, SHA256: hex.EncodeToString(digest[:])}, "", "  ")
	return append(encoded, '\n'), err
}

func decodeDerivedRecord(encoded []byte) (DerivedRecord, error) {
	var envelope derivedEnvelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return DerivedRecord{}, err
	}
	if envelope.Schema != derivedEnvelopeSchema || envelope.Record.validate() != nil {
		return DerivedRecord{}, errors.New("派生候选检查点格式无效")
	}
	payload, err := json.Marshal(envelope.Record)
	if err != nil {
		return DerivedRecord{}, err
	}
	digest := sha256.Sum256(payload)
	if envelope.SHA256 != hex.EncodeToString(digest[:]) {
		return DerivedRecord{}, errors.New("派生候选检查点摘要不一致")
	}
	return envelope.Record, nil
}

func derivedRecordName(revision uint64) string {
	return fmt.Sprintf("%020d.json", revision)
}
