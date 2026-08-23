package governance

import (
	"bufio"
	"encoding/json"
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
		next := "request the fixed Governor review before product work"
		state := &State{
			SchemaVersion:               schemaVersion,
			RunID:                       newID("run"),
			Status:                      "running",
			AuthorityHash:               authorityHash,
			CompletionConditions:        conditions,
			CurrentWorkPacket:           nil,
			PendingIntervention:         nil,
			ExplicitResourceConstraints: runtime.Config.ExplicitResourceConstraints,
			ReusableResults:             []ReusableResult{},
			NextAction:                  &next,
			Review:                      ReviewState{},
			Owner:                       nil,
			Handoff:                     nil,
			InfrastructureFailure:       nil,
		}
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if err := runtime.appendEvent("initialized", state.RunID, "created governance state", nil); err != nil {
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
		path := resolvePath(runtime.Root, configured)
		file, err := os.Open(path)
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
			if _, err := strconv.Atoi(first); err != nil || second == "" {
				continue
			}
			conditions = append(conditions, CompletionCondition{ConditionID: "condition:" + first + ":" + second, Status: "unmet"})
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
			previousAuthority := state.AuthorityHash
			state.AuthorityHash = currentAuthority
			state.CurrentWorkPacket = nil
			state.PendingIntervention = nil
			for index := range state.CompletionConditions {
				if state.CompletionConditions[index].Status != "unmet" {
					state.CompletionConditions[index].Status = "evidence_insufficient"
				}
			}
			authorityTrigger, triggerErr := newReviewTrigger("event", "authority-change", previousAuthority+":"+currentAuthority, "authority changed")
			if triggerErr != nil {
				return triggerErr
			}
			created, triggerErr := runtime.requestReviewLocked(state, authorityTrigger, true)
			if triggerErr != nil {
				return triggerErr
			}
			request = created
			return nil
		}
		if state.PendingIntervention != nil && state.PendingIntervention.Status == "awaiting_user" {
			return nil
		}
		identity := reviewTriggerIdentity(trigger)
		if state.Review.Required && state.Review.Trigger != nil && *state.Review.Trigger == identity {
			loaded, err := runtime.loadRequest()
			if err != nil {
				return err
			}
			request = loaded
			return nil
		}
		if state.Review.Required {
			loaded, err := runtime.loadRequest()
			if err != nil {
				return err
			}
			request = loaded
			return nil
		}
		if state.Review.Trigger != nil && *state.Review.Trigger == identity {
			return nil
		}
		state.Review.FixedReviewGeneration++
		created, err := runtime.requestReviewLocked(state, trigger, false)
		if err != nil {
			return err
		}
		request = created
		return nil
	})
	return request, err
}

func (runtime *Runtime) ProposeWorkPacket(proposal WorkPacketProposal) (*ReviewRequest, error) {
	if err := validateProposal(&proposal); err != nil {
		return nil, err
	}
	var request *ReviewRequest
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.Status != "running" || state.Review.Required {
			return errors.New("cannot propose work while governance is stopped or a review is pending")
		}
		if !hasCondition(state, proposal.ConditionID) {
			return fmt.Errorf("unknown completion condition %q", proposal.ConditionID)
		}
		packet, err := packetFromProposal(&proposal)
		if err != nil {
			return err
		}
		state.CurrentWorkPacket = packet
		setConditionStatus(state, proposal.ConditionID, "in_progress", nil)
		trigger, err := newReviewTrigger("event", "new-work-packet", packet.PacketID+":"+packet.PlanHash, "new work packet")
		if err != nil {
			return err
		}
		created, err := runtime.requestReviewLocked(state, trigger, false)
		if err != nil {
			return err
		}
		request = created
		return nil
	})
	return request, err
}

func packetFromProposal(proposal *WorkPacketProposal) (*WorkPacket, error) {
	plan := *proposal
	plan.AllowedScope = normalizeStrings(plan.AllowedScope)
	plan.ExcludedScope = normalizeStrings(plan.ExcludedScope)
	plan.ExpectedEvidence = normalizeStrings(plan.ExpectedEvidence)
	hash, err := hashJSON(plan)
	if err != nil {
		return nil, err
	}
	return &WorkPacket{
		PacketID: proposal.PacketID, ConditionID: proposal.ConditionID, Objective: proposal.Objective,
		Value: proposal.Value, AllowedScope: plan.AllowedScope, ExcludedScope: plan.ExcludedScope,
		ExpectedEvidence:   plan.ExpectedEvidence,
		EvidenceCheckpoint: EvidenceCheckpoint{CheckpointID: proposal.CheckpointID, Description: proposal.CheckpointDescription},
		PlanHash:           hash, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), FailureSignatures: []string{},
		FailureEvents: []FailureEvent{}, FailureRepairs: []FailureRepair{},
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
		if state.CurrentWorkPacket == nil || state.Review.Required {
			return errors.New("evidence requires an active approved work packet")
		}
		if state.CurrentWorkPacket.Approval == nil {
			return errors.New("work packet is not approved")
		}
		for _, existing := range state.ReusableResults {
			if existing.ResultID == record.EvidenceID {
				if existing.InputHash == hash && filepath.Clean(resolvePath(runtime.Root, existing.EvidencePath)) == filepath.Clean(path) {
					return nil
				}
				return fmt.Errorf("evidence id %q already identifies different content", record.EvidenceID)
			}
		}
		state.ReusableResults = append(state.ReusableResults, ReusableResult{ResultID: record.EvidenceID, Scope: normalizeStrings(record.Scope), EvidencePath: filepath.ToSlash(record.Path), InputHash: hash})
		now := time.Now().UTC().Format(time.RFC3339Nano)
		state.CurrentWorkPacket.LastEvidenceAt = &now
		setConditionStatus(state, state.CurrentWorkPacket.ConditionID, "in_progress", []string{record.EvidenceID})
		if expectedEvidenceReached(state.CurrentWorkPacket, state.ReusableResults) {
			state.CurrentWorkPacket.EvidenceCheckpoint.Reached = true
			state.CurrentWorkPacket.Checkpoint = &state.CurrentWorkPacket.EvidenceCheckpoint.CheckpointID
			trigger, err := newReviewTrigger("event", "evidence-checkpoint", state.CurrentWorkPacket.EvidenceCheckpoint.CheckpointID, "evidence checkpoint reached")
			if err != nil {
				return err
			}
			created, err := runtime.requestReviewLocked(state, trigger, false)
			if err != nil {
				return err
			}
			request = created
			return nil
		}
		if err := runtime.saveState(state); err != nil {
			return err
		}
		return runtime.appendEvent("evidence_recorded", state.RunID, "recorded validated evidence", map[string]any{"evidence_id": record.EvidenceID, "path": record.Path, "hash": hash})
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
		if state.CurrentWorkPacket == nil {
			return errors.New("failure requires a current work packet")
		}
		packet := state.CurrentWorkPacket
		identity, hashErr := hashJSON(map[string]any{
			"packet_id": packet.PacketID, "source_kind": input.SourceKind,
			"source_execution": input.SourceExecution, "tool_use_id": input.ToolUseID,
		})
		if hashErr != nil {
			return hashErr
		}
		eventID := "failure_" + strings.TrimPrefix(identity, "sha256:")[:24]
		for _, existing := range packet.FailureEvents {
			if existing.EventID == eventID {
				if existing.Signature != input.Signature || existing.EvidenceHash != input.EvidenceHash ||
					existing.SourceKind != input.SourceKind || existing.SourceExecution != input.SourceExecution || existing.ToolUseID != input.ToolUseID {
					return errors.New("governed failure event identity was replayed with conflicting facts")
				}
				return nil
			}
		}
		if state.Status != "running" || state.Review.Required || packet.Approval == nil {
			return errors.New("failure requires an active approved work packet without a pending review")
		}
		identities, err := runtime.currentFailureIdentities()
		if err != nil {
			return err
		}
		generation := 0
		for _, repair := range packet.FailureRepairs {
			if repair.Signature == input.Signature && repair.RepairGeneration > generation {
				generation = repair.RepairGeneration
			}
		}
		event := FailureEvent{
			EventID: eventID, Signature: input.Signature, WorkPacketID: packet.PacketID,
			SourceKind: input.SourceKind, SourceExecution: input.SourceExecution,
			ToolUseID: input.ToolUseID, RepairGeneration: generation,
			EvidenceHash: input.EvidenceHash, KnownEvidenceIDs: reusableResultIDs(state.ReusableResults), Trust: "verified",
			RepositoryIdentity: identities["repository"], CandidateIdentity: identities["candidate"],
			ConfigIdentity: identities["config"], RuntimeIdentity: identities["runtime"],
			OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		packet.FailureEvents = append(packet.FailureEvents, event)
		shouldReview := false
		if generation > 0 {
			for _, existing := range packet.FailureEvents {
				if existing.EventID != event.EventID && existing.Trust == "verified" && existing.Signature == event.Signature && existing.RepairGeneration < generation {
					shouldReview = true
					break
				}
			}
		}
		if shouldReview {
			trigger, triggerErr := newReviewTrigger("event", "repeated-failure", event.EventID, "verified failure repeated after repair: "+event.Signature)
			if triggerErr != nil {
				return triggerErr
			}
			created, reviewErr := runtime.requestReviewLocked(state, trigger, false)
			if reviewErr != nil {
				return reviewErr
			}
			request = created
			return nil
		}
		if err := runtime.saveState(state); err != nil {
			return err
		}
		return runtime.appendEvent("failure_recorded", state.RunID, "recorded verified failure event", map[string]any{"event_id": event.EventID, "signature": event.Signature, "repair_generation": generation})
	})
	return request, err
}

func (runtime *Runtime) ensureFailureRecordingReview(sourceID string) error {
	state, err := runtime.LoadState()
	if err != nil {
		return err
	}
	if state.Review.Required {
		return nil
	}
	_, err = runtime.requestEventReview("failure-recording-integrity", sourceID, "verified failure event could not be recorded")
	return err
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
		if state.CurrentWorkPacket == nil {
			return errors.New("repair requires a current work packet")
		}
		packet := state.CurrentWorkPacket
		for _, existing := range packet.FailureRepairs {
			if existing.Signature == input.Signature && existing.PreviousEventID == input.PreviousEventID {
				if !slices.Equal(existing.EvidenceIDs, input.EvidenceIDs) {
					return errors.New("governed failure repair identity was replayed with conflicting evidence")
				}
				recorded = &existing
				return nil
			}
		}
		if state.Status != "running" || state.Review.Required || packet.Approval == nil {
			return errors.New("repair requires an active approved work packet without a pending review")
		}
		var previous *FailureEvent
		generation := 0
		for index := range packet.FailureEvents {
			event := &packet.FailureEvents[index]
			if event.EventID == input.PreviousEventID && event.Signature == input.Signature && event.Trust == "verified" {
				previous = event
			}
			if event.Signature == input.Signature && event.RepairGeneration > generation {
				generation = event.RepairGeneration
			}
		}
		for _, existing := range packet.FailureRepairs {
			if existing.Signature == input.Signature && existing.RepairGeneration > generation {
				generation = existing.RepairGeneration
			}
		}
		if previous == nil {
			return errors.New("repair does not reference a verified failure event in the current work packet")
		}
		if previous.RepairGeneration != generation {
			return errors.New("repair references a stale failure generation")
		}
		available := map[string]struct{}{}
		knownAtFailure := map[string]struct{}{}
		for _, evidenceID := range previous.KnownEvidenceIDs {
			knownAtFailure[evidenceID] = struct{}{}
		}
		for _, evidence := range state.ReusableResults {
			available[evidence.ResultID] = struct{}{}
		}
		for _, evidenceID := range input.EvidenceIDs {
			if _, existed := knownAtFailure[evidenceID]; existed {
				return fmt.Errorf("repair evidence %q predates the verified failure event", evidenceID)
			}
			if _, exists := available[evidenceID]; !exists {
				return fmt.Errorf("repair evidence %q is not registered and validated", evidenceID)
			}
		}
		identities, err := runtime.currentFailureIdentities()
		if err != nil {
			return err
		}
		if identities["repository"] == previous.RepositoryIdentity &&
			identities["candidate"] == previous.CandidateIdentity &&
			identities["config"] == previous.ConfigIdentity &&
			identities["runtime"] == previous.RuntimeIdentity {
			return errors.New("repair identity is unchanged from the verified failure occurrence")
		}
		repairIdentity := map[string]any{
			"signature": input.Signature, "previous_event_id": input.PreviousEventID,
			"packet_id": packet.PacketID, "repository_identity": identities["repository"],
			"candidate_identity": identities["candidate"], "config_identity": identities["config"],
			"runtime_identity": identities["runtime"], "evidence_ids": input.EvidenceIDs,
		}
		hash, err := hashJSON(repairIdentity)
		if err != nil {
			return err
		}
		repairID := "repair_" + strings.TrimPrefix(hash, "sha256:")[:24]
		for index := range packet.FailureRepairs {
			if packet.FailureRepairs[index].RepairID == repairID {
				recorded = &packet.FailureRepairs[index]
				return nil
			}
		}
		repair := FailureRepair{
			RepairID: repairID, Signature: input.Signature, PreviousEventID: input.PreviousEventID,
			WorkPacketID: packet.PacketID, RepairGeneration: generation + 1,
			RepositoryIdentity: identities["repository"], CandidateIdentity: identities["candidate"],
			ConfigIdentity: identities["config"], RuntimeIdentity: identities["runtime"],
			EvidenceIDs: input.EvidenceIDs, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		packet.FailureRepairs = append(packet.FailureRepairs, repair)
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if err := runtime.appendEvent("failure_repair_recorded", state.RunID, "advanced a verified failure repair generation", map[string]any{"repair_id": repair.RepairID, "signature": repair.Signature, "repair_generation": repair.RepairGeneration}); err != nil {
			return err
		}
		recorded = &repair
		return nil
	})
	return recorded, err
}

// currentFailureIdentities derives every repair boundary from facts observed by
// the governance runtime. Callers cannot claim that a repair changed the
// candidate, configuration or runtime by supplying arbitrary hashes.
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
	return map[string]string{
		"repository": snapshot.WorkingTreeHash,
		"candidate":  candidateIdentity,
		"config":     configIdentity,
		"runtime":    runtimeIdentity,
	}, nil
}

func (runtime *Runtime) RequestAdvisoryReview(requestID, reason string) (*ReviewRequest, error) {
	trigger, err := newReviewTrigger("advisory", "explicit-advisory", requestID, reason)
	if err != nil {
		return nil, err
	}
	var request *ReviewRequest
	err = runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.PendingIntervention != nil && state.PendingIntervention.Status == "awaiting_user" {
			return errors.New("pending intervention must be resolved before requesting another review")
		}
		if state.Review.Required {
			loaded, err := runtime.loadRequest()
			if err != nil {
				return err
			}
			request = loaded
			return nil
		}
		created, err := runtime.requestReviewLocked(state, trigger, false)
		if err != nil {
			return err
		}
		request = created
		return nil
	})
	return request, err
}

func (runtime *Runtime) RequestCompletionReview(sourceID string) (*ReviewRequest, error) {
	if err := nonempty("completion source_id", sourceID); err != nil {
		return nil, err
	}
	var request *ReviewRequest
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		evidenceSummary, err := hashJSON(map[string]any{
			"authority_hash": state.AuthorityHash,
			"conditions":     state.CompletionConditions,
			"results":        state.ReusableResults,
		})
		if err != nil {
			return err
		}
		trigger, err := newReviewTrigger("event", "completion-candidate", strings.TrimSpace(sourceID)+":"+evidenceSummary, "explicit completion candidate")
		if err != nil {
			return err
		}
		created, err := runtime.requestReviewLocked(state, trigger, false)
		if err != nil {
			return err
		}
		request = created
		return nil
	})
	return request, err
}

func (runtime *Runtime) requestEventReview(triggerType, sourceID, reason string) (*ReviewRequest, error) {
	trigger, err := newReviewTrigger("event", triggerType, sourceID, reason)
	if err != nil {
		return nil, err
	}
	var request *ReviewRequest
	err = runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.PendingIntervention != nil && state.PendingIntervention.Status == "awaiting_user" {
			return errors.New("pending intervention must be resolved before requesting another review")
		}
		created, err := runtime.requestReviewLocked(state, trigger, false)
		if err != nil {
			return err
		}
		request = created
		return nil
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
		if !oneOf(state.Status, "product_decision_required", "external_input_required") || state.PendingIntervention == nil {
			return errors.New("no pending user intervention")
		}
		if state.Review.Required {
			return errors.New("cannot resolve an intervention while a Governor review is pending")
		}
		pending := state.PendingIntervention
		if pending.InterventionID != input.InterventionID {
			return errors.New("intervention identity does not match the pending request")
		}
		if pending.Status != "awaiting_user" || pending.Resolution != nil {
			return errors.New("pending intervention is not awaiting a user response")
		}
		pending.Status = "resolution_pending_review"
		pending.Resolution = &InterventionResolution{
			SourceTurnID: strings.TrimSpace(input.SourceTurnID),
			Summary:      strings.TrimSpace(input.Summary),
			EvidenceRefs: normalizeStrings(input.EvidenceRefs),
			SubmittedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		}
		trigger, err := newReviewTrigger("event", "intervention-resolution", pending.InterventionID+":"+pending.Resolution.SourceTurnID, "user intervention resolved")
		if err != nil {
			return err
		}
		created, err := runtime.requestReviewLocked(state, trigger, false)
		if err != nil {
			return err
		}
		request = created
		return runtime.appendEvent("intervention_resolved", state.RunID, "recorded a safe user intervention resolution", map[string]any{
			"intervention_id": pending.InterventionID,
			"kind":            pending.Kind,
			"source_turn_id":  pending.Resolution.SourceTurnID,
		})
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
		if err != nil {
			return err
		}
		if current == state.AuthorityHash {
			return nil
		}
		previous := state.AuthorityHash
		state.AuthorityHash = current
		state.CurrentWorkPacket = nil
		state.PendingIntervention = nil
		for index := range state.CompletionConditions {
			if state.CompletionConditions[index].Status != "unmet" {
				state.CompletionConditions[index].Status = "evidence_insufficient"
			}
		}
		trigger, err := newReviewTrigger("event", "authority-change", previous+":"+current, "authority changed")
		if err != nil {
			return err
		}
		created, err := runtime.requestReviewLocked(state, trigger, true)
		if err != nil {
			return err
		}
		request = created
		return nil
	})
	return request, err
}

func newReviewTrigger(kind, triggerType, sourceID, reason string) (ReviewTrigger, error) {
	trigger := ReviewTrigger{
		Kind:     strings.TrimSpace(kind),
		Type:     strings.TrimSpace(triggerType),
		SourceID: strings.TrimSpace(sourceID),
		Reason:   strings.TrimSpace(reason),
	}
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
	hash, err := hashJSON(map[string]string{
		"run_id":    runID,
		"kind":      trigger.Kind,
		"type":      trigger.Type,
		"source_id": trigger.SourceID,
	})
	if err != nil {
		return "", err
	}
	return "trigger_" + strings.TrimPrefix(hash, "sha256:")[:32], nil
}

func (runtime *Runtime) requestReviewLocked(state *State, trigger ReviewTrigger, replacePending bool) (*ReviewRequest, error) {
	identity := reviewTriggerIdentity(trigger)
	instanceID, err := reviewTriggerInstanceID(state.RunID, trigger)
	if err != nil {
		return nil, err
	}
	if state.Review.Trigger != nil && *state.Review.Trigger == identity {
		if state.Review.Required {
			return runtime.loadRequest()
		}
		return nil, nil
	}
	if state.Review.Required {
		if !replacePending {
			return runtime.loadRequest()
		}
		if err := runtime.archiveSupersededReviewLocked(state); err != nil {
			return nil, err
		}
	}
	seen, err := runtime.reviewTriggerInstanceSeen(instanceID)
	if err != nil {
		return nil, err
	}
	if seen {
		return nil, nil
	}
	snapshot, err := runtime.repositorySnapshot()
	if err != nil {
		return nil, err
	}
	request := &ReviewRequest{
		SchemaVersion: schemaVersion, ReviewID: newID("review"), TriggerInstanceID: instanceID,
		Trigger: trigger, AuthorityPaths: normalizeStrings(runtime.Config.AuthorityPaths),
		CompletionDefinitionPaths: normalizeStrings(runtime.Config.CompletionDefinitionPaths), RepositorySnapshot: snapshot,
		StatePath: filepath.ToSlash(mustRelative(runtime.Root, runtime.statePath())), PendingIntervention: state.PendingIntervention, ResourceFacts: []ResourceFact{},
		EvidenceRefs: []EvidenceReference{}, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if state.CurrentWorkPacket != nil {
		request.CurrentConditionID = stringPointer(state.CurrentWorkPacket.ConditionID)
		request.CurrentWorkPacket = &RequestWorkPacket{
			PacketID: state.CurrentWorkPacket.PacketID, ConditionID: state.CurrentWorkPacket.ConditionID,
			Objective: state.CurrentWorkPacket.Objective, Value: state.CurrentWorkPacket.Value,
			AllowedScope: state.CurrentWorkPacket.AllowedScope, ExcludedScope: state.CurrentWorkPacket.ExcludedScope,
			ExpectedEvidence:      state.CurrentWorkPacket.ExpectedEvidence,
			CheckpointID:          state.CurrentWorkPacket.EvidenceCheckpoint.CheckpointID,
			CheckpointDescription: state.CurrentWorkPacket.EvidenceCheckpoint.Description,
			PlanHash:              state.CurrentWorkPacket.PlanHash,
		}
		if state.CurrentWorkPacket.EvidenceCheckpoint.Reached {
			request.RecentCheckpoint = &RecentCheckpoint{CheckpointID: state.CurrentWorkPacket.EvidenceCheckpoint.CheckpointID, Description: state.CurrentWorkPacket.EvidenceCheckpoint.Description, EvidenceIDs: conditionEvidence(state, state.CurrentWorkPacket.ConditionID)}
		}
	}
	for _, constraint := range state.ExplicitResourceConstraints {
		request.ResourceFacts = append(request.ResourceFacts, ResourceFact{Measure: constraint.Measure, Value: constraint.Limit, Unit: "configured-limit", Source: constraint.Source})
	}
	for _, result := range state.ReusableResults {
		path := resolvePath(runtime.Root, result.EvidencePath)
		hash, hashErr := fileHash(path)
		if hashErr != nil || hash != result.InputHash {
			continue
		}
		request.EvidenceRefs = append(request.EvidenceRefs, EvidenceReference{EvidenceID: result.ResultID, Path: result.EvidencePath, Hash: hash})
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
	state.Review.Required = true
	state.Review.ReviewID = stringPointer(request.ReviewID)
	state.Review.TriggerInstanceID = stringPointer(request.TriggerInstanceID)
	state.Review.ReviewSnapshotHash = stringPointer(request.ReviewSnapshotHash)
	state.Review.Trigger = stringPointer(identity)
	state.Review.DecisionPath = nil
	if state.CurrentWorkPacket != nil {
		state.CurrentWorkPacket.Approval = nil
	}
	next := "spawn the read-only Governor with review-request.json, wait for its JSON result, then accept-review and apply-review"
	state.NextAction = &next
	if err := atomicWriteJSON(runtime.requestPath(), request); err != nil {
		return nil, err
	}
	if err := runtime.saveState(state); err != nil {
		return nil, err
	}
	if err := runtime.appendEvent("review_requested", state.RunID, "created Governor review request", map[string]any{"review_id": request.ReviewID, "trigger_instance_id": request.TriggerInstanceID, "trigger": request.Trigger}); err != nil {
		return nil, err
	}
	return request, nil
}

func (runtime *Runtime) reviewTriggerInstanceSeen(instanceID string) (bool, error) {
	lines, err := readJSONLines(runtime.eventsPath())
	if err != nil {
		return false, err
	}
	for _, line := range lines {
		var event Event
		if json.Unmarshal(line, &event) == nil && event.Kind == "review_requested" && event.Fields["trigger_instance_id"] == instanceID {
			return true, nil
		}
	}
	return false, nil
}

func (runtime *Runtime) archiveSupersededReviewLocked(state *State) error {
	if !state.Review.Required || state.Review.ReviewID == nil {
		return nil
	}
	data, err := os.ReadFile(runtime.requestPath())
	if err != nil {
		return fmt.Errorf("pending review request cannot be preserved: %w", err)
	}
	directory := filepath.Join(runtime.reviewsDir(), "superseded")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	destination := filepath.Join(directory, *state.Review.ReviewID+".json")
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		if err := atomicWrite(destination, data); err != nil {
			return err
		}
		return runtime.appendEvent("review_superseded", state.RunID, "preserved a stale review before creating its current-snapshot replacement", map[string]any{"review_id": *state.Review.ReviewID})
	} else if err != nil {
		return err
	}
	return nil
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
		if !state.Review.Required || state.Review.ReviewID == nil || state.Review.TriggerInstanceID == nil || state.Review.ReviewSnapshotHash == nil {
			return errors.New("no pending review")
		}
		if result.ReviewID != request.ReviewID || result.TriggerInstanceID != request.TriggerInstanceID || result.ReviewSnapshotHash != request.ReviewSnapshotHash {
			return errors.New("review result identity does not match pending request")
		}
		if *state.Review.ReviewID != result.ReviewID || *state.Review.TriggerInstanceID != result.TriggerInstanceID || *state.Review.ReviewSnapshotHash != result.ReviewSnapshotHash {
			return errors.New("review result does not match current state")
		}
		if err := runtime.verifyReviewSnapshot(request, state); err != nil {
			return err
		}
		if err := validateReviewResultForState(&result, state); err != nil {
			return err
		}
		if err := os.MkdirAll(runtime.reviewsDir(), 0o755); err != nil {
			return err
		}
		path = filepath.Join(runtime.reviewsDir(), result.ReviewID+".json")
		if err := atomicWriteJSON(path, &result); err != nil {
			return err
		}
		relative := filepath.ToSlash(mustRelative(runtime.Root, path))
		state.Review.DecisionPath = &relative
		if err := runtime.saveState(state); err != nil {
			return err
		}
		return runtime.appendEvent("review_accepted", state.RunID, "accepted validated Governor result", map[string]any{"review_id": result.ReviewID, "decision": result.Decision})
	})
	return path, err
}

func (runtime *Runtime) verifyReviewSnapshot(request *ReviewRequest, state *State) error {
	authority, err := runtime.authorityHash()
	if err != nil {
		return err
	}
	if authority != state.AuthorityHash {
		return errors.New("authority changed while review was pending")
	}
	snapshot, err := runtime.repositorySnapshot()
	if err != nil {
		return err
	}
	if snapshot.HeadCommit != request.RepositorySnapshot.HeadCommit || snapshot.WorkingTreeHash != request.RepositorySnapshot.WorkingTreeHash {
		return errors.New("repository snapshot changed while review was pending")
	}
	if (request.CurrentWorkPacket == nil) != (state.CurrentWorkPacket == nil) {
		return errors.New("current work packet changed while review was pending")
	}
	if request.CurrentWorkPacket != nil && (request.CurrentWorkPacket.PacketID != state.CurrentWorkPacket.PacketID || request.CurrentWorkPacket.PlanHash != state.CurrentWorkPacket.PlanHash) {
		return errors.New("current work packet identity changed while review was pending")
	}
	requestInterventionHash, err := hashJSON(request.PendingIntervention)
	if err != nil {
		return err
	}
	stateInterventionHash, err := hashJSON(state.PendingIntervention)
	if err != nil {
		return err
	}
	if requestInterventionHash != stateInterventionHash {
		return errors.New("pending intervention changed while review was pending")
	}
	return nil
}

func (runtime *Runtime) ApplyReview() (*State, error) {
	var applied *State
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if !state.Review.Required || state.Review.DecisionPath == nil {
			return errors.New("no accepted review is ready to apply")
		}
		var result ReviewResult
		if err := decodeStrictFile(resolvePath(runtime.Root, *state.Review.DecisionPath), &result); err != nil {
			return err
		}
		if err := validateReviewResult(&result); err != nil {
			return err
		}
		if state.Review.ReviewID == nil || state.Review.TriggerInstanceID == nil || state.Review.ReviewSnapshotHash == nil || result.ReviewID != *state.Review.ReviewID || result.TriggerInstanceID != *state.Review.TriggerInstanceID || result.ReviewSnapshotHash != *state.Review.ReviewSnapshotHash {
			return errors.New("accepted review no longer matches current state")
		}
		if err := ensureKnownEvidence(state, result.ValidatedEvidenceIDs); err != nil {
			return err
		}
		if err := ensureKnownEvidence(state, result.PreservedResultIDs); err != nil {
			return err
		}
		protectedResults := append(append([]string{}, result.PreservedResultIDs...), result.ValidatedEvidenceIDs...)
		if err := applyInvalidations(state, result.InvalidatedItems, protectedResults); err != nil {
			return err
		}
		previousCondition := ""
		if state.CurrentWorkPacket != nil {
			previousCondition = state.CurrentWorkPacket.ConditionID
		}
		switch result.Decision {
		case "start":
			if state.CurrentWorkPacket != nil {
				return errors.New("start requires no existing work packet")
			}
			packet, err := packetFromProposal(result.NextWorkPacket)
			if err != nil {
				return err
			}
			packet.Approval = &Approval{Status: "approved", ReviewID: result.ReviewID, TriggerInstanceID: result.TriggerInstanceID, ReviewSnapshotHash: result.ReviewSnapshotHash, ValidUntilCheckpoint: packet.EvidenceCheckpoint.CheckpointID}
			state.CurrentWorkPacket = packet
			setConditionStatus(state, packet.ConditionID, "in_progress", nil)
			state.PendingIntervention = nil
			state.Status = "running"
			next := "execute the approved work packet until its natural evidence checkpoint"
			state.NextAction = &next
		case "continue":
			if state.CurrentWorkPacket == nil {
				return errors.New("continue requires an existing work packet")
			}
			if state.CurrentWorkPacket.EvidenceCheckpoint.Reached {
				return errors.New("continue cannot reuse a work packet whose evidence checkpoint is already reached")
			}
			state.CurrentWorkPacket.Approval = &Approval{Status: "approved", ReviewID: result.ReviewID, TriggerInstanceID: result.TriggerInstanceID, ReviewSnapshotHash: result.ReviewSnapshotHash, ValidUntilCheckpoint: state.CurrentWorkPacket.EvidenceCheckpoint.CheckpointID}
			state.PendingIntervention = nil
			state.Status = "running"
			next := "continue the approved work packet from its persisted evidence checkpoint"
			state.NextAction = &next
		case "replan", "stage_complete":
			if result.Decision == "stage_complete" {
				if previousCondition == "" {
					return errors.New("stage_complete requires an existing work packet")
				}
				setConditionStatus(state, previousCondition, "met", result.ValidatedEvidenceIDs)
			}
			packet, err := packetFromProposal(result.NextWorkPacket)
			if err != nil {
				return err
			}
			packet.Approval = &Approval{Status: "approved", ReviewID: result.ReviewID, TriggerInstanceID: result.TriggerInstanceID, ReviewSnapshotHash: result.ReviewSnapshotHash, ValidUntilCheckpoint: packet.EvidenceCheckpoint.CheckpointID}
			state.CurrentWorkPacket = packet
			setConditionStatus(state, packet.ConditionID, "in_progress", nil)
			state.PendingIntervention = nil
			state.Status = "running"
			next := "execute the approved work packet until its natural evidence checkpoint"
			state.NextAction = &next
		case "task_complete":
			for index := range state.CompletionConditions {
				state.CompletionConditions[index].Status = "met"
				state.CompletionConditions[index].EvidenceIDs = append([]string(nil), result.ValidatedEvidenceIDs...)
			}
			state.CurrentWorkPacket = nil
			state.PendingIntervention = nil
			state.Status = "running"
			next := "run governance-cli finish to mechanically close the completed task"
			state.NextAction = &next
		case "product_decision_required":
			state.Status = "product_decision_required"
			state.PendingIntervention = pendingInterventionFromResult(result)
			next := result.ExternalInput.MinimumUserInput
			state.NextAction = &next
		case "external_input_required":
			state.Status = "external_input_required"
			state.PendingIntervention = pendingInterventionFromResult(result)
			next := result.ExternalInput.MinimumUserInput
			state.NextAction = &next
		}
		state.Review.Required = false
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if err := runtime.appendEvent("review_applied", state.RunID, "applied Governor decision", map[string]any{"review_id": result.ReviewID, "decision": result.Decision}); err != nil {
			return err
		}
		applied = state
		return nil
	})
	return applied, err
}

func pendingInterventionFromResult(result ReviewResult) *PendingIntervention {
	return &PendingIntervention{
		InterventionID:   newID("intervention"),
		SourceReviewID:   result.ReviewID,
		Kind:             result.ExternalInput.Kind,
		Fact:             strings.TrimSpace(result.ExternalInput.Fact),
		ExhaustedPaths:   normalizeStrings(result.ExternalInput.ExhaustedPaths),
		MinimumUserInput: strings.TrimSpace(result.ExternalInput.MinimumUserInput),
		Status:           "awaiting_user",
		Resolution:       nil,
	}
}

func (runtime *Runtime) CloseWorkPacket() error {
	return runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.CurrentWorkPacket == nil || !state.CurrentWorkPacket.EvidenceCheckpoint.Reached {
			return errors.New("work packet has not reached its natural checkpoint")
		}
		if state.Review.Required {
			return errors.New("checkpoint review is still pending")
		}
		conditionID := state.CurrentWorkPacket.ConditionID
		setConditionStatus(state, conditionID, "met", conditionEvidence(state, conditionID))
		state.CurrentWorkPacket = nil
		next := "request Governor review for the next highest-priority unmet condition"
		state.NextAction = &next
		return runtime.saveState(state)
	})
}

func (runtime *Runtime) Finish() error {
	return runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.Review.Required || state.Review.DecisionPath == nil {
			return errors.New("finish requires an applied task_complete review")
		}
		var result ReviewResult
		if err := decodeStrictFile(resolvePath(runtime.Root, *state.Review.DecisionPath), &result); err != nil {
			return err
		}
		if result.Decision != "task_complete" {
			return errors.New("only task_complete can finish the task")
		}
		for _, condition := range state.CompletionConditions {
			if condition.Status != "met" || len(condition.EvidenceIDs) == 0 {
				return fmt.Errorf("completion condition %q is not fully evidenced", condition.ConditionID)
			}
		}
		state.Status = "complete"
		state.CurrentWorkPacket = nil
		state.NextAction = nil
		if err := runtime.saveState(state); err != nil {
			return err
		}
		return runtime.appendEvent("task_finished", state.RunID, "mechanically closed task_complete decision", map[string]any{"review_id": result.ReviewID})
	})
}

func (runtime *Runtime) allowedEvidencePath(path string) bool {
	for _, configured := range runtime.Config.EvidenceRoots {
		if within(resolvePath(runtime.Root, configured), path) {
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
		if state.CompletionConditions[index].ConditionID != conditionID {
			continue
		}
		state.CompletionConditions[index].Status = status
		if len(evidence) > 0 {
			state.CompletionConditions[index].EvidenceIDs = normalizeStrings(append(state.CompletionConditions[index].EvidenceIDs, evidence...))
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

func expectedEvidenceReached(packet *WorkPacket, results []ReusableResult) bool {
	known := map[string]struct{}{}
	for _, result := range results {
		known[result.ResultID] = struct{}{}
	}
	for _, expected := range packet.ExpectedEvidence {
		if _, exists := known[expected]; !exists {
			return false
		}
	}
	return true
}

func ensureKnownEvidence(state *State, ids []string) error {
	known := map[string]struct{}{}
	for _, result := range state.ReusableResults {
		known[result.ResultID] = struct{}{}
	}
	for _, id := range ids {
		if _, exists := known[id]; !exists {
			return fmt.Errorf("Governor validated unknown evidence id %q", id)
		}
	}
	return nil
}

func applyInvalidations(state *State, invalidated, preserved []string) error {
	invalid := map[string]struct{}{}
	for _, item := range invalidated {
		invalid[item] = struct{}{}
	}
	for _, item := range preserved {
		if _, conflict := invalid[item]; conflict {
			return fmt.Errorf("result %q cannot be both preserved and invalidated", item)
		}
	}
	if len(invalid) == 0 {
		return nil
	}
	kept := state.ReusableResults[:0]
	for _, result := range state.ReusableResults {
		if _, remove := invalid[result.ResultID]; !remove {
			kept = append(kept, result)
		}
	}
	state.ReusableResults = kept
	for index := range state.CompletionConditions {
		var evidence []string
		for _, evidenceID := range state.CompletionConditions[index].EvidenceIDs {
			if _, remove := invalid[evidenceID]; !remove {
				evidence = append(evidence, evidenceID)
			}
		}
		state.CompletionConditions[index].EvidenceIDs = evidence
		if state.CompletionConditions[index].Status == "met" && len(evidence) == 0 {
			state.CompletionConditions[index].Status = "evidence_insufficient"
		}
	}
	return nil
}

func normalizeFailureSignature(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = regexp.MustCompile(`\b[0-9]+(?:\.[0-9]+)?\b`).ReplaceAllString(value, "#")
	value = strings.Join(strings.Fields(value), " ")
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
		return path
	}
	return relative
}
