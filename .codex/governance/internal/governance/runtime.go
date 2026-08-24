package governance

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var tableSeparator = regexp.MustCompile(`^\s*:?-{3,}:?\s*$`)

func (runtime *Runtime) Init() (*State, error) {
	var created *State
	err := runtime.withLock(func() error {
		if runtime.StateExists() {
			return errors.New("governance state already exists; init will not overwrite it")
		}
		authorityHash, err := runtime.authorityHash()
		if err != nil {
			return err
		}
		conditions, err := runtime.readCompletionConditions()
		if err != nil {
			return err
		}
		next := "restore repository facts and choose the most valuable next validation point"
		state := &State{
			SchemaVersion: schemaVersion, RunID: newID("run"), Status: "active", AuthorityHash: authorityHash,
			CompletionConditions: conditions, CurrentFocus: nil, PendingIntervention: nil,
			ExplicitResourceConstraints: runtime.Config.ExplicitResourceConstraints, ReusableResults: []ReusableResult{},
			NextAction: &next, ReviewBaseline: nil, InvalidatedEvidence: []EvidenceInvalidation{},
			Review: ReviewState{Status: "idle"}, Owner: nil, Handoff: nil,
		}
		if err := runtime.saveState(state); err != nil {
			return err
		}
		created = state
		return nil
	})
	return created, err
}

func (runtime *Runtime) readCompletionConditions() ([]CompletionCondition, error) {
	var conditions []CompletionCondition
	for _, configured := range runtime.Config.CompletionDefinitionPaths {
		file, err := os.Open(resolvePath(runtime.Root, configured))
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
				continue
			}
			columns := strings.Split(strings.Trim(line, "|"), "|")
			if len(columns) < 3 {
				continue
			}
			first, second := strings.TrimSpace(columns[0]), strings.TrimSpace(columns[1])
			if tableSeparator.MatchString(first) || first == "编号" {
				continue
			}
			if _, err := strconv.Atoi(first); err == nil && second != "" {
				conditions = append(conditions, CompletionCondition{ConditionID: "condition:" + first + ":" + second, Status: "unmet"})
			}
		}
		scanErr := scanner.Err()
		_ = file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	if len(conditions) == 0 {
		return nil, errors.New("completion definition contains no numbered condition table")
	}
	return conditions, nil
}

func (runtime *Runtime) RequestFixedReview(triggerType, sourceID string) (*ReviewRequest, error) {
	trigger, err := newReviewTrigger("fixed", triggerType, sourceID, "")
	if err != nil {
		return nil, err
	}
	var request *ReviewRequest
	err = runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.Status == "complete" {
			return nil
		}
		currentAuthority, err := runtime.authorityHash()
		if err != nil {
			return err
		}
		if currentAuthority != state.AuthorityHash {
			previous := state.AuthorityHash
			state.AuthorityHash = currentAuthority
			trigger, err = newReviewTrigger("event", "authority-change", previous+":"+currentAuthority, "authority changed")
			if err != nil {
				return err
			}
		}
		created, err := runtime.requestReviewLocked(state, trigger)
		if err != nil {
			return err
		}
		request = created
		return nil
	})
	return request, err
}

// RequestLifecycleReview creates one review for each real lifecycle boundary.
// SessionStart has no event identifier, so retries are deduplicated while a
// review is active; after that review is answered or missed, the next hook
// invocation is a new boundary and receives the next persisted generation.
func (runtime *Runtime) RequestLifecycleReview(triggerType, sourceID string) (*ReviewRequest, error) {
	var request *ReviewRequest
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.Status == "complete" {
			return nil
		}
		if oneOf(state.Review.Status, "requested", "feedback_ready", "superseded") {
			existing, loadErr := runtime.loadRequest()
			if loadErr == nil && runtime.verifyReviewIntegrity(existing, state) == nil {
				request = existing
				return nil
			}
			state.Review.Status = "missed"
			state.Review.Response = nil
		}
		currentAuthority, err := runtime.authorityHash()
		if err != nil {
			return err
		}
		var trigger ReviewTrigger
		if currentAuthority != state.AuthorityHash {
			previous := state.AuthorityHash
			state.AuthorityHash = currentAuthority
			trigger, err = newReviewTrigger("event", "authority-change", previous+":"+currentAuthority, "authority changed")
		} else {
			generationSource := fmt.Sprintf("%s:generation:%d", sourceID, state.Review.FixedReviewGeneration+1)
			trigger, err = newReviewTrigger("fixed", triggerType, generationSource, "")
		}
		if err != nil {
			return err
		}
		created, err := runtime.requestReviewLocked(state, trigger)
		if err != nil {
			return err
		}
		request = created
		return nil
	})
	return request, err
}

func (runtime *Runtime) UpdateExecutionSnapshot(input ExecutionSnapshotInput) (*ReviewRequest, error) {
	if err := validateExecutionSnapshotInput(&input); err != nil {
		return nil, err
	}
	var request *ReviewRequest
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		invalidEvidence, err := runtime.reconcileEvidenceIdentitiesLocked(state)
		if err != nil {
			return err
		}
		if state.Status == "complete" {
			return errors.New("completed governance state cannot accept a new execution snapshot")
		}
		if !hasCondition(state, input.ConditionID) {
			return fmt.Errorf("unknown completion condition %q", input.ConditionID)
		}
		snapshot, err := snapshotFromInput(&input)
		if err != nil {
			return err
		}
		if state.CurrentFocus != nil && state.CurrentFocus.FocusID == snapshot.FocusID {
			if state.CurrentFocus.SnapshotHash == snapshot.SnapshotHash {
				return nil
			}
			snapshot.StartedAt = state.CurrentFocus.StartedAt
			snapshot.LastEvidenceAt = state.CurrentFocus.LastEvidenceAt
			snapshot.Checkpoint = state.CurrentFocus.Checkpoint
			snapshot.FailureEvents = state.CurrentFocus.FailureEvents
			snapshot.FailureRepairs = state.CurrentFocus.FailureRepairs
		}
		state.CurrentFocus = snapshot
		state.NextAction = stringPointer(input.Objective)
		reusedEvidence := evidenceForFocus(snapshot, state.ReusableResults)
		if len(reusedEvidence) == len(snapshot.ExpectedEvidence) && len(snapshot.ExpectedEvidence) > 0 {
			now := time.Now().UTC().Format(time.RFC3339Nano)
			snapshot.LastEvidenceAt = &now
			snapshot.EvidenceCheckpoint.Reached = true
			snapshot.Checkpoint = &snapshot.EvidenceCheckpoint.CheckpointID
		}
		setConditionStatus(state, input.ConditionID, "in_progress", reusedEvidence)
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if len(invalidEvidence) == 0 {
			return nil
		}
		trigger, err := evidenceIdentityChangeTrigger(invalidEvidence)
		if err != nil {
			return err
		}
		request, err = runtime.requestReviewLocked(state, trigger)
		return err
	})
	return request, err
}

func snapshotFromInput(input *ExecutionSnapshotInput) (*ExecutionSnapshot, error) {
	copy := *input
	copy.InvolvedScope = normalizeStrings(copy.InvolvedScope)
	copy.ExpectedEvidence = normalizeStrings(copy.ExpectedEvidence)
	hash, err := hashJSON(copy)
	if err != nil {
		return nil, err
	}
	executionHash, err := hashJSON(map[string]string{"focus_id": input.FocusID, "snapshot_hash": hash})
	if err != nil {
		return nil, err
	}
	return &ExecutionSnapshot{
		FocusID: input.FocusID, ConditionID: input.ConditionID, Objective: input.Objective, Value: input.Value,
		InvolvedScope: copy.InvolvedScope, ExpectedEvidence: copy.ExpectedEvidence,
		EvidenceCheckpoint: EvidenceCheckpoint{CheckpointID: input.CheckpointID, Description: input.CheckpointDescription},
		SnapshotHash:       hash, ExecutionID: "execution_" + strings.TrimPrefix(executionHash, "sha256:")[:32],
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), FailureEvents: []FailureEvent{}, FailureRepairs: []FailureRepair{},
	}, nil
}

func (runtime *Runtime) RecordEvidence(record EvidenceRecord) (*ReviewRequest, error) {
	if err := nonempty("evidence_id", record.EvidenceID); err != nil {
		return nil, err
	}
	if record.ValidatorStatus != "passed" || strings.TrimSpace(record.ValidatorSource) == "" {
		return nil, errors.New("evidence requires an explicit passed validator and validator source")
	}
	path := resolvePath(runtime.Root, record.Path)
	if !runtime.allowedEvidencePath(path) {
		return nil, fmt.Errorf("evidence path is outside configured evidence roots: %s", record.Path)
	}
	hash, err := fileHash(path)
	if err != nil {
		return nil, err
	}
	if record.InputHash != "" && record.InputHash != hash {
		return nil, errors.New("evidence input_hash does not match file")
	}
	var request *ReviewRequest
	err = runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		invalidEvidence, err := runtime.reconcileEvidenceIdentitiesLocked(state)
		if err != nil {
			return err
		}
		if state.CurrentFocus == nil {
			return errors.New("evidence requires a current execution snapshot")
		}
		alreadyKnown := false
		for _, existing := range state.ReusableResults {
			if existing.ResultID == record.EvidenceID {
				if existing.InputHash == hash && filepath.Clean(resolvePath(runtime.Root, existing.EvidencePath)) == filepath.Clean(path) {
					alreadyKnown = true
					break
				}
				return fmt.Errorf("evidence id %q already identifies different content", record.EvidenceID)
			}
		}
		if !alreadyKnown {
			state.ReusableResults = append(state.ReusableResults, ReusableResult{ResultID: record.EvidenceID, Scope: normalizeStrings(record.Scope), EvidencePath: filepath.ToSlash(record.Path), InputHash: hash})
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		state.CurrentFocus.LastEvidenceAt = &now
		setConditionStatus(state, state.CurrentFocus.ConditionID, "in_progress", []string{record.EvidenceID})
		reached := expectedEvidenceReached(state.CurrentFocus, state.ReusableResults)
		if reached {
			state.CurrentFocus.EvidenceCheckpoint.Reached = true
			state.CurrentFocus.Checkpoint = &state.CurrentFocus.EvidenceCheckpoint.CheckpointID
		}
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if len(invalidEvidence) > 0 {
			trigger, err := evidenceIdentityChangeTrigger(invalidEvidence)
			if err != nil {
				return err
			}
			request, err = runtime.requestReviewLocked(state, trigger)
			return err
		}
		return nil
	})
	return request, err
}

// RecordCheckpointResult is the trusted fallback when Codex PostToolUse does
// not expose a reliable process result. It records one already-existing
// evidence boundary, not every command executed by the main Agent.
func (runtime *Runtime) RecordCheckpointResult(input CheckpointResultInput) (*ReviewRequest, error) {
	input.FailureCategory = normalizeFailureSignature(input.FailureCategory)
	if err := validateCheckpointResultInput(&input); err != nil {
		return nil, err
	}
	var request *ReviewRequest
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		focus := state.CurrentFocus
		if focus == nil || focus.FocusID != input.FocusID || focus.EvidenceCheckpoint.CheckpointID != input.CheckpointID {
			return errors.New("checkpoint result is not bound to the current execution")
		}
		if err := ensureKnownEvidence(state, input.EvidenceIDs); err != nil {
			return err
		}
		if input.Outcome == "passed" {
			if !expectedEvidenceReached(focus, state.ReusableResults) {
				return errors.New("passed checkpoint does not have all expected evidence")
			}
			focus.EvidenceCheckpoint.Reached = true
			focus.Checkpoint = stringPointer(input.CheckpointID)
			if err := runtime.saveState(state); err != nil {
				return err
			}
			return nil
		}

		identities, err := runtime.currentFailureIdentities()
		if err != nil {
			return err
		}
		generation := 0
		for _, repair := range focus.FailureRepairs {
			if repair.Signature == input.FailureCategory && repair.RepairGeneration > generation {
				generation = repair.RepairGeneration
			}
		}
		identity, err := hashJSON(map[string]any{"execution_id": focus.ExecutionID, "checkpoint_id": input.CheckpointID, "failure_category": input.FailureCategory, "repair_generation": generation})
		if err != nil {
			return err
		}
		eventID := "failure_" + strings.TrimPrefix(identity, "sha256:")[:24]
		for _, existing := range focus.FailureEvents {
			if existing.EventID == eventID {
				return nil
			}
		}
		focus.FailureEvents = append(focus.FailureEvents, FailureEvent{
			EventID: eventID, Signature: input.FailureCategory, FocusID: focus.FocusID,
			SourceKind: "checkpoint_result", SourceExecution: input.SourceExecution, ToolUseID: input.CheckpointID,
			RepairGeneration: generation, EvidenceHash: input.ResultHash, KnownEvidenceIDs: normalizeStrings(input.EvidenceIDs),
			RepositoryIdentity: identities["repository"], CandidateIdentity: identities["candidate"], ConfigIdentity: identities["config"], RuntimeIdentity: identities["runtime"],
			Trust: "verified", OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		})
		focus.EvidenceCheckpoint.Reached = false
		focus.Checkpoint = nil
		if err := runtime.saveState(state); err != nil {
			return err
		}
		triggerType := "evidence-checkpoint-missed"
		reason := "verified checkpoint did not produce expected evidence"
		if generation > 0 {
			triggerType = "repeated-failure"
			reason = "verified failure repeated after a recorded repair"
		}
		trigger, err := newReviewTrigger("event", triggerType, eventID, reason)
		if err != nil {
			return err
		}
		request, err = runtime.requestReviewLocked(state, trigger)
		return err
	})
	return request, err
}

// PrepareExpansionReview creates a single advisory boundary before the main
// Agent expands a representative probe into a batch or other high-cost action.
// It never authorizes or blocks that action.
func (runtime *Runtime) PrepareExpansionReview(input ExpansionReviewInput) (*ReviewRequest, error) {
	if err := validateExpansionReviewInput(&input); err != nil {
		return nil, err
	}
	var request *ReviewRequest
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		focus := state.CurrentFocus
		if focus == nil || focus.FocusID != input.FocusID || focus.EvidenceCheckpoint.CheckpointID != input.CheckpointID {
			return errors.New("expansion boundary is not bound to the current execution")
		}
		if !focus.EvidenceCheckpoint.Reached {
			return errors.New("high-cost expansion requires a reached representative checkpoint")
		}
		if err := ensureKnownEvidence(state, input.EvidenceIDs); err != nil {
			return err
		}
		sourceHash, err := hashJSON(map[string]any{"execution_id": focus.ExecutionID, "checkpoint_id": input.CheckpointID, "evidence_ids": normalizeStrings(input.EvidenceIDs), "investment": strings.TrimSpace(input.Investment)})
		if err != nil {
			return err
		}
		trigger, err := newReviewTrigger("event", "high-cost-expansion", focus.ExecutionID+":"+sourceHash, "representative evidence is ready before a higher-cost expansion")
		if err != nil {
			return err
		}
		request, err = runtime.requestReviewLocked(state, trigger)
		return err
	})
	return request, err
}

func (runtime *Runtime) RecordFailureEvent(input FailureEventInput) (*ReviewRequest, error) {
	input.Signature = normalizeFailureSignature(input.Signature)
	if input.Signature == "" || strings.TrimSpace(input.SourceExecution) == "" || strings.TrimSpace(input.ToolUseID) == "" {
		return nil, errors.New("failure event requires a signature, source execution and tool_use_id")
	}
	if input.SourceKind != "codex_hook" && input.SourceKind != "governed_run" {
		return nil, errors.New("failure event source is not a trusted governed execution path")
	}
	if err := validHash("failure evidence_hash", input.EvidenceHash); err != nil {
		return nil, err
	}
	var request *ReviewRequest
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.CurrentFocus == nil {
			return errors.New("failure requires a current execution snapshot")
		}
		focus := state.CurrentFocus
		identity, err := hashJSON(map[string]any{"focus_id": focus.FocusID, "source_kind": input.SourceKind, "source_execution": input.SourceExecution, "tool_use_id": input.ToolUseID})
		if err != nil {
			return err
		}
		eventID := "failure_" + strings.TrimPrefix(identity, "sha256:")[:24]
		for _, existing := range focus.FailureEvents {
			if existing.EventID == eventID {
				if existing.Signature != input.Signature || existing.EvidenceHash != input.EvidenceHash || existing.SourceKind != input.SourceKind || existing.SourceExecution != input.SourceExecution || existing.ToolUseID != input.ToolUseID {
					return errors.New("governed failure event identity was replayed with conflicting facts")
				}
				return nil
			}
		}
		identities, err := runtime.currentFailureIdentities()
		if err != nil {
			return err
		}
		generation := 0
		for _, repair := range focus.FailureRepairs {
			if repair.Signature == input.Signature && repair.RepairGeneration > generation {
				generation = repair.RepairGeneration
			}
		}
		event := FailureEvent{
			EventID: eventID, Signature: input.Signature, FocusID: focus.FocusID, SourceKind: input.SourceKind,
			SourceExecution: input.SourceExecution, ToolUseID: input.ToolUseID, RepairGeneration: generation,
			EvidenceHash: input.EvidenceHash, KnownEvidenceIDs: reusableResultIDs(state.ReusableResults), Trust: "verified",
			RepositoryIdentity: identities["repository"], CandidateIdentity: identities["candidate"], ConfigIdentity: identities["config"], RuntimeIdentity: identities["runtime"],
			OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		focus.FailureEvents = append(focus.FailureEvents, event)
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if generation > 0 {
			for _, existing := range focus.FailureEvents {
				if existing.EventID != event.EventID && existing.Trust == "verified" && existing.Signature == event.Signature && existing.RepairGeneration < generation {
					trigger, err := newReviewTrigger("event", "repeated-failure", event.EventID, "verified failure repeated after repair: "+event.Signature)
					if err != nil {
						return err
					}
					request, err = runtime.requestReviewLocked(state, trigger)
					return err
				}
			}
		}
		return nil
	})
	return request, err
}

func (runtime *Runtime) RecordRepair(input FailureRepairInput) (*FailureRepair, error) {
	input.Signature = normalizeFailureSignature(input.Signature)
	input.EvidenceIDs = normalizeStrings(input.EvidenceIDs)
	if input.Signature == "" || strings.TrimSpace(input.PreviousEventID) == "" || len(input.EvidenceIDs) == 0 {
		return nil, errors.New("repair requires a signature, previous event and validated evidence")
	}
	var recorded *FailureRepair
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.CurrentFocus == nil {
			return errors.New("repair requires a current execution snapshot")
		}
		focus := state.CurrentFocus
		for _, existing := range focus.FailureRepairs {
			if existing.Signature == input.Signature && existing.PreviousEventID == input.PreviousEventID {
				if !slices.Equal(existing.EvidenceIDs, input.EvidenceIDs) {
					return errors.New("governed failure repair identity was replayed with conflicting evidence")
				}
				copy := existing
				recorded = &copy
				return nil
			}
		}
		var previous *FailureEvent
		generation := 0
		for index := range focus.FailureEvents {
			event := &focus.FailureEvents[index]
			if event.EventID == input.PreviousEventID && event.Signature == input.Signature && event.Trust == "verified" {
				previous = event
			}
			if event.Signature == input.Signature && event.RepairGeneration > generation {
				generation = event.RepairGeneration
			}
		}
		for _, existing := range focus.FailureRepairs {
			if existing.Signature == input.Signature && existing.RepairGeneration > generation {
				generation = existing.RepairGeneration
			}
		}
		if previous == nil || previous.RepairGeneration != generation {
			return errors.New("repair must reference the latest verified failure generation in the current focus")
		}
		available, knownAtFailure := map[string]struct{}{}, map[string]struct{}{}
		for _, id := range previous.KnownEvidenceIDs {
			knownAtFailure[id] = struct{}{}
		}
		for _, evidence := range state.ReusableResults {
			available[evidence.ResultID] = struct{}{}
		}
		for _, id := range input.EvidenceIDs {
			if _, existed := knownAtFailure[id]; existed {
				return fmt.Errorf("repair evidence %q predates the verified failure event", id)
			}
			if _, exists := available[id]; !exists {
				return fmt.Errorf("repair evidence %q is not registered and validated", id)
			}
		}
		identities, err := runtime.currentFailureIdentities()
		if err != nil {
			return err
		}
		if identities["repository"] == previous.RepositoryIdentity && identities["candidate"] == previous.CandidateIdentity && identities["config"] == previous.ConfigIdentity && identities["runtime"] == previous.RuntimeIdentity {
			return errors.New("repair identity is unchanged from the verified failure occurrence")
		}
		repairIdentity := map[string]any{"signature": input.Signature, "previous_event_id": input.PreviousEventID, "focus_id": focus.FocusID, "repository_identity": identities["repository"], "candidate_identity": identities["candidate"], "config_identity": identities["config"], "runtime_identity": identities["runtime"], "evidence_ids": input.EvidenceIDs}
		hash, err := hashJSON(repairIdentity)
		if err != nil {
			return err
		}
		repair := FailureRepair{RepairID: "repair_" + strings.TrimPrefix(hash, "sha256:")[:24], Signature: input.Signature, PreviousEventID: input.PreviousEventID, FocusID: focus.FocusID, RepairGeneration: generation + 1, RepositoryIdentity: identities["repository"], CandidateIdentity: identities["candidate"], ConfigIdentity: identities["config"], RuntimeIdentity: identities["runtime"], EvidenceIDs: input.EvidenceIDs, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		focus.FailureRepairs = append(focus.FailureRepairs, repair)
		if err := runtime.saveState(state); err != nil {
			return err
		}
		recorded = &repair
		return nil
	})
	return recorded, err
}

func (runtime *Runtime) currentFailureIdentities() (map[string]string, error) {
	snapshot, err := runtime.repositorySnapshot()
	if err != nil {
		return nil, err
	}
	configIdentity, err := fileHash(runtime.ConfigPath)
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	runtimeIdentity, err := fileHash(executable)
	if err != nil {
		return nil, err
	}
	candidateIdentity, err := hashJSON(map[string]string{"head_commit": snapshot.HeadCommit})
	if err != nil {
		return nil, err
	}
	repositoryIdentity, err := hashJSON(map[string]string{"root": snapshot.Root, "head_commit": snapshot.HeadCommit})
	if err != nil {
		return nil, err
	}
	return map[string]string{"repository": repositoryIdentity, "candidate": candidateIdentity, "config": configIdentity, "runtime": runtimeIdentity}, nil
}

func (runtime *Runtime) RequestAdvisoryReview(requestID, reason string) (*ReviewRequest, error) {
	return nil, errors.New("free-form advisory reviews are disabled; record a verified governance boundary instead")
}

func (runtime *Runtime) RequestCompletionReview(sourceID string) (*ReviewRequest, error) {
	if err := nonempty("completion source_id", sourceID); err != nil {
		return nil, err
	}
	state, err := runtime.LoadState()
	if err != nil {
		return nil, err
	}
	evidenceSummary, err := hashJSON(map[string]any{"authority_hash": state.AuthorityHash, "conditions": state.CompletionConditions, "results": state.ReusableResults})
	if err != nil {
		return nil, err
	}
	trigger, err := newReviewTrigger("event", "completion-candidate", strings.TrimSpace(sourceID)+":"+evidenceSummary, "explicit completion candidate")
	if err != nil {
		return nil, err
	}
	return runtime.requestReview(trigger)
}

func (runtime *Runtime) requestReview(trigger ReviewTrigger) (*ReviewRequest, error) {
	var request *ReviewRequest
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		request, err = runtime.requestReviewLocked(state, trigger)
		return err
	})
	return request, err
}

func (runtime *Runtime) ResolveIntervention(input ResolveInterventionInput) (*ReviewRequest, error) {
	if err := validateInterventionResolutionInput(&input); err != nil {
		return nil, err
	}
	var request *ReviewRequest
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		pending := state.PendingIntervention
		if pending == nil || pending.InterventionID != input.InterventionID || pending.Status != "awaiting_user" || pending.Resolution != nil {
			return errors.New("no matching pending user intervention")
		}
		pending.Status = "resolution_pending_review"
		pending.Resolution = &InterventionResolution{SourceTurnID: strings.TrimSpace(input.SourceTurnID), Summary: strings.TrimSpace(input.Summary), EvidenceRefs: normalizeStrings(input.EvidenceRefs), SubmittedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		state.Status = "active"
		if err := runtime.saveState(state); err != nil {
			return err
		}
		trigger, err := newReviewTrigger("event", "intervention-resolution", pending.InterventionID+":"+pending.Resolution.SourceTurnID, "user intervention resolved")
		if err != nil {
			return err
		}
		request, err = runtime.requestReviewLocked(state, trigger)
		return err
	})
	return request, err
}

func (runtime *Runtime) ReconcileAuthority() (*ReviewRequest, error) {
	var request *ReviewRequest
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		current, err := runtime.authorityHash()
		if err != nil || current == state.AuthorityHash {
			return err
		}
		previous := state.AuthorityHash
		state.AuthorityHash = current
		if err := runtime.saveState(state); err != nil {
			return err
		}
		trigger, err := newReviewTrigger("event", "authority-change", previous+":"+current, "authority changed")
		if err != nil {
			return err
		}
		request, err = runtime.requestReviewLocked(state, trigger)
		return err
	})
	return request, err
}

func newReviewTrigger(kind, triggerType, sourceID, reason string) (ReviewTrigger, error) {
	trigger := ReviewTrigger{Kind: strings.TrimSpace(kind), Type: strings.TrimSpace(triggerType), SourceID: strings.TrimSpace(sourceID), Reason: strings.TrimSpace(reason)}
	if trigger.Reason == "" {
		trigger.Reason = strings.ReplaceAll(trigger.Type, "-", " ")
	}
	if err := validateReviewTrigger(trigger); err != nil {
		return ReviewTrigger{}, err
	}
	return trigger, nil
}

func reviewTriggerIdentity(trigger ReviewTrigger) string {
	return trigger.Kind + ":" + trigger.Type + ":" + trigger.SourceID
}

func reviewTriggerInstanceID(runID string, trigger ReviewTrigger) (string, error) {
	hash, err := hashJSON(map[string]string{"run_id": runID, "kind": trigger.Kind, "type": trigger.Type, "source_id": trigger.SourceID})
	if err != nil {
		return "", err
	}
	return "trigger_" + strings.TrimPrefix(hash, "sha256:")[:32], nil
}

func (runtime *Runtime) requestReviewLocked(state *State, trigger ReviewTrigger) (*ReviewRequest, error) {
	invalidEvidence, err := runtime.reconcileEvidenceIdentitiesLocked(state)
	if err != nil {
		return nil, err
	}
	if len(invalidEvidence) > 0 {
		trigger, err = evidenceIdentityChangeTrigger(invalidEvidence)
		if err != nil {
			return nil, err
		}
	}
	identity := reviewTriggerIdentity(trigger)
	if oneOf(state.Review.Status, "requested", "feedback_ready", "superseded") {
		existing, loadErr := runtime.loadRequest()
		if loadErr == nil && runtime.verifyReviewIntegrity(existing, state) == nil {
			if state.Review.Status == "requested" {
				if err := removeFileIfExists(runtime.reviewPath()); err != nil {
					return nil, err
				}
			}
			if identity != valueOr(state.Review.Trigger, "") {
				if err := mergePendingReview(state, trigger); err != nil {
					return nil, err
				}
				if err := runtime.saveState(state); err != nil {
					return nil, err
				}
			}
			return existing, nil
		}
		state.Review.Status = "missed"
		state.Review.Response = nil
		state.Review.GovernorAgentID = nil
	}
	if state.Review.Trigger != nil && *state.Review.Trigger == identity {
		return nil, nil
	}
	instanceID, err := reviewTriggerInstanceID(state.RunID, trigger)
	if err != nil {
		return nil, err
	}
	if trigger.Kind == "fixed" {
		state.Review.FixedReviewGeneration++
	}
	snapshot, err := runtime.repositorySnapshot()
	if err != nil {
		return nil, err
	}
	authorityRefs, err := runtime.authorityReferences()
	if err != nil {
		return nil, err
	}
	request := &ReviewRequest{
		SchemaVersion: schemaVersion, ReviewID: newID("review"), TriggerInstanceID: instanceID, Trigger: trigger,
		AuthorityPaths: normalizeStrings(runtime.Config.AuthorityPaths), AuthorityRefs: authorityRefs,
		CompletionDefinitionPaths: normalizeStrings(runtime.Config.CompletionDefinitionPaths),
		RepositorySnapshot:        snapshot, StatePath: filepath.ToSlash(mustRelative(runtime.Root, runtime.statePath())), PendingIntervention: state.PendingIntervention,
		ResourceFacts: []ResourceFact{}, EvidenceRefs: runtime.evidenceReferences(state),
		ProgressDelta: runtime.progressDelta(state), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if state.CurrentFocus != nil {
		request.CurrentConditionID = stringPointer(state.CurrentFocus.ConditionID)
		request.CurrentFocus = &RequestFocus{FocusID: state.CurrentFocus.FocusID, ConditionID: state.CurrentFocus.ConditionID, Objective: state.CurrentFocus.Objective, Value: state.CurrentFocus.Value, InvolvedScope: state.CurrentFocus.InvolvedScope, ExpectedEvidence: state.CurrentFocus.ExpectedEvidence, CheckpointID: state.CurrentFocus.EvidenceCheckpoint.CheckpointID, CheckpointDescription: state.CurrentFocus.EvidenceCheckpoint.Description, SnapshotHash: state.CurrentFocus.SnapshotHash, ExecutionID: state.CurrentFocus.ExecutionID}
		if state.CurrentFocus.EvidenceCheckpoint.Reached {
			request.RecentCheckpoint = &RecentCheckpoint{CheckpointID: state.CurrentFocus.EvidenceCheckpoint.CheckpointID, Description: state.CurrentFocus.EvidenceCheckpoint.Description, EvidenceIDs: conditionEvidence(state, state.CurrentFocus.ConditionID)}
		}
	}
	for _, constraint := range state.ExplicitResourceConstraints {
		request.ResourceFacts = append(request.ResourceFacts, ResourceFact{Measure: constraint.Measure, Value: constraint.Limit, Unit: "configured-limit", Source: constraint.Source})
	}
	request.ReviewSnapshotHash = ""
	hash, err := hashJSON(request)
	if err != nil {
		return nil, err
	}
	request.ReviewSnapshotHash = hash
	if err := validateReviewRequest(request); err != nil {
		return nil, err
	}
	state.Review.Status = "requested"
	state.Review.ReviewID = stringPointer(request.ReviewID)
	state.Review.TriggerInstanceID = stringPointer(request.TriggerInstanceID)
	state.Review.ReviewSnapshotHash = stringPointer(request.ReviewSnapshotHash)
	state.Review.Trigger = stringPointer(identity)
	state.Review.Response = nil
	state.Review.Pending = nil
	if err := atomicWriteJSON(runtime.requestPath(), request); err != nil {
		return nil, err
	}
	if err := runtime.saveState(state); err != nil {
		return nil, err
	}
	if err := removeFileIfExists(runtime.reviewPath()); err != nil {
		return nil, err
	}
	return request, nil
}

func (runtime *Runtime) loadRequest() (*ReviewRequest, error) {
	var request ReviewRequest
	if err := decodeStrictFile(runtime.requestPath(), &request); err != nil {
		return nil, err
	}
	if err := validateReviewRequest(&request); err != nil {
		return nil, err
	}
	return &request, nil
}

func (runtime *Runtime) AcceptReview(result ReviewResult) (string, error) {
	if err := validateReviewResult(&result); err != nil {
		return "", err
	}
	var path string
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		request, err := runtime.loadRequest()
		if err != nil {
			return err
		}
		if state.Review.Status != "requested" || state.Review.ReviewID == nil || state.Review.TriggerInstanceID == nil || state.Review.ReviewSnapshotHash == nil {
			return errors.New("no requested review is awaiting feedback")
		}
		if result.ReviewID != request.ReviewID || result.TriggerInstanceID != request.TriggerInstanceID || result.ReviewSnapshotHash != request.ReviewSnapshotHash || *state.Review.ReviewID != result.ReviewID || *state.Review.TriggerInstanceID != result.TriggerInstanceID || *state.Review.ReviewSnapshotHash != result.ReviewSnapshotHash {
			return errors.New("Governor feedback identity does not match the current request")
		}
		if err := runtime.verifyReviewIntegrity(request, state); err != nil {
			return err
		}
		if err := runtime.validateReviewResultForRequest(&result, request); err != nil {
			return err
		}
		path = runtime.reviewPath()
		if err := atomicWriteJSON(path, &result); err != nil {
			return err
		}
		if runtime.reviewStillApplicable(request, state) {
			state.Review.Status = "feedback_ready"
		} else {
			state.Review.Status = "superseded"
		}
		if err := runtime.saveState(state); err != nil {
			return err
		}
		return nil
	})
	return path, err
}

// MarkReviewMissed records that the optional Governor path was unavailable.
// It deliberately releases the review state without changing execution state
// or preventing a later natural boundary from requesting fresh feedback.
func (runtime *Runtime) MarkReviewMissed(reason string) error {
	reason = normalizeFailureSignature(reason)
	if reason == "" {
		reason = "governor unavailable"
	}
	return runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if oneOf(state.Review.Status, "feedback_ready", "superseded") {
			var feedback ReviewResult
			if err := decodeStrictFile(runtime.reviewPath(), &feedback); err == nil && reviewMatchesState(&feedback, state) {
				return nil
			}
		}
		if !oneOf(state.Review.Status, "requested", "feedback_ready", "superseded") {
			return nil
		}
		state.Review.Status = "missed"
		state.Review.Response = nil
		state.Review.GovernorAgentID = nil
		state.LastDiagnostic = &RuntimeDiagnostic{Source: "governor-review", Summary: reason, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}
		pending := state.Review.Pending
		state.Review.Pending = nil
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if err := removeFileIfExists(runtime.reviewPath()); err != nil {
			return err
		}
		if pending != nil {
			trigger, err := pendingReviewTrigger(*pending)
			if err != nil {
				return err
			}
			_, err = runtime.requestReviewLocked(state, trigger)
			return err
		}
		return nil
	})
}

func (runtime *Runtime) verifyReviewSnapshot(request *ReviewRequest, state *State) error {
	return runtime.verifyReviewIntegrity(request, state)
}

func (runtime *Runtime) RecordReviewResponse(input ReviewResponseInput) (*State, error) {
	if err := validateReviewResponseInput(&input); err != nil {
		return nil, err
	}
	var updated *State
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if !oneOf(state.Review.Status, "feedback_ready", "superseded") || state.Review.ReviewID == nil || *state.Review.ReviewID != input.ReviewID {
			return errors.New("no matching Governor feedback is awaiting the main Agent response")
		}
		var result ReviewResult
		if err := decodeStrictFile(runtime.reviewPath(), &result); err != nil {
			return err
		}
		if !reviewMatchesState(&result, state) {
			return errors.New("current Governor feedback identity does not match governance state")
		}
		wasSuperseded := state.Review.Status == "superseded"
		response := &ReviewResponse{ReviewID: input.ReviewID, Disposition: input.Disposition, Reason: strings.TrimSpace(input.Reason), NextValidationPoint: strings.TrimSpace(input.NextValidationPoint), RespondedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		state.Review.Response = response
		state.Review.Status = "responded"
		state.NextAction = stringPointer(response.NextValidationPoint)
		if !wasSuperseded && input.Disposition == "adopt" && oneOf(result.Recommendation, "product_decision_needed", "external_input_needed") {
			state.PendingIntervention = pendingInterventionFromResult(result)
			state.Status = "awaiting_user"
			state.NextAction = stringPointer(result.ExternalInput.MinimumUserInput)
		} else if state.PendingIntervention != nil && state.PendingIntervention.Status == "resolution_pending_review" {
			state.PendingIntervention = nil
			state.Status = "active"
		}
		state.ReviewBaseline = runtime.baselineFromState(state, input.ReviewID)
		pending := state.Review.Pending
		state.Review.Pending = nil
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if pending != nil {
			trigger, triggerErr := pendingReviewTrigger(*pending)
			if triggerErr != nil {
				return triggerErr
			}
			if _, requestErr := runtime.requestReviewLocked(state, trigger); requestErr != nil {
				return requestErr
			}
		}
		updated = state
		return nil
	})
	return updated, err
}

// ApplyReviewCompatibility keeps already-loaded old instructions safe. It
// acknowledges stored feedback but never applies Governor output to execution.
func (runtime *Runtime) ApplyReviewCompatibility() (*State, error) {
	state, err := runtime.LoadState()
	if err != nil {
		return nil, err
	}
	if state.Review.Status != "feedback_ready" || state.Review.ReviewID == nil {
		return state, nil
	}
	return runtime.RecordReviewResponse(ReviewResponseInput{ReviewID: *state.Review.ReviewID, Disposition: "acknowledge", Reason: "compatibility acknowledgement; Governor feedback was not applied as execution control", NextValidationPoint: valueOr(state.NextAction, "continue from the persisted execution snapshot")})
}

func pendingInterventionFromResult(result ReviewResult) *PendingIntervention {
	return &PendingIntervention{InterventionID: newID("intervention"), SourceReviewID: result.ReviewID, Kind: result.ExternalInput.Kind, Fact: strings.TrimSpace(result.ExternalInput.Fact), ExhaustedPaths: normalizeStrings(result.ExternalInput.ExhaustedPaths), MinimumUserInput: strings.TrimSpace(result.ExternalInput.MinimumUserInput), Status: "awaiting_user", Resolution: nil}
}

func (runtime *Runtime) CompleteExecutionSnapshot() error {
	return runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		invalidEvidence, err := runtime.reconcileEvidenceIdentitiesLocked(state)
		if err != nil {
			return err
		}
		if len(invalidEvidence) > 0 {
			trigger, triggerErr := evidenceIdentityChangeTrigger(invalidEvidence)
			if triggerErr != nil {
				return triggerErr
			}
			if _, requestErr := runtime.requestReviewLocked(state, trigger); requestErr != nil {
				return requestErr
			}
			return errors.New("execution evidence identity changed; a fresh advisory review was requested")
		}
		if state.CurrentFocus == nil {
			return errors.New("execution snapshot has not reached its natural evidence checkpoint")
		}
		if !state.CurrentFocus.EvidenceCheckpoint.Reached {
			missing := missingEvidenceForFocus(state.CurrentFocus, state.ReusableResults)
			trigger, triggerErr := newReviewTrigger("event", "evidence-checkpoint-missed", state.CurrentFocus.FocusID+":"+state.CurrentFocus.SnapshotHash+":"+state.CurrentFocus.EvidenceCheckpoint.CheckpointID+":"+strings.Join(missing, ","), "evidence checkpoint reached without expected evidence")
			if triggerErr != nil {
				return triggerErr
			}
			if _, requestErr := runtime.requestReviewLocked(state, trigger); requestErr != nil {
				return requestErr
			}
			return fmt.Errorf("execution checkpoint is missing expected evidence %v; an advisory review was requested", missing)
		}
		if oneOf(state.Review.Status, "feedback_ready", "superseded") {
			return errors.New("Governor feedback is available and must be explicitly answered before crossing this stage boundary")
		}
		focus := state.CurrentFocus
		trigger, triggerErr := newReviewTrigger("event", "stage-boundary", focus.ExecutionID+":"+focus.EvidenceCheckpoint.CheckpointID, "execution stage reached its natural boundary")
		if triggerErr != nil {
			return triggerErr
		}
		if _, requestErr := runtime.requestReviewLocked(state, trigger); requestErr != nil {
			return requestErr
		}
		conditionID := state.CurrentFocus.ConditionID
		setConditionStatus(state, conditionID, "met", conditionEvidence(state, conditionID))
		state.CurrentFocus = nil
		state.NextAction = stringPointer("select the next highest-value unmet completion condition")
		if err := runtime.saveState(state); err != nil {
			return err
		}
		return nil
	})
}

func (runtime *Runtime) Finish() error {
	return runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		invalidEvidence, err := runtime.reconcileEvidenceIdentitiesLocked(state)
		if err != nil {
			return err
		}
		if len(invalidEvidence) > 0 {
			trigger, triggerErr := evidenceIdentityChangeTrigger(invalidEvidence)
			if triggerErr != nil {
				return triggerErr
			}
			if _, requestErr := runtime.requestReviewLocked(state, trigger); requestErr != nil {
				return requestErr
			}
			return errors.New("completion evidence identity changed; completion was reopened and a fresh advisory review was requested")
		}
		if state.Review.Trigger == nil || !strings.Contains(*state.Review.Trigger, ":completion-candidate:") {
			return errors.New("finish requires a completion-candidate advisory review")
		}
		if state.Review.Status == "requested" {
			markReviewMissedLocked(state, "Governor feedback was unavailable at the completion boundary")
			_ = removeFileIfExists(runtime.reviewPath())
		}
		if oneOf(state.Review.Status, "feedback_ready", "superseded") {
			return errors.New("completion feedback is available and must be explicitly answered before the completion claim")
		}
		if state.Review.Status != "responded" && state.Review.Status != "missed" {
			return errors.New("finish requires one completion-candidate advisory boundary")
		}
		if state.Review.Status == "responded" && state.Review.Response == nil {
			return errors.New("responded completion review is missing its feedback or main Agent response")
		}
		if state.Review.Status == "responded" {
			request, err := runtime.loadRequest()
			if err != nil {
				return err
			}
			if err := runtime.verifyReviewSnapshot(request, state); err != nil {
				return fmt.Errorf("completion candidate is no longer current: %w", err)
			}
			var feedback ReviewResult
			if err := decodeStrictFile(runtime.reviewPath(), &feedback); err != nil {
				return err
			}
			if err := validateReviewResult(&feedback); err != nil {
				return err
			}
			if err := validateReviewResultForState(&feedback, request, state); err != nil {
				return err
			}
		}
		for _, condition := range state.CompletionConditions {
			if condition.Status != "met" || len(condition.EvidenceIDs) == 0 {
				return fmt.Errorf("completion condition %q is not fully evidenced", condition.ConditionID)
			}
		}
		state.Status = "complete"
		state.CurrentFocus = nil
		state.NextAction = nil
		state.Review.Pending = nil
		if err := runtime.saveState(state); err != nil {
			return err
		}
		return nil
	})
}

func (runtime *Runtime) allowedEvidencePath(path string) bool {
	for _, root := range runtime.Config.EvidenceRoots {
		if within(resolvePath(runtime.Root, root), path) {
			return true
		}
	}
	return false
}

func hasCondition(state *State, conditionID string) bool {
	for _, condition := range state.CompletionConditions {
		if condition.ConditionID == conditionID {
			return true
		}
	}
	return false
}

func setConditionStatus(state *State, conditionID, status string, evidence []string) {
	for index := range state.CompletionConditions {
		if state.CompletionConditions[index].ConditionID == conditionID {
			state.CompletionConditions[index].Status = status
			if evidence != nil {
				state.CompletionConditions[index].EvidenceIDs = normalizeStrings(append(state.CompletionConditions[index].EvidenceIDs, evidence...))
			}
		}
	}
}

func conditionEvidence(state *State, conditionID string) []string {
	for _, condition := range state.CompletionConditions {
		if condition.ConditionID == conditionID {
			return append([]string(nil), condition.EvidenceIDs...)
		}
	}
	return nil
}

func expectedEvidenceReached(focus *ExecutionSnapshot, results []ReusableResult) bool {
	return len(focus.ExpectedEvidence) > 0 && len(evidenceForFocus(focus, results)) == len(focus.ExpectedEvidence)
}

func evidenceForFocus(focus *ExecutionSnapshot, results []ReusableResult) []string {
	if focus == nil {
		return nil
	}
	present := map[string]struct{}{}
	for _, result := range results {
		present[result.ResultID] = struct{}{}
	}
	matched := make([]string, 0, len(focus.ExpectedEvidence))
	for _, expected := range focus.ExpectedEvidence {
		if _, exists := present[expected]; exists {
			matched = append(matched, expected)
		}
	}
	return normalizeStrings(matched)
}

func (runtime *Runtime) evidenceReferences(state *State) []EvidenceReference {
	if state == nil {
		return []EvidenceReference{}
	}
	references := make([]EvidenceReference, 0, len(state.ReusableResults))
	for _, result := range state.ReusableResults {
		path := resolvePath(runtime.Root, result.EvidencePath)
		hash, err := fileHash(path)
		if err == nil && hash == result.InputHash && runtime.allowedEvidencePath(path) {
			references = append(references, EvidenceReference{EvidenceID: result.ResultID, Path: result.EvidencePath, Hash: hash})
		}
	}
	slices.SortFunc(references, func(left, right EvidenceReference) int {
		return strings.Compare(left.EvidenceID+"\x00"+left.Path+"\x00"+left.Hash, right.EvidenceID+"\x00"+right.Path+"\x00"+right.Hash)
	})
	return references
}

func recentCheckpointForState(state *State) *RecentCheckpoint {
	if state == nil || state.CurrentFocus == nil || !state.CurrentFocus.EvidenceCheckpoint.Reached {
		return nil
	}
	return &RecentCheckpoint{CheckpointID: state.CurrentFocus.EvidenceCheckpoint.CheckpointID, Description: state.CurrentFocus.EvidenceCheckpoint.Description, EvidenceIDs: conditionEvidence(state, state.CurrentFocus.ConditionID)}
}

func markReviewMissedLocked(state *State, reason string) {
	state.Review.Status = "missed"
	state.Review.Response = nil
	state.Review.GovernorAgentID = nil
	state.LastDiagnostic = &RuntimeDiagnostic{Source: "governor-review", Summary: normalizeFailureSignature(reason), OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}
}

func missingEvidenceForFocus(focus *ExecutionSnapshot, results []ReusableResult) []string {
	if focus == nil {
		return nil
	}
	present := stringSet(evidenceForFocus(focus, results))
	missing := make([]string, 0)
	for _, expected := range focus.ExpectedEvidence {
		if _, exists := present[expected]; !exists {
			missing = append(missing, expected)
		}
	}
	return normalizeStrings(missing)
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func evidenceIdentityChangeTrigger(invalidEvidence []string) (ReviewTrigger, error) {
	identity, err := hashJSON(normalizeStrings(invalidEvidence))
	if err != nil {
		return ReviewTrigger{}, err
	}
	return newReviewTrigger("event", "evidence-identity-change", identity, "registered evidence content or availability changed")
}

func (runtime *Runtime) reconcileEvidenceIdentitiesLocked(state *State) ([]string, error) {
	if state == nil || len(state.ReusableResults) == 0 {
		return nil, nil
	}
	valid := make([]ReusableResult, 0, len(state.ReusableResults))
	invalid := make([]string, 0)
	invalidSet := map[string]struct{}{}
	for _, result := range state.ReusableResults {
		path := resolvePath(runtime.Root, result.EvidencePath)
		hash, err := fileHash(path)
		if !runtime.allowedEvidencePath(path) || err != nil || hash != result.InputHash {
			invalid = append(invalid, result.ResultID)
			invalidSet[result.ResultID] = struct{}{}
			state.InvalidatedEvidence = upsertEvidenceInvalidation(state.InvalidatedEvidence, EvidenceInvalidation{EvidenceID: result.ResultID, PreviousHash: result.InputHash, InvalidatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
			continue
		}
		valid = append(valid, result)
	}
	invalid = normalizeStrings(invalid)
	if len(invalid) == 0 {
		return nil, nil
	}
	state.ReusableResults = valid
	for index := range state.CompletionConditions {
		condition := &state.CompletionConditions[index]
		kept := make([]string, 0, len(condition.EvidenceIDs))
		removed := false
		for _, evidenceID := range condition.EvidenceIDs {
			if _, lost := invalidSet[evidenceID]; lost {
				removed = true
				continue
			}
			kept = append(kept, evidenceID)
		}
		condition.EvidenceIDs = normalizeStrings(kept)
		if removed {
			condition.Status = "unmet"
		}
	}
	if state.CurrentFocus != nil && len(missingEvidenceForFocus(state.CurrentFocus, state.ReusableResults)) > 0 {
		state.CurrentFocus.EvidenceCheckpoint.Reached = false
		state.CurrentFocus.Checkpoint = nil
		setConditionStatus(state, state.CurrentFocus.ConditionID, "in_progress", nil)
	}
	if state.Status == "complete" {
		state.Status = "active"
	}
	state.NextAction = stringPointer("replace or revalidate invalid evidence: " + strings.Join(invalid, ", "))
	if state.Review.Status == "feedback_ready" {
		state.Review.Status = "superseded"
	}
	if err := runtime.saveState(state); err != nil {
		return nil, err
	}
	return invalid, nil
}

func upsertEvidenceInvalidation(values []EvidenceInvalidation, next EvidenceInvalidation) []EvidenceInvalidation {
	for index := range values {
		if values[index].EvidenceID == next.EvidenceID {
			values[index] = next
			return values
		}
	}
	return append(values, next)
}

func normalizeFailureSignature(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = regexp.MustCompile(`\s+`).ReplaceAllString(value, " ")
	if len(value) > 240 {
		value = value[:240]
	}
	return value
}

func reusableResultIDs(results []ReusableResult) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ResultID)
	}
	return normalizeStrings(ids)
}

func stringPointer(value string) *string { return &value }

func mustRelative(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(filepath.Clean(path))
	}
	return filepath.ToSlash(relative)
}
