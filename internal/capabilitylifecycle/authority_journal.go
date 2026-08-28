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
)

const (
	AuthorityRecordSchema       = "ownward.authority-persistence-lifecycle-record/v1"
	AuthorityPhaseReady         = "ready"
	AuthorityPhaseSwitching     = "switching"
	AuthorityPhaseObserving     = "observing"
	AuthorityPhaseRollbackReady = "rollback-ready"
	AuthorityPhaseAccepted      = "accepted"
	AuthorityPhaseRolledBack    = "rolled-back"
	authorityEnvelopeSchema     = "ownward.authority-persistence-lifecycle-envelope/v1"
)

type AuthorityRecord struct {
	Schema            string                       `json:"schema"`
	Revision          uint64                       `json:"revision"`
	Plan              string                       `json:"plan"`
	Phase             string                       `json:"phase"`
	Baseline          AuthorityPersistenceSnapshot `json:"baseline"`
	Candidate         AuthorityPersistenceSnapshot `json:"candidate"`
	CandidateFormat   string                       `json:"candidate_format"`
	RecoveryBackup    string                       `json:"recovery_backup_sha256,omitempty"`
	ObservationSHA256 string                       `json:"observation_sha256,omitempty"`
}

type AuthorityJournal interface {
	Read() (AuthorityRecord, bool, error)
	Append(uint64, AuthorityRecord) (AuthorityRecord, error)
}

type authorityRecordEnvelope struct {
	Schema string          `json:"schema"`
	Record AuthorityRecord `json:"record"`
	SHA256 string          `json:"sha256"`
}

type FileAuthorityJournal struct {
	mu  sync.Mutex
	dir string
}

func OpenAuthorityJournal(dir string) (*FileAuthorityJournal, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("权威持久化生命周期目录必须是绝对路径")
	}
	return &FileAuthorityJournal{dir: filepath.Clean(dir)}, nil
}

func (journal *FileAuthorityJournal) Read() (AuthorityRecord, bool, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.read()
}

func (journal *FileAuthorityJournal) Append(expected uint64, next AuthorityRecord) (AuthorityRecord, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	current, exists, err := journal.read()
	if err != nil {
		return AuthorityRecord{}, err
	}
	actual := uint64(0)
	if exists {
		actual = current.Revision
	}
	if actual != expected || next.Revision != expected+1 {
		return AuthorityRecord{}, fmt.Errorf("权威持久化检查点冲突，当前修订为 %d", actual)
	}
	if err := validateAuthorityRecordTransition(current, exists, next); err != nil {
		return AuthorityRecord{}, err
	}
	encoded, err := encodeAuthorityRecord(next)
	if err != nil {
		return AuthorityRecord{}, err
	}
	if err := os.MkdirAll(journal.dir, 0o700); err != nil {
		return AuthorityRecord{}, err
	}
	temporary, err := os.CreateTemp(journal.dir, ".authority-record-*.tmp")
	if err != nil {
		return AuthorityRecord{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return AuthorityRecord{}, err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return AuthorityRecord{}, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return AuthorityRecord{}, err
	}
	if err := temporary.Close(); err != nil {
		return AuthorityRecord{}, err
	}
	if err := os.Link(temporaryPath, filepath.Join(journal.dir, authorityRecordName(next.Revision))); err != nil {
		return AuthorityRecord{}, fmt.Errorf("提交权威持久化检查点: %w", err)
	}
	return next, nil
}

func (journal *FileAuthorityJournal) read() (AuthorityRecord, bool, error) {
	entries, err := os.ReadDir(journal.dir)
	if errors.Is(err, os.ErrNotExist) {
		return AuthorityRecord{}, false, nil
	}
	if err != nil {
		return AuthorityRecord{}, false, err
	}
	var revisions []uint64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		revision, parseErr := strconv.ParseUint(strings.TrimSuffix(entry.Name(), ".json"), 10, 64)
		if parseErr != nil || revision == 0 || entry.Name() != authorityRecordName(revision) {
			return AuthorityRecord{}, false, fmt.Errorf("权威持久化检查点名称无效: %s", entry.Name())
		}
		revisions = append(revisions, revision)
	}
	if len(revisions) == 0 {
		return AuthorityRecord{}, false, nil
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i] < revisions[j] })
	var previous AuthorityRecord
	for index, revision := range revisions {
		if revision != uint64(index+1) {
			return AuthorityRecord{}, false, errors.New("权威持久化检查点不连续")
		}
		encoded, err := os.ReadFile(filepath.Join(journal.dir, authorityRecordName(revision)))
		if err != nil {
			return AuthorityRecord{}, false, err
		}
		record, err := decodeAuthorityRecord(encoded)
		if err != nil || record.Revision != revision {
			return AuthorityRecord{}, false, errors.New("权威持久化检查点损坏")
		}
		if err := validateAuthorityRecordTransition(previous, index > 0, record); err != nil {
			return AuthorityRecord{}, false, err
		}
		previous = record
	}
	return previous, true, nil
}

func (record AuthorityRecord) validate() error {
	if record.Schema != AuthorityRecordSchema || record.Revision == 0 || !isSHA256(record.Plan) || strings.TrimSpace(record.CandidateFormat) == "" ||
		validateAuthoritySnapshot(record.Baseline) != nil || validateAuthoritySnapshot(record.Candidate) != nil {
		return errors.New("权威持久化检查点内容无效")
	}
	switch record.Phase {
	case AuthorityPhaseReady, AuthorityPhaseSwitching, AuthorityPhaseObserving:
		if record.ObservationSHA256 != "" {
			return errors.New("未结束的权威持久化检查点包含观察结论")
		}
	case AuthorityPhaseRollbackReady, AuthorityPhaseAccepted, AuthorityPhaseRolledBack:
		if !isSHA256(record.ObservationSHA256) {
			return errors.New("权威持久化终态缺少观察证据")
		}
	default:
		return errors.New("权威持久化阶段无效")
	}
	if record.RecoveryBackup != "" && !isSHA256(record.RecoveryBackup) {
		return errors.New("权威持久化恢复备份摘要无效")
	}
	return nil
}

func validateAuthorityRecordTransition(current AuthorityRecord, exists bool, next AuthorityRecord) error {
	if err := next.validate(); err != nil {
		return err
	}
	if !exists {
		if next.Phase != AuthorityPhaseReady {
			return errors.New("权威持久化候选必须从 ready 开始")
		}
		return nil
	}
	if current.Phase == AuthorityPhaseAccepted || current.Phase == AuthorityPhaseRolledBack {
		if next.Phase != AuthorityPhaseReady || next.Plan == current.Plan {
			return errors.New("已结束的权威持久化生命周期只能开始新计划")
		}
		return nil
	}
	if next.Plan != current.Plan || next.CandidateFormat != current.CandidateFormat {
		return errors.New("权威持久化检查点身份漂移")
	}
	allowed := false
	switch current.Phase {
	case AuthorityPhaseReady:
		allowed = next.Phase == AuthorityPhaseReady || next.Phase == AuthorityPhaseSwitching
	case AuthorityPhaseSwitching:
		allowed = next.Phase == AuthorityPhaseSwitching || next.Phase == AuthorityPhaseObserving
	case AuthorityPhaseObserving:
		allowed = next.Phase == AuthorityPhaseObserving || next.Phase == AuthorityPhaseRollbackReady || next.Phase == AuthorityPhaseAccepted
	case AuthorityPhaseRollbackReady:
		allowed = next.Phase == AuthorityPhaseRollbackReady || next.Phase == AuthorityPhaseRolledBack
	}
	if !allowed {
		return fmt.Errorf("非法权威持久化阶段迁移: %s -> %s", current.Phase, next.Phase)
	}
	return nil
}

func encodeAuthorityRecord(record AuthorityRecord) ([]byte, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	encoded, err := json.MarshalIndent(authorityRecordEnvelope{Schema: authorityEnvelopeSchema, Record: record, SHA256: hex.EncodeToString(digest[:])}, "", "  ")
	return append(encoded, '\n'), err
}

func decodeAuthorityRecord(encoded []byte) (AuthorityRecord, error) {
	var envelope authorityRecordEnvelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return AuthorityRecord{}, err
	}
	payload, err := json.Marshal(envelope.Record)
	if err != nil {
		return AuthorityRecord{}, err
	}
	digest := sha256.Sum256(payload)
	if envelope.Schema != authorityEnvelopeSchema || envelope.SHA256 != hex.EncodeToString(digest[:]) || envelope.Record.validate() != nil {
		return AuthorityRecord{}, errors.New("权威持久化检查点封装无效")
	}
	return envelope.Record, nil
}

func authorityRecordName(revision uint64) string { return fmt.Sprintf("%020d.json", revision) }
