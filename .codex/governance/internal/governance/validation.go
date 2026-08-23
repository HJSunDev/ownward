package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

func nonempty(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	return nil
}

func validHash(name, value string) error {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("%s is not a sha256 identity", name)
	}
	for _, char := range value[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", char) {
			return fmt.Errorf("%s is not a sha256 identity", name)
		}
	}
	return nil
}

func uniqueNonempty(name string, values []string, require bool) error {
	if require && len(values) == 0 {
		return fmt.Errorf("%s must not be empty", name)
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s contains an empty value", name)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateState(state *State) error {
	if state == nil || state.SchemaVersion != schemaVersion {
		return fmt.Errorf("state schema_version must be %d", schemaVersion)
	}
	if err := nonempty("run_id", state.RunID); err != nil {
		return err
	}
	if !oneOf(state.Status, "active", "awaiting_user", "complete") {
		return fmt.Errorf("invalid governance status %q", state.Status)
	}
	if err := validHash("authority_hash", state.AuthorityHash); err != nil {
		return err
	}
	seenConditions := map[string]struct{}{}
	for _, condition := range state.CompletionConditions {
		if err := nonempty("condition_id", condition.ConditionID); err != nil {
			return err
		}
		if _, exists := seenConditions[condition.ConditionID]; exists {
			return fmt.Errorf("duplicate completion condition %q", condition.ConditionID)
		}
		seenConditions[condition.ConditionID] = struct{}{}
		if !oneOf(condition.Status, "unmet", "in_progress", "met", "evidence_insufficient") {
			return fmt.Errorf("invalid condition status %q", condition.Status)
		}
		if err := uniqueNonempty("condition evidence_ids", condition.EvidenceIDs, false); err != nil {
			return err
		}
	}
	if state.CurrentFocus != nil {
		if err := validateExecutionSnapshot(state.CurrentFocus); err != nil {
			return err
		}
		if _, exists := seenConditions[state.CurrentFocus.ConditionID]; !exists {
			return errors.New("current focus references an unknown completion condition")
		}
	}
	if state.PendingIntervention != nil {
		if err := validatePendingIntervention(state.PendingIntervention); err != nil {
			return err
		}
		if state.Status != "awaiting_user" && state.PendingIntervention.Status == "awaiting_user" {
			return errors.New("an awaiting user intervention requires status awaiting_user")
		}
	} else if state.Status == "awaiting_user" {
		return errors.New("status awaiting_user requires a pending intervention")
	}
	if state.Status == "complete" && state.CurrentFocus != nil {
		return errors.New("complete state cannot retain a current focus")
	}
	if err := validateReviewState(&state.Review); err != nil {
		return err
	}
	for _, result := range state.ReusableResults {
		if err := nonempty("result_id", result.ResultID); err != nil {
			return err
		}
		if err := validHash("result input_hash", result.InputHash); err != nil {
			return err
		}
	}
	if state.Owner != nil {
		if state.Owner.OwnerEpoch == 0 || strings.TrimSpace(state.Owner.SessionID) == "" {
			return errors.New("governance owner identity is invalid")
		}
		if _, err := time.Parse(time.RFC3339Nano, state.Owner.AcquiredAt); err != nil {
			return errors.New("governance owner acquired_at is invalid")
		}
	}
	if state.Handoff != nil {
		if state.Owner == nil || !oneOf(state.Handoff.Status, "prepared", "bound") || state.Handoff.SourceEpoch != state.Owner.OwnerEpoch || state.Handoff.SourceSessionID != state.Owner.SessionID {
			return errors.New("governance handoff is inconsistent with its owner")
		}
	}
	return nil
}

func validateReviewState(review *ReviewState) error {
	if review == nil || !oneOf(review.Status, "idle", "requested", "feedback_ready", "responded", "missed") {
		return errors.New("review status is invalid")
	}
	if review.FixedReviewGeneration < 0 {
		return errors.New("fixed_review_generation must be non-negative")
	}
	active := review.Status != "idle"
	if active && (review.ReviewID == nil || review.TriggerInstanceID == nil || review.ReviewSnapshotHash == nil || review.Trigger == nil) {
		return errors.New("non-idle review must carry its complete identity")
	}
	if review.ReviewSnapshotHash != nil {
		if err := validHash("review_snapshot_hash", *review.ReviewSnapshotHash); err != nil {
			return err
		}
	}
	if review.Status == "requested" && (review.FeedbackPath != nil || review.Response != nil) {
		return errors.New("requested review cannot contain feedback or a response")
	}
	if review.Status == "feedback_ready" && (review.FeedbackPath == nil || review.Response != nil) {
		return errors.New("feedback_ready review requires feedback and no response")
	}
	if review.Status == "responded" && (review.FeedbackPath == nil || review.Response == nil) {
		return errors.New("responded review requires feedback and a main-Agent response")
	}
	if review.Status == "missed" && review.Response != nil {
		return errors.New("missed review cannot contain a response")
	}
	return nil
}

func validateExecutionSnapshot(snapshot *ExecutionSnapshot) error {
	if snapshot == nil {
		return errors.New("execution snapshot is required")
	}
	for name, value := range map[string]string{"focus_id": snapshot.FocusID, "condition_id": snapshot.ConditionID, "objective": snapshot.Objective, "value": snapshot.Value, "checkpoint_id": snapshot.EvidenceCheckpoint.CheckpointID, "checkpoint_description": snapshot.EvidenceCheckpoint.Description, "started_at": snapshot.StartedAt} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if err := uniqueNonempty("involved_scope", snapshot.InvolvedScope, true); err != nil {
		return err
	}
	if err := uniqueNonempty("expected_evidence", snapshot.ExpectedEvidence, true); err != nil {
		return err
	}
	if err := validHash("snapshot_hash", snapshot.SnapshotHash); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, snapshot.StartedAt); err != nil {
		return errors.New("execution snapshot started_at is invalid")
	}
	if snapshot.LastEvidenceAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *snapshot.LastEvidenceAt); err != nil {
			return errors.New("execution snapshot last_evidence_at is invalid")
		}
	}
	if snapshot.EvidenceCheckpoint.Reached && (snapshot.Checkpoint == nil || *snapshot.Checkpoint != snapshot.EvidenceCheckpoint.CheckpointID) {
		return errors.New("reached evidence checkpoint must be reflected by checkpoint")
	}
	for _, event := range snapshot.FailureEvents {
		if event.FocusID != snapshot.FocusID || event.EventID == "" || event.Signature == "" {
			return errors.New("failure event is not bound to the current focus")
		}
	}
	for _, repair := range snapshot.FailureRepairs {
		if repair.FocusID != snapshot.FocusID || repair.RepairID == "" || repair.RepairGeneration < 1 {
			return errors.New("failure repair is not bound to the current focus")
		}
	}
	return nil
}

func validateReviewRequest(request *ReviewRequest) error {
	if request == nil || request.SchemaVersion != schemaVersion {
		return fmt.Errorf("review request schema_version must be %d", schemaVersion)
	}
	for name, value := range map[string]string{"review_id": request.ReviewID, "trigger_instance_id": request.TriggerInstanceID, "repository.root": request.RepositorySnapshot.Root, "repository.head_commit": request.RepositorySnapshot.HeadCommit, "state_path": request.StatePath, "created_at": request.CreatedAt} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if err := validHash("review_snapshot_hash", request.ReviewSnapshotHash); err != nil {
		return err
	}
	if err := validHash("repository.working_tree_hash", request.RepositorySnapshot.WorkingTreeHash); err != nil {
		return err
	}
	if err := validateReviewTrigger(request.Trigger); err != nil {
		return err
	}
	if err := uniqueNonempty("authority_paths", request.AuthorityPaths, true); err != nil {
		return err
	}
	if err := uniqueNonempty("completion_definition_paths", request.CompletionDefinitionPaths, true); err != nil {
		return err
	}
	if request.CurrentFocus != nil {
		input := &ExecutionSnapshotInput{FocusID: request.CurrentFocus.FocusID, ConditionID: request.CurrentFocus.ConditionID, Objective: request.CurrentFocus.Objective, Value: request.CurrentFocus.Value, InvolvedScope: request.CurrentFocus.InvolvedScope, ExpectedEvidence: request.CurrentFocus.ExpectedEvidence, CheckpointID: request.CurrentFocus.CheckpointID, CheckpointDescription: request.CurrentFocus.CheckpointDescription}
		if err := validateExecutionSnapshotInput(input); err != nil {
			return err
		}
		if err := validHash("current_focus.snapshot_hash", request.CurrentFocus.SnapshotHash); err != nil {
			return err
		}
	}
	if request.PendingIntervention != nil {
		if err := validatePendingIntervention(request.PendingIntervention); err != nil {
			return err
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, request.CreatedAt); err != nil {
		return errors.New("review request created_at must be RFC3339")
	}
	return nil
}

func validateReviewTrigger(trigger ReviewTrigger) error {
	for name, value := range map[string]string{"trigger.kind": trigger.Kind, "trigger.type": trigger.Type, "trigger.source_id": trigger.SourceID, "trigger.reason": trigger.Reason} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	allowed := map[string][]string{
		"fixed":    {"activation", "session-start", "post-compact", "legacy-state-migration"},
		"event":    {"focus-change", "evidence-checkpoint", "evidence-checkpoint-missed", "evidence-identity-change", "repeated-failure", "authority-change", "intervention-resolution", "failure-recording-integrity", "completion-candidate"},
		"advisory": {"explicit-advisory"},
	}
	types, exists := allowed[trigger.Kind]
	if !exists || !oneOf(trigger.Type, types...) {
		return fmt.Errorf("review trigger %q/%q is not an allowed structured trigger", trigger.Kind, trigger.Type)
	}
	return nil
}

func validateReviewResult(result *ReviewResult) error {
	if result == nil {
		return errors.New("Governor feedback is required")
	}
	for name, value := range map[string]string{"review_id": result.ReviewID, "trigger_instance_id": result.TriggerInstanceID, "reason": result.Reason, "macro_assessment.overall_progress": result.MacroAssessment.OverallProgress, "macro_assessment.evidence_support": result.MacroAssessment.EvidenceSupport} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if err := validHash("review_snapshot_hash", result.ReviewSnapshotHash); err != nil {
		return err
	}
	if !oneOf(result.Recommendation, "continue", "adjust", "stage_complete", "goal_complete", "product_decision_needed", "external_input_needed") {
		return fmt.Errorf("invalid Governor recommendation %q", result.Recommendation)
	}
	if err := uniqueNonempty("preserved_result_ids", result.PreservedResultIDs, false); err != nil {
		return err
	}
	if err := uniqueNonempty("validated_evidence_ids", result.ValidatedEvidenceIDs, false); err != nil {
		return err
	}
	switch result.Recommendation {
	case "continue":
		if result.SuggestedFocus != nil || result.ExternalInput != nil || !result.PathAssessment.Necessary || !result.PathAssessment.Efficient || !result.PathAssessment.Optimal {
			return errors.New("continue feedback must affirm the current path without a replacement focus")
		}
	case "adjust", "stage_complete":
		if result.SuggestedFocus == nil || result.ExternalInput != nil || result.HighestPriorityGap == nil {
			return errors.New("adjust and stage_complete feedback require a suggested focus and no external input")
		}
		if err := validateExecutionSnapshotInput(result.SuggestedFocus); err != nil {
			return err
		}
		if result.Recommendation == "adjust" && (result.PathAssessment.Optimal || len(result.PathAssessment.Problems) == 0 || len(result.PathAssessment.BetterPlan) == 0) {
			return errors.New("adjust feedback must identify a real path problem and a better plan")
		}
	case "goal_complete":
		if result.HighestPriorityGap != nil || result.SuggestedFocus != nil || result.ExternalInput != nil || len(result.ValidatedEvidenceIDs) == 0 || len(result.MacroAssessment.Unmet) != 0 {
			return errors.New("goal_complete feedback must bind complete evidence without remaining work")
		}
	case "product_decision_needed", "external_input_needed":
		if result.HighestPriorityGap == nil || result.SuggestedFocus != nil || result.ExternalInput == nil {
			return errors.New("user-input feedback requires one bounded external input")
		}
		if err := validateExternalInput(result.ExternalInput); err != nil {
			return err
		}
		if result.Recommendation == "product_decision_needed" && result.ExternalInput.Kind != "product_decision" {
			return errors.New("product_decision_needed requires product_decision input")
		}
		if result.Recommendation == "external_input_needed" && result.ExternalInput.Kind == "product_decision" {
			return errors.New("external_input_needed cannot request a product decision")
		}
	}
	return nil
}

func validateReviewResultForState(result *ReviewResult, request *ReviewRequest, state *State) error {
	if result == nil || request == nil || state == nil {
		return errors.New("Governor feedback, request and state are required")
	}
	if err := ensureKnownEvidence(state, result.ValidatedEvidenceIDs); err != nil {
		return err
	}
	if err := ensureKnownEvidence(state, result.PreservedResultIDs); err != nil {
		return err
	}
	if err := ensureReferencedEvidence(request, append(append([]string(nil), result.ValidatedEvidenceIDs...), result.PreservedResultIDs...)); err != nil {
		return err
	}
	return nil
}

func validateReviewResponseInput(input *ReviewResponseInput) error {
	if input == nil {
		return errors.New("review response is required")
	}
	for name, value := range map[string]string{"review_id": input.ReviewID, "reason": input.Reason, "next_validation_point": input.NextValidationPoint} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if !oneOf(input.Disposition, "adopt", "decline", "acknowledge") {
		return errors.New("review response disposition must be adopt, decline or acknowledge")
	}
	return nil
}

func validatePendingIntervention(pending *PendingIntervention) error {
	if pending == nil {
		return errors.New("pending intervention is required")
	}
	for name, value := range map[string]string{"intervention_id": pending.InterventionID, "source_review_id": pending.SourceReviewID, "fact": pending.Fact, "minimum_user_input": pending.MinimumUserInput} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if !oneOf(pending.Kind, "product_decision", "permission", "credential", "external_state") || !oneOf(pending.Status, "awaiting_user", "resolution_pending_review") {
		return errors.New("pending intervention kind or status is invalid")
	}
	if err := uniqueNonempty("pending intervention exhausted_paths", pending.ExhaustedPaths, true); err != nil {
		return err
	}
	if pending.Status == "awaiting_user" && pending.Resolution != nil {
		return errors.New("awaiting_user intervention cannot contain a resolution")
	}
	if pending.Status == "resolution_pending_review" {
		if pending.Resolution == nil {
			return errors.New("resolution_pending_review requires a resolution")
		}
		return validateInterventionResolution(pending.Resolution)
	}
	return nil
}

func validateExternalInput(input *ExternalInput) error {
	if input == nil || !oneOf(input.Kind, "product_decision", "permission", "credential", "external_state") {
		return errors.New("external input kind is invalid")
	}
	if err := nonempty("external input fact", input.Fact); err != nil {
		return err
	}
	if err := uniqueNonempty("external input exhausted_paths", input.ExhaustedPaths, true); err != nil {
		return err
	}
	return nonempty("external input minimum_user_input", input.MinimumUserInput)
}

func validateInterventionResolutionInput(input *ResolveInterventionInput) error {
	if input == nil {
		return errors.New("intervention resolution is required")
	}
	for name, value := range map[string]string{"intervention_id": input.InterventionID, "source_turn_id": input.SourceTurnID, "summary": input.Summary} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if len(input.Summary) > 4000 {
		return errors.New("intervention summary is too long")
	}
	return uniqueNonempty("intervention evidence_refs", input.EvidenceRefs, false)
}

func validateInterventionResolution(resolution *InterventionResolution) error {
	if resolution == nil {
		return errors.New("intervention resolution is required")
	}
	input := ResolveInterventionInput{SourceTurnID: resolution.SourceTurnID, Summary: resolution.Summary, EvidenceRefs: resolution.EvidenceRefs, InterventionID: "persisted"}
	if err := validateInterventionResolutionInput(&input); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, resolution.SubmittedAt); err != nil {
		return errors.New("intervention resolution submitted_at is invalid")
	}
	return nil
}

func validateExecutionSnapshotInput(input *ExecutionSnapshotInput) error {
	if input == nil {
		return errors.New("execution snapshot input is required")
	}
	for name, value := range map[string]string{"focus_id": input.FocusID, "condition_id": input.ConditionID, "objective": input.Objective, "value": input.Value, "checkpoint_id": input.CheckpointID, "checkpoint_description": input.CheckpointDescription} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if err := uniqueNonempty("involved_scope", input.InvolvedScope, true); err != nil {
		return err
	}
	return uniqueNonempty("expected_evidence", input.ExpectedEvidence, true)
}

func ensureKnownEvidence(state *State, ids []string) error {
	known := map[string]struct{}{}
	for _, result := range state.ReusableResults {
		known[result.ResultID] = struct{}{}
	}
	for _, id := range ids {
		if _, exists := known[id]; !exists {
			return fmt.Errorf("unknown evidence id %q", id)
		}
	}
	return nil
}

func ensureReferencedEvidence(request *ReviewRequest, ids []string) error {
	referenced := map[string]struct{}{}
	for _, evidence := range request.EvidenceRefs {
		referenced[evidence.EvidenceID] = struct{}{}
	}
	for _, id := range normalizeStrings(ids) {
		if _, exists := referenced[id]; !exists {
			return fmt.Errorf("Governor referenced evidence id %q that was not present in its review request", id)
		}
	}
	return nil
}

func validateSchemaDocuments(runtime *Runtime) error {
	for _, configured := range []string{runtime.Config.StateSchemaPath, runtime.Config.ReviewRequestSchemaPath, runtime.Config.ReviewSchemaPath} {
		data, err := os.ReadFile(resolvePath(runtime.Root, configured))
		if err != nil {
			return err
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			return fmt.Errorf("invalid JSON schema %s: %w", configured, err)
		}
		if document["type"] != "object" || document["additionalProperties"] != false {
			return fmt.Errorf("schema %s is not a closed object contract", configured)
		}
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
