package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

var hashPattern = regexp.MustCompile(`^sha256:[0-9a-fA-F]{64}$`)

func nonempty(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	return nil
}

func validHash(name, value string) error {
	if !hashPattern.MatchString(value) {
		return fmt.Errorf("%s is not a sha256 identity", name)
	}
	return nil
}

func uniqueNonempty(name string, values []string, require bool) error {
	if require && len(values) == 0 {
		return fmt.Errorf("%s must not be empty", name)
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if err := nonempty(name, value); err != nil {
			return err
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
		return errors.New("state schema_version must be 1")
	}
	if err := nonempty("run_id", state.RunID); err != nil {
		return err
	}
	if err := validHash("authority_hash", state.AuthorityHash); err != nil {
		return err
	}
	if !oneOf(state.Status, "running", "product_decision_required", "external_input_required", "complete") {
		return fmt.Errorf("invalid state status %q", state.Status)
	}
	conditionIDs := map[string]struct{}{}
	for _, condition := range state.CompletionConditions {
		if err := nonempty("condition_id", condition.ConditionID); err != nil {
			return err
		}
		if _, exists := conditionIDs[condition.ConditionID]; exists {
			return fmt.Errorf("duplicate completion condition %q", condition.ConditionID)
		}
		conditionIDs[condition.ConditionID] = struct{}{}
		if !oneOf(condition.Status, "unmet", "in_progress", "met", "evidence_insufficient") {
			return fmt.Errorf("invalid completion condition status %q", condition.Status)
		}
		if err := uniqueNonempty("completion evidence_ids", condition.EvidenceIDs, false); err != nil {
			return err
		}
	}
	if len(conditionIDs) == 0 {
		return errors.New("completion_conditions must not be empty")
	}
	for _, result := range state.ReusableResults {
		if err := nonempty("result_id", result.ResultID); err != nil {
			return err
		}
		if err := uniqueNonempty("result scope", result.Scope, false); err != nil {
			return err
		}
		if err := nonempty("evidence_path", result.EvidencePath); err != nil {
			return err
		}
		if err := validHash("input_hash", result.InputHash); err != nil {
			return err
		}
	}
	if state.CurrentWorkPacket != nil {
		if _, exists := conditionIDs[state.CurrentWorkPacket.ConditionID]; !exists {
			return fmt.Errorf("work packet references unknown condition %q", state.CurrentWorkPacket.ConditionID)
		}
		if err := validateWorkPacket(state.CurrentWorkPacket); err != nil {
			return err
		}
	}
	if state.PendingIntervention != nil {
		if err := validatePendingIntervention(state.PendingIntervention); err != nil {
			return err
		}
	}
	switch state.Status {
	case "product_decision_required":
		if state.PendingIntervention == nil || state.PendingIntervention.Kind != "product_decision" {
			return errors.New("product_decision_required requires a matching pending intervention")
		}
	case "external_input_required":
		if state.PendingIntervention == nil || !oneOf(state.PendingIntervention.Kind, "permission", "credential", "external_state") {
			return errors.New("external_input_required requires a matching pending intervention")
		}
	case "running", "complete":
		if state.PendingIntervention != nil {
			return fmt.Errorf("%s state cannot retain a pending intervention", state.Status)
		}
	}
	if state.Review.FixedReviewGeneration < 0 {
		return errors.New("fixed_review_generation must be non-negative")
	}
	if state.Review.Required {
		if state.Review.ReviewID == nil || state.Review.TriggerInstanceID == nil || state.Review.ReviewSnapshotHash == nil || state.Review.Trigger == nil {
			return errors.New("required review must carry request identity and trigger")
		}
		if err := validHash("review_snapshot_hash", *state.Review.ReviewSnapshotHash); err != nil {
			return err
		}
	}
	if state.Status == "complete" && state.Review.Required {
		return errors.New("complete state cannot require review")
	}
	return nil
}

func validateWorkPacket(packet *WorkPacket) error {
	if packet == nil {
		return errors.New("work packet is required")
	}
	for name, value := range map[string]string{
		"packet_id": packet.PacketID, "condition_id": packet.ConditionID, "objective": packet.Objective,
		"value": packet.Value, "checkpoint_id": packet.EvidenceCheckpoint.CheckpointID,
		"checkpoint_description": packet.EvidenceCheckpoint.Description, "started_at": packet.StartedAt,
	} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, packet.StartedAt); err != nil {
		return errors.New("work packet started_at must be RFC3339")
	}
	if err := uniqueNonempty("allowed_scope", packet.AllowedScope, true); err != nil {
		return err
	}
	if err := uniqueNonempty("excluded_scope", packet.ExcludedScope, false); err != nil {
		return err
	}
	if err := uniqueNonempty("expected_evidence", packet.ExpectedEvidence, true); err != nil {
		return err
	}
	if err := uniqueNonempty("failure_signatures", packet.FailureSignatures, false); err != nil {
		return err
	}
	if err := validHash("plan_hash", packet.PlanHash); err != nil {
		return err
	}
	expectedPlanHash, err := workPacketPlanHash(packet)
	if err != nil {
		return err
	}
	if packet.PlanHash != expectedPlanHash {
		return errors.New("work packet plan_hash does not match its governed plan")
	}
	if packet.Approval != nil {
		if packet.Approval.Status != "approved" || packet.Approval.ValidUntilCheckpoint != packet.EvidenceCheckpoint.CheckpointID {
			return errors.New("work packet approval is invalid")
		}
		if err := validHash("approval review_snapshot_hash", packet.Approval.ReviewSnapshotHash); err != nil {
			return err
		}
	}
	return nil
}

func workPacketPlanHash(packet *WorkPacket) (string, error) {
	proposal := WorkPacketProposal{
		PacketID: packet.PacketID, ConditionID: packet.ConditionID, Objective: packet.Objective, Value: packet.Value,
		AllowedScope: normalizeStrings(packet.AllowedScope), ExcludedScope: normalizeStrings(packet.ExcludedScope),
		ExpectedEvidence: normalizeStrings(packet.ExpectedEvidence), CheckpointID: packet.EvidenceCheckpoint.CheckpointID,
		CheckpointDescription: packet.EvidenceCheckpoint.Description,
	}
	return hashJSON(proposal)
}

func validateReviewRequest(request *ReviewRequest) error {
	if request == nil || request.SchemaVersion != schemaVersion {
		return errors.New("review request schema_version must be 1")
	}
	for name, value := range map[string]string{
		"review_id": request.ReviewID, "trigger_instance_id": request.TriggerInstanceID,
		"trigger.reason": request.Trigger.Reason, "repository.root": request.RepositorySnapshot.Root,
		"repository.head_commit": request.RepositorySnapshot.HeadCommit, "state_path": request.StatePath,
		"created_at": request.CreatedAt,
	} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if !oneOf(request.Trigger.Kind, "fixed", "event") {
		return errors.New("review trigger kind must be fixed or event")
	}
	if err := validHash("review_snapshot_hash", request.ReviewSnapshotHash); err != nil {
		return err
	}
	if err := validHash("repository.working_tree_hash", request.RepositorySnapshot.WorkingTreeHash); err != nil {
		return err
	}
	if err := uniqueNonempty("authority_paths", request.AuthorityPaths, true); err != nil {
		return err
	}
	if err := uniqueNonempty("completion_definition_paths", request.CompletionDefinitionPaths, true); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, request.CreatedAt); err != nil {
		return errors.New("review request created_at must be RFC3339")
	}
	for _, evidence := range request.EvidenceRefs {
		if err := nonempty("evidence_id", evidence.EvidenceID); err != nil {
			return err
		}
		if err := nonempty("evidence path", evidence.Path); err != nil {
			return err
		}
		if err := validHash("evidence hash", evidence.Hash); err != nil {
			return err
		}
	}
	if request.PendingIntervention != nil {
		if err := validatePendingIntervention(request.PendingIntervention); err != nil {
			return fmt.Errorf("invalid pending intervention in review request: %w", err)
		}
	}
	return nil
}

func validateReviewResult(result *ReviewResult) error {
	if result == nil {
		return errors.New("review result is required")
	}
	for name, value := range map[string]string{
		"review_id": result.ReviewID, "trigger_instance_id": result.TriggerInstanceID,
		"macro_assessment.overall_progress": result.MacroAssessment.OverallProgress,
		"macro_assessment.evidence_support": result.MacroAssessment.EvidenceSupport, "reason": result.Reason,
	} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if err := validHash("review_snapshot_hash", result.ReviewSnapshotHash); err != nil {
		return err
	}
	if !oneOf(result.Decision, "start", "continue", "replan", "stage_complete", "task_complete", "product_decision_required", "external_input_required") {
		return fmt.Errorf("invalid review decision %q", result.Decision)
	}
	if err := uniqueNonempty("preserved_result_ids", result.PreservedResultIDs, false); err != nil {
		return err
	}
	if err := uniqueNonempty("validated_evidence_ids", result.ValidatedEvidenceIDs, false); err != nil {
		return err
	}
	switch result.Decision {
	case "start", "replan", "stage_complete":
		if result.HighestPriorityGap == nil || result.NextWorkPacket == nil || result.ExternalInput != nil {
			return errors.New("work-packet transition decisions require a gap and next work packet, without external input")
		}
		if err := validateProposal(result.NextWorkPacket); err != nil {
			return err
		}
	case "continue":
		if result.HighestPriorityGap == nil || result.NextWorkPacket != nil || result.ExternalInput != nil {
			return errors.New("continue requires a gap and the existing work packet, without a replacement packet or external input")
		}
	case "task_complete":
		if result.HighestPriorityGap != nil || result.NextWorkPacket != nil || result.ExternalInput != nil || len(result.MacroAssessment.Unmet) != 0 || len(result.ValidatedEvidenceIDs) == 0 {
			return errors.New("task_complete requires no gap, next packet, external input or unmet items, and validated evidence")
		}
	case "product_decision_required":
		if result.HighestPriorityGap == nil || result.NextWorkPacket != nil || result.ExternalInput == nil || result.ExternalInput.Kind != "product_decision" {
			return errors.New("product_decision_required has inconsistent external input")
		}
	case "external_input_required":
		if result.HighestPriorityGap == nil || result.NextWorkPacket != nil || result.ExternalInput == nil || !oneOf(result.ExternalInput.Kind, "permission", "credential", "external_state") {
			return errors.New("external_input_required has inconsistent external input")
		}
	}
	if oneOf(result.Decision, "start", "continue") && (!result.PathAssessment.Necessary || !result.PathAssessment.Efficient || !result.PathAssessment.Optimal) {
		return fmt.Errorf("%s requires a necessary, efficient and optimal path", result.Decision)
	}
	if result.Decision == "replan" && (result.PathAssessment.Optimal || len(result.PathAssessment.Problems) == 0 || len(result.PathAssessment.BetterPlan) == 0) {
		return errors.New("replan requires a non-optimal path, problems and a better plan")
	}
	if result.Decision == "stage_complete" && len(result.ValidatedEvidenceIDs) == 0 {
		return errors.New("stage_complete requires validated evidence")
	}
	if result.ExternalInput != nil {
		if err := nonempty("external_input.fact", result.ExternalInput.Fact); err != nil {
			return err
		}
		if err := uniqueNonempty("external_input.exhausted_paths", result.ExternalInput.ExhaustedPaths, true); err != nil {
			return err
		}
		if err := nonempty("external_input.minimum_user_input", result.ExternalInput.MinimumUserInput); err != nil {
			return err
		}
	}
	return nil
}

func validateReviewResultForState(result *ReviewResult, state *State) error {
	if result == nil || state == nil {
		return errors.New("review result and state are required")
	}
	switch result.Decision {
	case "start":
		if state.CurrentWorkPacket != nil {
			return errors.New("start is only valid when no current work packet exists")
		}
	case "continue":
		if state.CurrentWorkPacket == nil {
			return errors.New("continue requires the existing current work packet")
		}
		if state.CurrentWorkPacket.EvidenceCheckpoint.Reached {
			return errors.New("continue cannot approve a work packet whose evidence checkpoint is reached")
		}
	case "stage_complete":
		if state.CurrentWorkPacket == nil {
			return errors.New("stage_complete requires an existing current work packet")
		}
	}
	if state.PendingIntervention != nil && state.PendingIntervention.Status == "awaiting_user" {
		return errors.New("Governor cannot decide before the pending user intervention is resolved")
	}
	return nil
}

func validatePendingIntervention(pending *PendingIntervention) error {
	if pending == nil {
		return errors.New("pending intervention is required")
	}
	for name, value := range map[string]string{
		"intervention_id": pending.InterventionID, "source_review_id": pending.SourceReviewID,
		"kind": pending.Kind, "fact": pending.Fact, "minimum_user_input": pending.MinimumUserInput,
	} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if !oneOf(pending.Kind, "product_decision", "permission", "credential", "external_state") {
		return fmt.Errorf("invalid pending intervention kind %q", pending.Kind)
	}
	if err := uniqueNonempty("pending intervention exhausted_paths", pending.ExhaustedPaths, true); err != nil {
		return err
	}
	if !oneOf(pending.Status, "awaiting_user", "resolution_pending_review") {
		return fmt.Errorf("invalid pending intervention status %q", pending.Status)
	}
	if pending.Status == "awaiting_user" && pending.Resolution != nil {
		return errors.New("awaiting_user intervention cannot contain a resolution")
	}
	if pending.Status == "resolution_pending_review" {
		if pending.Resolution == nil {
			return errors.New("resolution_pending_review intervention requires a resolution")
		}
		if err := validateInterventionResolution(pending.Resolution); err != nil {
			return err
		}
	}
	return nil
}

func validateInterventionResolutionInput(input *ResolveInterventionInput) error {
	if input == nil {
		return errors.New("intervention resolution is required")
	}
	for name, value := range map[string]string{
		"intervention_id": input.InterventionID, "source_turn_id": input.SourceTurnID, "summary": input.Summary,
	} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if len([]rune(strings.TrimSpace(input.Summary))) > 4000 {
		return errors.New("intervention resolution summary exceeds 4000 characters")
	}
	return uniqueNonempty("intervention resolution evidence_refs", input.EvidenceRefs, false)
}

func validateInterventionResolution(resolution *InterventionResolution) error {
	if resolution == nil {
		return errors.New("intervention resolution is required")
	}
	input := ResolveInterventionInput{
		SourceTurnID:   resolution.SourceTurnID,
		Summary:        resolution.Summary,
		EvidenceRefs:   resolution.EvidenceRefs,
		InterventionID: "persisted",
	}
	if err := validateInterventionResolutionInput(&input); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, resolution.SubmittedAt); err != nil {
		return errors.New("intervention resolution submitted_at must be RFC3339")
	}
	return nil
}

func validateProposal(proposal *WorkPacketProposal) error {
	if proposal == nil {
		return errors.New("work packet proposal is required")
	}
	for name, value := range map[string]string{
		"packet_id": proposal.PacketID, "condition_id": proposal.ConditionID, "objective": proposal.Objective,
		"value": proposal.Value, "checkpoint_id": proposal.CheckpointID,
		"checkpoint_description": proposal.CheckpointDescription,
	} {
		if err := nonempty(name, value); err != nil {
			return err
		}
	}
	if err := uniqueNonempty("allowed_scope", proposal.AllowedScope, true); err != nil {
		return err
	}
	if err := uniqueNonempty("excluded_scope", proposal.ExcludedScope, false); err != nil {
		return err
	}
	return uniqueNonempty("expected_evidence", proposal.ExpectedEvidence, true)
}

func validateSchemaDocuments(runtime *Runtime) error {
	for _, configured := range []string{runtime.Config.StateSchemaPath, runtime.Config.ReviewRequestSchemaPath, runtime.Config.ReviewSchemaPath} {
		data, err := os.ReadFile(resolvePath(runtime.Root, configured))
		if err != nil {
			return err
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			return fmt.Errorf("invalid schema %s: %w", configured, err)
		}
		if document["type"] != "object" || document["additionalProperties"] != false {
			return fmt.Errorf("schema %s must be a closed object", configured)
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
