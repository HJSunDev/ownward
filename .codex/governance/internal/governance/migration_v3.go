package governance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type MigrationValidationReport struct {
	Status             string `json:"status"`
	SourceStateHash    string `json:"source_state_hash"`
	MigratedStateHash  string `json:"migrated_state_hash"`
	CurrentFiles       int    `json:"current_files"`
	RepeatIsIdempotent bool   `json:"repeat_is_idempotent"`
	SourceUnchanged    bool   `json:"source_unchanged"`
}

// ValidateStateMigrationCopy proves migration against a disposable copy of
// the three current-state files. The source directory is read-only and is
// hashed again before the temporary copy is removed.
func (runtime *Runtime) ValidateStateMigrationCopy(sourceDir string) (*MigrationValidationReport, error) {
	sourceDir = filepath.Clean(strings.TrimSpace(sourceDir))
	if sourceDir == "." || !within(runtime.Root, sourceDir) {
		return nil, errors.New("migration source must be a governance directory inside the project")
	}
	stateSource := filepath.Join(sourceDir, "state.json")
	beforeSource, err := os.ReadFile(stateSource)
	if err != nil {
		return nil, err
	}
	sourceHash := sha256Value(beforeSource)
	directory, err := os.MkdirTemp("", "ownward-governance-migration-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	for _, name := range []string{"state.json", "review-request.json", "review.json"} {
		data, readErr := os.ReadFile(filepath.Join(sourceDir, name))
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		if err := atomicWrite(filepath.Join(directory, name), data); err != nil {
			return nil, err
		}
	}
	copyRuntime := &Runtime{Root: runtime.Root, ConfigPath: runtime.ConfigPath, Config: runtime.Config, RuntimeDir: directory}
	if err := copyRuntime.migrateLegacyStateIfNeeded(); err != nil {
		return nil, err
	}
	if _, err := copyRuntime.LoadState(); err != nil {
		return nil, err
	}
	first, err := readCurrentStateFiles(directory)
	if err != nil {
		return nil, err
	}
	if err := copyRuntime.validateCurrentStateRelationships(); err != nil {
		return nil, err
	}
	if err := copyRuntime.migrateLegacyStateIfNeeded(); err != nil {
		return nil, err
	}
	second, err := readCurrentStateFiles(directory)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(first, second) {
		return nil, errors.New("repeated migration changed the three current-state files")
	}
	afterSource, err := os.ReadFile(stateSource)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(beforeSource, afterSource) {
		return nil, errors.New("migration validation modified its source state")
	}
	return &MigrationValidationReport{
		Status: "passed", SourceStateHash: sourceHash, MigratedStateHash: sha256Value(first),
		CurrentFiles: countCurrentStateFiles(directory), RepeatIsIdempotent: true,
		SourceUnchanged: true,
	}, nil
}

func readCurrentStateFiles(directory string) ([]byte, error) {
	var combined []byte
	for _, name := range []string{"state.json", "review-request.json", "review.json"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if errors.Is(err, os.ErrNotExist) {
			combined = append(combined, []byte(name+":missing\n")...)
			continue
		}
		if err != nil {
			return nil, err
		}
		combined = append(combined, []byte(name+":")...)
		combined = append(combined, data...)
	}
	return combined, nil
}

func countCurrentStateFiles(directory string) int {
	count := 0
	for _, name := range []string{"state.json", "review-request.json", "review.json"} {
		if info, err := os.Stat(filepath.Join(directory, name)); err == nil && !info.IsDir() {
			count++
		}
	}
	return count
}

func (runtime *Runtime) validateCurrentStateRelationships() error {
	state, err := runtime.LoadState()
	if err != nil {
		return err
	}
	if state.Review.Status == "idle" {
		return nil
	}
	request, err := runtime.loadRequest()
	if err != nil {
		return fmt.Errorf("non-idle review is missing its current request: %w", err)
	}
	if err := runtime.verifyReviewIntegrity(request, state); err != nil {
		return err
	}
	if oneOf(state.Review.Status, "feedback_ready", "superseded", "responded") {
		var result ReviewResult
		if err := decodeStrictFile(runtime.reviewPath(), &result); err != nil {
			return fmt.Errorf("review state is missing its current feedback: %w", err)
		}
		if !reviewMatchesState(&result, state) {
			return errors.New("current feedback identity does not match migrated state")
		}
	}
	return nil
}

// migrateEfficiencyV3 upgrades the three current-state files as one logical
// unit. The state file is switched last, so an interrupted migration remains
// recognizable as v2 and can be retried safely.
func (runtime *Runtime) migrateEfficiencyV3(rawState []byte) error {
	archiveDir := filepath.Join(runtime.RuntimeDir, "migrations", "efficiency-v3")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return err
	}
	if err := writeOnce(filepath.Join(archiveDir, "state.v2.json"), rawState); err != nil {
		return err
	}

	requestRaw, requestErr := os.ReadFile(runtime.requestPath())
	if requestErr == nil {
		if err := writeOnce(filepath.Join(archiveDir, "review-request.v2.json"), requestRaw); err != nil {
			return err
		}
	} else if !errors.Is(requestErr, os.ErrNotExist) {
		return requestErr
	}
	reviewRaw, reviewErr := os.ReadFile(runtime.reviewPath())
	if reviewErr == nil {
		if err := writeOnce(filepath.Join(archiveDir, "review.v2.json"), reviewRaw); err != nil {
			return err
		}
	} else if !errors.Is(reviewErr, os.ErrNotExist) {
		return reviewErr
	}

	var state State
	if err := json.Unmarshal(rawState, &state); err != nil {
		return err
	}
	state.SchemaVersion = schemaVersion
	if state.InvalidatedEvidence == nil {
		state.InvalidatedEvidence = []EvidenceInvalidation{}
	}
	if state.CurrentFocus != nil && strings.TrimSpace(state.CurrentFocus.ExecutionID) == "" {
		executionID, err := executionIdentity(state.CurrentFocus.FocusID, state.CurrentFocus.SnapshotHash)
		if err != nil {
			return err
		}
		state.CurrentFocus.ExecutionID = executionID
	}
	state.Review.Pending = nil
	state.Review.GovernorAgentID = nil

	var request *ReviewRequest
	if requestErr == nil {
		var migrated ReviewRequest
		if err := json.Unmarshal(requestRaw, &migrated); err != nil {
			return err
		}
		migrated.SchemaVersion = schemaVersion
		if migrated.Trigger.Kind == "advisory" {
			migrated.Trigger.Kind = "legacy"
			migrated.Trigger.Type = "migrated-advisory"
			migrated.Trigger.Reason = "preserved advisory feedback from the v2 runtime; no new review may use this legacy trigger"
		}
		refs, err := runtime.authorityReferences()
		if err != nil {
			return err
		}
		migrated.AuthorityRefs = refs
		if migrated.CurrentFocus != nil && strings.TrimSpace(migrated.CurrentFocus.ExecutionID) == "" {
			executionID, err := executionIdentity(migrated.CurrentFocus.FocusID, migrated.CurrentFocus.SnapshotHash)
			if err != nil {
				return err
			}
			migrated.CurrentFocus.ExecutionID = executionID
		}
		migrated.ProgressDelta = runtime.progressDelta(&state)
		migrated.ReviewSnapshotHash = ""
		hash, err := hashJSON(&migrated)
		if err != nil {
			return err
		}
		migrated.ReviewSnapshotHash = hash
		if err := validateReviewRequest(&migrated); err != nil {
			return fmt.Errorf("migrated review request is invalid: %w", err)
		}
		request = &migrated
		state.Review.ReviewID = stringPointer(migrated.ReviewID)
		state.Review.TriggerInstanceID = stringPointer(migrated.TriggerInstanceID)
		state.Review.ReviewSnapshotHash = stringPointer(migrated.ReviewSnapshotHash)
		state.Review.Trigger = stringPointer(reviewTriggerIdentity(migrated.Trigger))
	}

	var review *ReviewResult
	if reviewErr == nil && request != nil {
		var migrated ReviewResult
		if err := json.Unmarshal(reviewRaw, &migrated); err != nil {
			return err
		}
		migrated.ReviewID = request.ReviewID
		migrated.TriggerInstanceID = request.TriggerInstanceID
		migrated.ReviewSnapshotHash = request.ReviewSnapshotHash
		if migrated.AuthorityClaims == nil {
			migrated.AuthorityClaims = []AuthorityClaim{}
		}
		if migrated.Assumptions == nil {
			migrated.Assumptions = []ReviewAssumption{}
		}
		if err := validateReviewResult(&migrated); err != nil {
			return fmt.Errorf("migrated Governor feedback is invalid: %w", err)
		}
		if err := runtime.validateReviewResultForRequest(&migrated, request); err != nil {
			return fmt.Errorf("migrated Governor feedback is not bound to its request: %w", err)
		}
		review = &migrated
	}

	if state.Review.Status == "responded" && state.Review.ReviewID != nil {
		state.ReviewBaseline = runtime.migratedBaseline(&state, request)
	}
	if request != nil {
		if err := atomicWriteJSON(runtime.requestPath(), request); err != nil {
			return err
		}
	}
	if review != nil {
		if err := atomicWriteJSON(runtime.reviewPath(), review); err != nil {
			return err
		}
	}
	if err := runtime.saveState(&state); err != nil {
		return err
	}
	return runtime.finalizeEfficiencyV3Migration()
}

func executionIdentity(focusID, snapshotHash string) (string, error) {
	hash, err := hashJSON(map[string]string{"focus_id": strings.TrimSpace(focusID), "snapshot_hash": strings.TrimSpace(snapshotHash)})
	if err != nil {
		return "", err
	}
	return "execution_" + strings.TrimPrefix(hash, "sha256:")[:32], nil
}

func (runtime *Runtime) migratedBaseline(state *State, request *ReviewRequest) *ReviewBaseline {
	if state == nil || state.Review.ReviewID == nil {
		return nil
	}
	establishedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if state.Review.Response != nil && strings.TrimSpace(state.Review.Response.RespondedAt) != "" {
		establishedAt = state.Review.Response.RespondedAt
	} else if request != nil && strings.TrimSpace(request.CreatedAt) != "" {
		establishedAt = request.CreatedAt
	}
	checkpointOutcome := "not_applicable"
	var focusID, focusHash, focusExecutionID, checkpointID *string
	if state.CurrentFocus != nil {
		focusID = stringPointer(state.CurrentFocus.FocusID)
		focusHash = stringPointer(state.CurrentFocus.SnapshotHash)
		focusExecutionID = stringPointer(state.CurrentFocus.ExecutionID)
		checkpointID = stringPointer(state.CurrentFocus.EvidenceCheckpoint.CheckpointID)
		if state.CurrentFocus.EvidenceCheckpoint.Reached {
			checkpointOutcome = "passed"
		} else if len(state.CurrentFocus.FailureEvents) > 0 {
			checkpointOutcome = "failed"
		} else {
			checkpointOutcome = "pending"
		}
	}
	identity, _ := hashJSON(map[string]any{
		"review_id": *state.Review.ReviewID, "authority_hash": state.AuthorityHash,
		"focus_id": focusID, "focus_snapshot_hash": focusHash, "focus_execution_id": focusExecutionID,
		"conditions": state.CompletionConditions, "evidence_refs": runtime.evidenceReferencesForCondition(state, criticalConditionID(state)),
		"checkpoint_id": checkpointID, "checkpoint_outcome": checkpointOutcome,
	})
	return &ReviewBaseline{
		BaselineID: "baseline_" + strings.TrimPrefix(identity, "sha256:")[:24], ReviewID: *state.Review.ReviewID,
		AuthorityHash: state.AuthorityHash, FocusID: focusID, FocusSnapshotHash: focusHash, FocusExecutionID: focusExecutionID,
		Conditions: append([]CompletionCondition(nil), state.CompletionConditions...), EvidenceRefs: runtime.evidenceReferencesForCondition(state, criticalConditionID(state)),
		CheckpointID: checkpointID, CheckpointOutcome: checkpointOutcome, EstablishedAt: establishedAt,
	}
}

func (runtime *Runtime) finalizeEfficiencyV3Migration() error {
	dir := filepath.Join(runtime.RuntimeDir, "migrations", "efficiency-v3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	marker := filepath.Join(dir, "migration.json")
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWriteJSON(marker, map[string]any{"schema_version": schemaVersion, "migration": "efficiency-v3", "status": "complete"})
}
