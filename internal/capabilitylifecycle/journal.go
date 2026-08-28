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
)

const (
	JournalRecordSchema = "ownward.capability-lifecycle-record/v1"
	PhasePrepared       = "prepared"
	PhaseObserving      = "observing"
	PhaseAccepted       = "accepted"
	PhaseRolledBack     = "rolled-back"

	journalEnvelopeSchema = "ownward.capability-lifecycle-record-envelope/v1"
)

// JournalRecord is lifecycle evidence, not an active product truth. The only
// active selection remains control-state/v1. Records are append-only so an
// interrupted transition can be reconciled with that authority decision.
type JournalRecord struct {
	Schema                   string `json:"schema"`
	Revision                 uint64 `json:"revision"`
	Plan                     string `json:"plan"`
	Role                     string `json:"role"`
	CandidateComponent       string `json:"candidate_component"`
	BaselineComposition      string `json:"baseline_composition"`
	TargetComposition        string `json:"target_composition"`
	BaselineKernelGeneration string `json:"baseline_kernel_generation"`
	ValidationSHA256         string `json:"validation_sha256"`
	Phase                    string `json:"phase"`
	ObservationSHA256        string `json:"observation_sha256,omitempty"`
}

func (record JournalRecord) validate() error {
	if record.Schema != JournalRecordSchema || record.Revision == 0 || record.Role != "access" ||
		!isSHA256(record.Plan) || !isSHA256(record.CandidateComponent) ||
		!isSHA256(record.BaselineComposition) || !isSHA256(record.TargetComposition) ||
		record.BaselineComposition == record.TargetComposition || !isSHA256(record.BaselineKernelGeneration) ||
		!isSHA256(record.ValidationSHA256) {
		return errors.New("候选生命周期记录无效")
	}
	switch record.Phase {
	case PhasePrepared, PhaseObserving:
		if record.ObservationSHA256 != "" {
			return errors.New("未结束的候选生命周期包含观察结论")
		}
	case PhaseAccepted, PhaseRolledBack:
		if !isSHA256(record.ObservationSHA256) {
			return errors.New("已结束的候选生命周期缺少观察证据")
		}
	default:
		return errors.New("候选生命周期阶段无效")
	}
	return nil
}

// Journal is an append-only candidate checkpoint. It does not own product
// selection and cannot mutate assets, derived state or authority control.
type Journal interface {
	Read() (JournalRecord, bool, error)
	Append(expectedRevision uint64, next JournalRecord) (JournalRecord, error)
}

type journalEnvelope struct {
	Schema string        `json:"schema"`
	Record JournalRecord `json:"record"`
	SHA256 string        `json:"sha256"`
}

// FileJournal opens a durable append-only journal directory. Every revision
// is published under a new name, so a crash cannot replace a prior checkpoint.
type FileJournal struct {
	mu  sync.Mutex
	dir string
}

func OpenFileJournal(dir string) (*FileJournal, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("候选生命周期目录必须是绝对路径")
	}
	return &FileJournal{dir: filepath.Clean(dir)}, nil
}

func (journal *FileJournal) Read() (JournalRecord, bool, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.read()
}

func (journal *FileJournal) Append(expectedRevision uint64, next JournalRecord) (JournalRecord, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	current, exists, err := journal.read()
	if err != nil {
		return JournalRecord{}, err
	}
	actualRevision := uint64(0)
	if exists {
		actualRevision = current.Revision
	}
	if actualRevision != expectedRevision {
		return JournalRecord{}, fmt.Errorf("候选生命周期已更新，当前修订为 %d", actualRevision)
	}
	if next.Revision != expectedRevision+1 {
		return JournalRecord{}, errors.New("候选生命周期修订不连续")
	}
	if err := validateTransition(current, exists, next); err != nil {
		return JournalRecord{}, err
	}
	if err := next.validate(); err != nil {
		return JournalRecord{}, err
	}
	encoded, err := encodeJournalRecord(next)
	if err != nil {
		return JournalRecord{}, err
	}
	if err := os.MkdirAll(journal.dir, 0o700); err != nil {
		return JournalRecord{}, fmt.Errorf("创建候选生命周期目录: %w", err)
	}
	temporary, err := os.CreateTemp(journal.dir, ".record-*.tmp")
	if err != nil {
		return JournalRecord{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return JournalRecord{}, err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return JournalRecord{}, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return JournalRecord{}, err
	}
	if err := temporary.Close(); err != nil {
		return JournalRecord{}, err
	}
	target := filepath.Join(journal.dir, recordName(next.Revision))
	if err := os.Link(temporaryPath, target); err != nil {
		return JournalRecord{}, fmt.Errorf("提交候选生命周期记录: %w", err)
	}
	return next, nil
}

func (journal *FileJournal) read() (JournalRecord, bool, error) {
	entries, err := os.ReadDir(journal.dir)
	if errors.Is(err, os.ErrNotExist) {
		return JournalRecord{}, false, nil
	}
	if err != nil {
		return JournalRecord{}, false, err
	}
	var revisions []uint64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		value, parseErr := strconv.ParseUint(strings.TrimSuffix(entry.Name(), ".json"), 10, 64)
		if parseErr != nil || value == 0 || entry.Name() != recordName(value) {
			return JournalRecord{}, false, fmt.Errorf("候选生命周期记录名称无效: %s", entry.Name())
		}
		revisions = append(revisions, value)
	}
	if len(revisions) == 0 {
		return JournalRecord{}, false, nil
	}
	sort.Slice(revisions, func(left, right int) bool { return revisions[left] < revisions[right] })
	for index, revision := range revisions {
		if revision != uint64(index+1) {
			return JournalRecord{}, false, errors.New("候选生命周期记录不连续")
		}
	}
	var previous JournalRecord
	for index, revision := range revisions {
		encoded, readErr := os.ReadFile(filepath.Join(journal.dir, recordName(revision)))
		if readErr != nil {
			return JournalRecord{}, false, readErr
		}
		record, decodeErr := decodeJournalRecord(encoded)
		if decodeErr != nil {
			return JournalRecord{}, false, decodeErr
		}
		if record.Revision != revision {
			return JournalRecord{}, false, errors.New("候选生命周期记录修订与文件名不一致")
		}
		if transitionErr := validateTransition(previous, index > 0, record); transitionErr != nil {
			return JournalRecord{}, false, transitionErr
		}
		previous = record
	}
	return previous, true, nil
}

func validateTransition(current JournalRecord, exists bool, next JournalRecord) error {
	if !exists {
		if next.Phase != PhasePrepared {
			return errors.New("候选生命周期必须从准备阶段开始")
		}
		return nil
	}
	if current.Phase == PhaseAccepted || current.Phase == PhaseRolledBack {
		if next.Phase != PhasePrepared || next.Plan == current.Plan {
			return errors.New("已结束的候选生命周期只能开始另一个计划")
		}
		return nil
	}
	if next.Plan != current.Plan || next.Role != current.Role || next.CandidateComponent != current.CandidateComponent ||
		next.BaselineComposition != current.BaselineComposition || next.TargetComposition != current.TargetComposition ||
		next.BaselineKernelGeneration != current.BaselineKernelGeneration || next.ValidationSHA256 != current.ValidationSHA256 {
		return errors.New("候选生命周期记录身份漂移")
	}
	if (current.Phase == PhasePrepared && next.Phase != PhaseObserving && next.Phase != PhaseRolledBack) ||
		(current.Phase == PhaseObserving && next.Phase != PhaseAccepted && next.Phase != PhaseRolledBack) {
		return errors.New("候选生命周期阶段跳转无效")
	}
	return nil
}

func encodeJournalRecord(record JournalRecord) ([]byte, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	encoded, err := json.MarshalIndent(journalEnvelope{
		Schema: journalEnvelopeSchema, Record: record, SHA256: hex.EncodeToString(digest[:]),
	}, "", "  ")
	return append(encoded, '\n'), err
}

func decodeJournalRecord(encoded []byte) (JournalRecord, error) {
	var envelope journalEnvelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return JournalRecord{}, err
	}
	if envelope.Schema != journalEnvelopeSchema || envelope.Record.validate() != nil {
		return JournalRecord{}, errors.New("候选生命周期记录格式无效")
	}
	payload, err := json.Marshal(envelope.Record)
	if err != nil {
		return JournalRecord{}, err
	}
	digest := sha256.Sum256(payload)
	if envelope.SHA256 != hex.EncodeToString(digest[:]) {
		return JournalRecord{}, errors.New("候选生命周期记录完整性校验失败")
	}
	return envelope.Record, nil
}

func recordName(revision uint64) string {
	return fmt.Sprintf("%020d.json", revision)
}
