package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

const invalidStopReviewMigrationID = "invalid-main-agent-stop-v1"

type invalidStopReviewMigration struct {
	SchemaVersion             int      `json:"schema_version"`
	MigrationID               string   `json:"migration_id"`
	Status                    string   `json:"status"`
	OldReviewID               string   `json:"old_review_id"`
	OldTriggerInstanceID      string   `json:"old_trigger_instance_id"`
	OldReviewSnapshotHash     string   `json:"old_review_snapshot_hash"`
	OldDecisionPath           *string  `json:"old_decision_path"`
	RecoveryReviewID          *string  `json:"recovery_review_id"`
	CorrectionIndexPath       string   `json:"correction_index_path"`
	PreservedGovernanceHash   string   `json:"preserved_governance_hash"`
	CorrectedHistoricalReview []string `json:"corrected_historical_review_ids"`
	CreatedAt                 string   `json:"created_at"`
	CompletedAt               *string  `json:"completed_at"`
}

type invalidStopReviewCorrectionIndex struct {
	SchemaVersion int      `json:"schema_version"`
	MigrationID   string   `json:"migration_id"`
	ReviewIDs     []string `json:"review_ids"`
	Meaning       string   `json:"meaning"`
	CreatedAt     string   `json:"created_at"`
}

func (runtime *Runtime) MigrateInvalidStopReview() (*ReviewRequest, error) {
	return runtime.migrateInvalidStopReview("")
}

func (runtime *Runtime) migrateInvalidStopReview(failAfter string) (*ReviewRequest, error) {
	var recovery *ReviewRequest
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		markerPath := filepath.Join(runtime.RuntimeDir, invalidStopReviewMigrationID+".json")
		marker, err := runtime.loadInvalidStopMigration(markerPath)
		if err != nil {
			return err
		}
		if marker != nil && marker.Status == "complete" {
			if state.Review.ReviewID == nil || marker.RecoveryReviewID == nil || *state.Review.ReviewID != *marker.RecoveryReviewID {
				return errors.New("completed invalid Stop migration is not bound to its recovery review")
			}
			changed, err := runtime.ensureStopCorrectionIndex(marker)
			if err != nil {
				return err
			}
			if changed {
				if err := atomicWriteJSON(markerPath, marker); err != nil {
					return err
				}
			}
			recovery, err = runtime.loadRequest()
			return err
		}

		if marker == nil {
			if state.Review.Trigger == nil || *state.Review.Trigger != "event:main-agent-stop" || !state.Review.Required || state.Review.ReviewID == nil || state.Review.TriggerInstanceID == nil || state.Review.ReviewSnapshotHash == nil {
				return errors.New("current review is not the exact legacy event:main-agent-stop state")
			}
			rawRequest, err := readLegacyStopRequest(runtime.requestPath())
			if err != nil {
				return err
			}
			if rawRequest.ReviewID != *state.Review.ReviewID || rawRequest.TriggerInstanceID != *state.Review.TriggerInstanceID || rawRequest.ReviewSnapshotHash != *state.Review.ReviewSnapshotHash {
				return errors.New("legacy Stop request identity does not match current state")
			}
			preservedHash, err := governanceProgressHash(state)
			if err != nil {
				return err
			}
			ids, err := runtime.legacyStopReviewIDs()
			if err != nil {
				return err
			}
			if !containsString(ids, rawRequest.ReviewID) {
				ids = append(ids, rawRequest.ReviewID)
				sort.Strings(ids)
			}
			marker = &invalidStopReviewMigration{
				SchemaVersion: schemaVersion, MigrationID: invalidStopReviewMigrationID, Status: "prepared",
				OldReviewID: rawRequest.ReviewID, OldTriggerInstanceID: rawRequest.TriggerInstanceID,
				OldReviewSnapshotHash: rawRequest.ReviewSnapshotHash, OldDecisionPath: state.Review.DecisionPath,
				CorrectionIndexPath:     runtime.invalidStopCorrectionIndexPath(rawRequest.ReviewID),
				PreservedGovernanceHash: preservedHash, CorrectedHistoricalReview: ids,
				CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}
			if err := atomicWriteJSON(markerPath, marker); err != nil {
				return err
			}
			if failAfter == "prepared" {
				return errors.New("injected migration failure after prepared")
			}
		}

		archivePath := filepath.Join(runtime.reviewsDir(), "superseded", marker.OldReviewID+".json")
		if marker.Status == "prepared" {
			if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
				return err
			}
			if _, err := os.Stat(archivePath); errors.Is(err, os.ErrNotExist) {
				data, err := os.ReadFile(runtime.requestPath())
				if err != nil {
					return fmt.Errorf("read legacy Stop request: %w", err)
				}
				if err := atomicWrite(archivePath, data); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if marker.OldDecisionPath != nil {
				var result ReviewResult
				if err := decodeStrictFile(resolvePath(runtime.Root, *marker.OldDecisionPath), &result); err != nil {
					return fmt.Errorf("validate accepted legacy Stop result: %w", err)
				}
				if result.ReviewID != marker.OldReviewID || result.TriggerInstanceID != marker.OldTriggerInstanceID || result.ReviewSnapshotHash != marker.OldReviewSnapshotHash {
					return errors.New("accepted legacy Stop result identity is inconsistent")
				}
			}
			if _, err := runtime.ensureStopCorrectionIndex(marker); err != nil {
				return err
			}
			marker.Status = "archived"
			if err := atomicWriteJSON(markerPath, marker); err != nil {
				return err
			}
			if failAfter == "archived" {
				return errors.New("injected migration failure after archived")
			}
		}

		if marker.Status == "archived" {
			legacyStillActive := state.Review.Trigger != nil && *state.Review.Trigger == "event:main-agent-stop" && state.Review.ReviewID != nil && *state.Review.ReviewID == marker.OldReviewID
			alreadyCleared := !state.Review.Required && state.Review.ReviewID == nil && state.Review.Trigger == nil
			if !legacyStillActive && !alreadyCleared {
				return errors.New("legacy Stop state changed during migration")
			}
			if legacyStillActive {
				state.Review = ReviewState{FixedReviewGeneration: state.Review.FixedReviewGeneration}
				if state.CurrentWorkPacket != nil {
					state.CurrentWorkPacket.Approval = nil
				}
				next := "request one runtime-repair recovery review for the preserved work packet"
				state.NextAction = &next
				if err := runtime.saveState(state); err != nil {
					return err
				}
				if failAfter == "state_saved" {
					return errors.New("injected migration failure after state_saved")
				}
			}
			marker.Status = "state_cleared"
			if err := atomicWriteJSON(markerPath, marker); err != nil {
				return err
			}
			if failAfter == "state_cleared" {
				return errors.New("injected migration failure after state_cleared")
			}
		}

		state, err = runtime.LoadState()
		if err != nil {
			return err
		}
		if marker.Status == "state_cleared" {
			preservedHash, err := governanceProgressHash(state)
			if err != nil || preservedHash != marker.PreservedGovernanceHash {
				return errors.New("governance progress changed during invalid Stop migration")
			}
			trigger, err := newReviewTrigger("fixed", "runtime-repair-recovery", invalidStopReviewMigrationID, "recover from invalid main-agent-stop state")
			if err != nil {
				return err
			}
			recovery, err = runtime.requestReviewLocked(state, trigger, false)
			if err != nil {
				return err
			}
			if recovery == nil {
				return errors.New("migration did not create its recovery review")
			}
			if failAfter == "recovery_saved" {
				return errors.New("injected migration failure after recovery_saved")
			}
			marker.RecoveryReviewID = stringPointer(recovery.ReviewID)
			marker.Status = "recovery_created"
			if err := atomicWriteJSON(markerPath, marker); err != nil {
				return err
			}
			if failAfter == "recovery_created" {
				return errors.New("injected migration failure after recovery_created")
			}
		}

		if marker.Status == "recovery_created" {
			if recovery == nil {
				recovery, err = runtime.loadRequest()
				if err != nil {
					return err
				}
			}
			if marker.RecoveryReviewID == nil || recovery.ReviewID != *marker.RecoveryReviewID || recovery.Trigger.Kind != "fixed" || recovery.Trigger.Type != "runtime-repair-recovery" {
				return errors.New("migration recovery review identity is inconsistent")
			}
			if !runtime.invalidStopMigrationEventExists() {
				if err := runtime.appendEvent("invalid_stop_review_migrated", state.RunID, "archived invalid main-agent-stop reviews and created one recovery review", map[string]any{"migration_id": invalidStopReviewMigrationID, "old_review_id": marker.OldReviewID, "recovery_review_id": recovery.ReviewID}); err != nil {
					return err
				}
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			marker.CompletedAt = &now
			marker.Status = "complete"
			return atomicWriteJSON(markerPath, marker)
		}
		return fmt.Errorf("unsupported invalid Stop migration stage %q", marker.Status)
	})
	return recovery, err
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

type legacyStopRequestIdentity struct {
	ReviewID           string
	TriggerInstanceID  string
	ReviewSnapshotHash string
}

func readLegacyStopRequest(path string) (*legacyStopRequestIdentity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	trigger, _ := value["trigger"].(map[string]any)
	if trigger["kind"] != "event" || trigger["reason"] != "main-agent-stop" {
		return nil, errors.New("review request is not the exact legacy main-agent-stop trigger")
	}
	identity := &legacyStopRequestIdentity{
		ReviewID: fmt.Sprint(value["review_id"]), TriggerInstanceID: fmt.Sprint(value["trigger_instance_id"]),
		ReviewSnapshotHash: fmt.Sprint(value["review_snapshot_hash"]),
	}
	if identity.ReviewID == "" || identity.TriggerInstanceID == "" || identity.ReviewSnapshotHash == "" {
		return nil, errors.New("legacy Stop request identity is incomplete")
	}
	return identity, nil
}

func governanceProgressHash(state *State) (string, error) {
	return hashJSON(map[string]any{
		"owner": state.Owner, "work_packet": state.CurrentWorkPacket, "conditions": state.CompletionConditions,
		"results": state.ReusableResults, "intervention": state.PendingIntervention,
	})
}

func (runtime *Runtime) loadInvalidStopMigration(path string) (*invalidStopReviewMigration, error) {
	var marker invalidStopReviewMigration
	if err := decodeStrictFile(path, &marker); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if marker.SchemaVersion != schemaVersion || marker.MigrationID != invalidStopReviewMigrationID {
		return nil, errors.New("invalid Stop migration marker identity is invalid")
	}
	return &marker, nil
}

func (runtime *Runtime) ensureStopCorrectionIndex(marker *invalidStopReviewMigration) (bool, error) {
	if marker == nil || marker.OldReviewID == "" {
		return false, errors.New("invalid Stop correction index requires a migration marker")
	}
	expectedPath := runtime.invalidStopCorrectionIndexPath(marker.OldReviewID)
	changed := marker.CorrectionIndexPath != expectedPath
	marker.CorrectionIndexPath = expectedPath
	path := resolvePath(runtime.Root, expectedPath)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		index := invalidStopReviewCorrectionIndex{
			SchemaVersion: schemaVersion, MigrationID: invalidStopReviewMigrationID,
			ReviewIDs: normalizeStrings(marker.CorrectedHistoricalReview),
			Meaning:   "These legacy reviews were created by the invalid main-agent-stop trigger and remain audit-only; they cannot authorize current work.",
			CreatedAt: marker.CreatedAt,
		}
		return true, atomicWriteJSON(path, &index)
	} else if err != nil {
		return false, err
	}
	var existing invalidStopReviewCorrectionIndex
	if err := decodeStrictFile(path, &existing); err != nil {
		return false, err
	}
	if existing.SchemaVersion != schemaVersion || existing.MigrationID != invalidStopReviewMigrationID || !slices.Equal(existing.ReviewIDs, normalizeStrings(marker.CorrectedHistoricalReview)) {
		return false, errors.New("invalid Stop correction index conflicts with its immutable migration facts")
	}
	return changed, nil
}

func (runtime *Runtime) invalidStopCorrectionIndexPath(reviewID string) string {
	path := filepath.Join(runtime.reviewsDir(), "corrections", invalidStopReviewMigrationID+"-"+reviewID+".json")
	return filepath.ToSlash(mustRelative(runtime.Root, path))
}

func (runtime *Runtime) legacyStopReviewIDs() ([]string, error) {
	lines, err := readJSONLines(runtime.eventsPath())
	if err != nil {
		return nil, err
	}
	ids := map[string]struct{}{}
	for _, line := range lines {
		var event Event
		if json.Unmarshal(line, &event) != nil || event.Kind != "review_requested" {
			continue
		}
		trigger, _ := event.Fields["trigger"].(map[string]any)
		if trigger["kind"] == "event" && trigger["reason"] == "main-agent-stop" {
			if id, ok := event.Fields["review_id"].(string); ok && id != "" {
				ids[id] = struct{}{}
			}
		}
	}
	values := make([]string, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	sort.Strings(values)
	return values, nil
}

func (runtime *Runtime) invalidStopMigrationEventExists() bool {
	lines, err := readJSONLines(runtime.eventsPath())
	if err != nil {
		return false
	}
	for _, line := range lines {
		var event Event
		if json.Unmarshal(line, &event) == nil && event.Kind == "invalid_stop_review_migrated" && event.Fields["migration_id"] == invalidStopReviewMigrationID {
			return true
		}
	}
	return false
}
