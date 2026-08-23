package governance

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRuntime(t *testing.T) *Runtime {
	t.Helper()
	root, err := findRoot("")
	if err != nil {
		t.Fatal(err)
	}
	var config Config
	configPath := filepath.Join(root, ".codex", "governance", "config.json")
	if err := decodeStrictFile(configPath, &config); err != nil {
		t.Fatal(err)
	}
	return &Runtime{Root: root, ConfigPath: configPath, Config: config, RuntimeDir: t.TempDir()}
}

func hookJSON(t *testing.T, runtime *Runtime, event string, input any) string {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runtime.HandleHook(event, bytes.NewReader(data), &output); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(output.String())
}

func validContinueFeedback(request *ReviewRequest) ReviewResult {
	gap := "next unmet condition"
	return ReviewResult{
		ReviewID: request.ReviewID, TriggerInstanceID: request.TriggerInstanceID, ReviewSnapshotHash: request.ReviewSnapshotHash,
		Recommendation: "continue", HighestPriorityGap: &gap,
		MacroAssessment:    MacroAssessment{OverallProgress: "evidence-backed", EvidenceSupport: "repository and cited evidence", Completed: []string{}, Unmet: []string{gap}},
		PathAssessment:     PathAssessment{Necessary: true, Efficient: true, Optimal: true, Problems: []string{}, BetterPlan: []string{}},
		PreservedResultIDs: []string{}, SuggestedInvalidations: []string{}, ValidatedEvidenceIDs: []string{}, Reason: "current path is the best evidenced route",
	}
}

func respondToCurrentReview(t *testing.T, runtime *Runtime) {
	t.Helper()
	request, err := runtime.loadRequest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AcceptReview(validContinueFeedback(request)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordReviewResponse(ReviewResponseInput{ReviewID: request.ReviewID, Disposition: "acknowledge", Reason: "considered against repository facts", NextValidationPoint: "next evidence boundary"}); err != nil {
		t.Fatal(err)
	}
}

func TestStableActivationAndOrdinaryMessagesAreIsolated(t *testing.T) {
	runtime := testRuntime(t)
	if got := hookJSON(t, runtime, "user-prompt-submit", map[string]any{"session_id": "main", "prompt": "ordinary task"}); got != "{}" || runtime.StateExists() {
		t.Fatalf("ordinary prompt activated governance: %s", got)
	}
	if got := hookJSON(t, runtime, "user-prompt-submit", map[string]any{"session_id": "main", "prompt": "intro\n" + runtime.Config.ActivationMarker}); got != "{}" || runtime.StateExists() {
		t.Fatalf("marker outside the first nonempty line activated governance: %s", got)
	}
	prompt := runtime.Config.ActivationMarker + "\ncontinue the product goal"
	first := hookJSON(t, runtime, "user-prompt-submit", map[string]any{"session_id": "main", "turn_id": "turn-1", "prompt": prompt})
	if !strings.Contains(first, "advisory governance review") {
		t.Fatalf("activation did not deliver an advisory review: %s", first)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	reviewID := valueOr(state.Review.ReviewID, "")
	generation := state.Review.FixedReviewGeneration
	_ = hookJSON(t, runtime, "user-prompt-submit", map[string]any{"session_id": "main", "turn_id": "turn-2", "prompt": prompt})
	state, _ = runtime.LoadState()
	if valueOr(state.Review.ReviewID, "") != reviewID || state.Review.FixedReviewGeneration != generation {
		t.Fatal("Goal Mode replay created duplicate governance work")
	}
	before, _ := os.ReadFile(runtime.statePath())
	if got := hookJSON(t, runtime, "user-prompt-submit", map[string]any{"session_id": "main", "prompt": "do an unrelated task"}); got != "{}" {
		t.Fatalf("ordinary message received governance context: %s", got)
	}
	after, _ := os.ReadFile(runtime.statePath())
	if !bytes.Equal(before, after) {
		t.Fatal("ordinary message changed governance state")
	}
}

func TestGovernorFeedbackIsAdvisoryAndMainResponseIsExplicit(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	condition := mustCondition(t, runtime)
	input := ExecutionSnapshotInput{FocusID: "focus-1", ConditionID: condition, Objective: "verify one root cause", Value: "closes the current gap", InvolvedScope: []string{"internal/component"}, ExpectedEvidence: []string{"evidence-1"}, CheckpointID: "checkpoint-1", CheckpointDescription: "targeted verification passes"}
	request, err := runtime.UpdateExecutionSnapshot(input)
	if err != nil || request == nil {
		t.Fatalf("update snapshot: %v", err)
	}
	suggested := input
	suggested.FocusID = "suggested-focus"
	suggested.Objective = "take a different route"
	gap := "a more valuable route exists"
	feedback := ReviewResult{
		ReviewID: request.ReviewID, TriggerInstanceID: request.TriggerInstanceID, ReviewSnapshotHash: request.ReviewSnapshotHash,
		Recommendation: "adjust", HighestPriorityGap: &gap,
		MacroAssessment:    MacroAssessment{OverallProgress: "partial", EvidenceSupport: "repository facts", Completed: []string{}, Unmet: []string{gap}},
		PathAssessment:     PathAssessment{Necessary: true, Efficient: false, Optimal: false, Problems: []string{"avoidable detour"}, BetterPlan: []string{"use the direct check"}},
		PreservedResultIDs: []string{}, SuggestedInvalidations: []string{"stale assertion"}, ValidatedEvidenceIDs: []string{}, SuggestedFocus: &suggested, Reason: "a direct route exists",
	}
	if _, err := runtime.AcceptReview(feedback); err != nil {
		t.Fatal(err)
	}
	state, _ := runtime.LoadState()
	if state.CurrentFocus == nil || state.CurrentFocus.FocusID != "focus-1" || state.Review.Status != "feedback_ready" {
		t.Fatal("Governor feedback mutated main execution state")
	}
	if _, err := runtime.RecordReviewResponse(ReviewResponseInput{ReviewID: request.ReviewID, Disposition: "decline", Reason: "direct evidence shows the current route remains necessary", NextValidationPoint: "checkpoint-1"}); err != nil {
		t.Fatal(err)
	}
	state, _ = runtime.LoadState()
	if state.CurrentFocus.FocusID != "focus-1" || state.Review.Status != "responded" || state.Review.Response.Disposition != "decline" {
		t.Fatal("main Agent did not retain control while recording its response")
	}
	raw, _ := os.ReadFile(runtime.statePath())
	for _, forbidden := range []string{`"approval"`, `"allowed_scope"`, `"excluded_scope"`, `"current_work_packet"`} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("state retained old control field %s", forbidden)
		}
	}
}

func TestNaturalBoundaryIsIdempotentAndGovernorFailureDoesNotBlock(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ensureHookOwner(HookInput{SessionID: "main"}); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"session_id": "main", "source": "compact", "hook_event_name": "SessionStart"}
	first := hookJSON(t, runtime, "session-start", input)
	if !strings.Contains(first, "additionalContext") || !strings.Contains(first, "advisory governance review") {
		t.Fatalf("compact recovery did not deliver model-visible advisory context: %s", first)
	}
	state, _ := runtime.LoadState()
	reviewID := valueOr(state.Review.ReviewID, "")
	generation := state.Review.FixedReviewGeneration
	_ = hookJSON(t, runtime, "session-start", input)
	state, _ = runtime.LoadState()
	if valueOr(state.Review.ReviewID, "") != reviewID || state.Review.FixedReviewGeneration != generation {
		t.Fatal("same compact boundary created a duplicate review")
	}
	respondToCurrentReview(t, runtime)
	second := hookJSON(t, runtime, "session-start", input)
	state, _ = runtime.LoadState()
	if !strings.Contains(second, "additionalContext") || state.Review.FixedReviewGeneration != generation+1 || valueOr(state.Review.ReviewID, "") == reviewID {
		t.Fatal("a later compact boundary did not create a fresh review")
	}
	if err := runtime.MarkReviewMissed("Governor process unavailable"); err != nil {
		t.Fatal(err)
	}
	state, _ = runtime.LoadState()
	if state.Status != "active" || state.Review.Status != "missed" {
		t.Fatal("Governor failure changed or blocked main execution")
	}
	if got := hookJSON(t, runtime, "pre-tool-use", map[string]any{"session_id": "main", "tool_name": "apply_patch"}); got != "{}" {
		t.Fatalf("legacy pre-tool compatibility denied work: %s", got)
	}
	if got := hookJSON(t, runtime, "stop", map[string]any{"session_id": "main"}); got != "{}" {
		t.Fatalf("legacy Stop compatibility interfered: %s", got)
	}
}

func TestAvailableGovernorFeedbackCannotBeDowngradedToMissed(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	request, err := runtime.RequestFixedReview("activation", "available-feedback")
	if err != nil || request == nil {
		t.Fatalf("request review: %v", err)
	}
	if _, err := runtime.AcceptReview(validContinueFeedback(request)); err != nil {
		t.Fatal(err)
	}
	if err := runtime.MarkReviewMissed("a later review-chain command failed"); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil || state.Review.Status != "feedback_ready" || state.Review.FeedbackPath == nil {
		t.Fatalf("available feedback was discarded instead of awaiting the main Agent response: %v", err)
	}
}

func TestLostGovernorFeedbackCanFailOpenAsMissed(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	request, err := runtime.RequestFixedReview("activation", "lost-feedback")
	if err != nil || request == nil {
		t.Fatalf("request review: %v", err)
	}
	feedbackPath, err := runtime.AcceptReview(validContinueFeedback(request))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(feedbackPath); err != nil {
		t.Fatal(err)
	}
	if err := runtime.MarkReviewMissed("stored Governor feedback became unavailable"); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil || state.Review.Status != "missed" {
		t.Fatalf("unavailable feedback did not fail open: %v", err)
	}
}

func TestLaterResumeCreatesANewReviewAfterThePreviousOneIsAnswered(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ensureHookOwner(HookInput{SessionID: "main"}); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"session_id": "main", "source": "resume", "hook_event_name": "SessionStart"}
	_ = hookJSON(t, runtime, "session-start", input)
	state, _ := runtime.LoadState()
	firstReviewID := valueOr(state.Review.ReviewID, "")
	firstGeneration := state.Review.FixedReviewGeneration
	respondToCurrentReview(t, runtime)
	output := hookJSON(t, runtime, "session-start", input)
	state, _ = runtime.LoadState()
	if !strings.Contains(output, "additionalContext") || state.Review.FixedReviewGeneration != firstGeneration+1 || valueOr(state.Review.ReviewID, "") == firstReviewID {
		t.Fatal("a later real resume was incorrectly deduplicated against historical review state")
	}
}

func TestPostToolFailureStoresOnlyAHashedSignature(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ensureHookOwner(HookInput{SessionID: "main"}); err != nil {
		t.Fatal(err)
	}
	condition := mustCondition(t, runtime)
	if _, err := runtime.UpdateExecutionSnapshot(ExecutionSnapshotInput{FocusID: "focus-safe-failure", ConditionID: condition, Objective: "verify failure handling", Value: "protects audit safety", InvolvedScope: []string{"internal/component"}, ExpectedEvidence: []string{"evidence-1"}, CheckpointID: "checkpoint-1", CheckpointDescription: "failure handling passes"}); err != nil {
		t.Fatal(err)
	}
	secret := "super-secret-token"
	_ = hookJSON(t, runtime, "post-tool-use", map[string]any{
		"session_id": "main", "turn_id": "turn-failure", "tool_name": "exec_command", "tool_use_id": "tool-failure",
		"tool_response": map[string]any{"isError": true, "error": secret},
	})
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.CurrentFocus.FailureEvents) != 1 || strings.Contains(state.CurrentFocus.FailureEvents[0].Signature, secret) || !strings.Contains(state.CurrentFocus.FailureEvents[0].Signature, "sha256:") {
		t.Fatal("failed tool output was not reduced to a safe hash signature")
	}
	raw, _ := os.ReadFile(runtime.eventsPath())
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("failed tool output leaked into the governance event log")
	}
}

func TestPostToolFailureClassificationIgnoresVolatileMetadata(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ensureHookOwner(HookInput{SessionID: "main"}); err != nil {
		t.Fatal(err)
	}
	condition := mustCondition(t, runtime)
	if _, err := runtime.UpdateExecutionSnapshot(ExecutionSnapshotInput{FocusID: "focus-repeat", ConditionID: condition, Objective: "verify repeated failure classification", Value: "triggers macro review after a failed repair", InvolvedScope: []string{"internal/component"}, ExpectedEvidence: []string{"evidence-1"}, CheckpointID: "checkpoint-1", CheckpointDescription: "repeated failure is recognized"}); err != nil {
		t.Fatal(err)
	}
	respondToCurrentReview(t, runtime)
	firstResponse := json.RawMessage(`{"isError":true,"call_id":"call-11111111-1111-1111-1111-111111111111","timestamp":"2026-08-23T10:11:12Z","wall_time_seconds":1.25,"error":"connection reset after 1250 ms"}`)
	secondResponse := json.RawMessage(`{"isError":true,"call_id":"call-22222222-2222-2222-2222-222222222222","timestamp":"2026-08-23T10:12:55Z","wall_time_seconds":4.75,"error":"connection reset after 4750 ms"}`)
	signature := failureFromResponse("exec_command", firstResponse)
	if signature != failureFromResponse("exec_command", secondResponse) {
		t.Fatal("volatile failure metadata changed the stable failure class")
	}
	_ = hookJSON(t, runtime, "post-tool-use", HookInput{SessionID: "main", TurnID: "turn-1", ToolName: "exec_command", ToolUseID: "tool-1", ToolResponse: firstResponse})
	state, err := runtime.LoadState()
	if err != nil || len(state.CurrentFocus.FailureEvents) != 1 {
		t.Fatalf("first failure was not recorded: %v", err)
	}
	previous := state.CurrentFocus.FailureEvents[0]
	state.CurrentFocus.FailureRepairs = append(state.CurrentFocus.FailureRepairs, FailureRepair{RepairID: "repair-stable-class", Signature: signature, PreviousEventID: previous.EventID, FocusID: state.CurrentFocus.FocusID, RepairGeneration: 1, RepositoryIdentity: previous.RepositoryIdentity, CandidateIdentity: previous.CandidateIdentity, ConfigIdentity: previous.ConfigIdentity, RuntimeIdentity: previous.RuntimeIdentity, EvidenceIDs: []string{"evidence-1"}, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err := runtime.saveState(state); err != nil {
		t.Fatal(err)
	}
	output := hookJSON(t, runtime, "post-tool-use", HookInput{SessionID: "main", TurnID: "turn-2", ToolName: "exec_command", ToolUseID: "tool-2", ToolResponse: secondResponse})
	state, err = runtime.LoadState()
	if err != nil || state.Review.Status != "requested" || !strings.Contains(output, "additionalContext") {
		t.Fatalf("same failure class after repair did not trigger advisory review: %v", err)
	}
}

func TestReusableEvidenceBindsToANewExecutionSnapshot(t *testing.T) {
	runtime := testRuntime(t)
	runtime.Config.EvidenceRoots = []string{runtime.RuntimeDir}
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	condition := mustCondition(t, runtime)
	first := ExecutionSnapshotInput{FocusID: "focus-first", ConditionID: condition, Objective: "produce reusable evidence", Value: "establishes a reusable fact", InvolvedScope: []string{"internal/component"}, ExpectedEvidence: []string{"evidence-reusable"}, CheckpointID: "checkpoint-first", CheckpointDescription: "evidence exists"}
	if _, err := runtime.UpdateExecutionSnapshot(first); err != nil {
		t.Fatal(err)
	}
	respondToCurrentReview(t, runtime)
	evidencePath := filepath.Join(runtime.RuntimeDir, "reusable.json")
	if err := os.WriteFile(evidencePath, []byte(`{"status":"passed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordEvidence(EvidenceRecord{EvidenceID: "evidence-reusable", Scope: []string{"condition"}, Path: evidencePath, ValidatorStatus: "passed", ValidatorSource: "unit-test"}); err != nil {
		t.Fatal(err)
	}
	respondToCurrentReview(t, runtime)
	if err := runtime.CompleteExecutionSnapshot(); err != nil {
		t.Fatal(err)
	}
	second := first
	second.FocusID = "focus-second"
	second.CheckpointID = "checkpoint-second"
	if _, err := runtime.UpdateExecutionSnapshot(second); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil || state.CurrentFocus == nil || !state.CurrentFocus.EvidenceCheckpoint.Reached {
		t.Fatalf("unchanged reusable evidence did not satisfy the new snapshot: %v", err)
	}
	if evidence := conditionEvidence(state, condition); len(evidence) != 1 || evidence[0] != "evidence-reusable" {
		t.Fatal("reused evidence was not bound to the new condition state")
	}
}

func TestNewEvidenceSupersedesAStaleReviewAndCheckpointWaitsForAResponse(t *testing.T) {
	runtime := testRuntime(t)
	runtime.Config.EvidenceRoots = []string{runtime.RuntimeDir}
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	condition := mustCondition(t, runtime)
	firstRequest, err := runtime.UpdateExecutionSnapshot(ExecutionSnapshotInput{FocusID: "focus-evidence-snapshot", ConditionID: condition, Objective: "bind evidence to the review that can see it", Value: "prevents stale Governor claims", InvolvedScope: []string{"internal/component"}, ExpectedEvidence: []string{"evidence-snapshot"}, CheckpointID: "checkpoint-evidence-snapshot", CheckpointDescription: "evidence is visible to the review"})
	if err != nil || firstRequest == nil {
		t.Fatalf("create first review: %v", err)
	}
	evidencePath := filepath.Join(runtime.RuntimeDir, "snapshot-evidence.json")
	if err := os.WriteFile(evidencePath, []byte(`{"status":"passed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpointRequest, err := runtime.RecordEvidence(EvidenceRecord{EvidenceID: "evidence-snapshot", Scope: []string{"condition"}, Path: evidencePath, ValidatorStatus: "passed", ValidatorSource: "unit-test"})
	if err != nil || checkpointRequest == nil || checkpointRequest.ReviewID == firstRequest.ReviewID {
		t.Fatalf("new evidence did not replace the stale review: %v", err)
	}
	if len(checkpointRequest.EvidenceRefs) != 1 || checkpointRequest.EvidenceRefs[0].EvidenceID != "evidence-snapshot" || checkpointRequest.RecentCheckpoint == nil {
		t.Fatal("replacement review was not bound to the new evidence checkpoint")
	}
	staleFeedback := validContinueFeedback(firstRequest)
	staleFeedback.ValidatedEvidenceIDs = []string{"evidence-snapshot"}
	if _, err := runtime.AcceptReview(staleFeedback); err == nil {
		t.Fatal("stale feedback validated evidence that was absent from its request")
	}
	feedback := validContinueFeedback(checkpointRequest)
	feedback.ValidatedEvidenceIDs = []string{"evidence-snapshot"}
	if _, err := runtime.AcceptReview(feedback); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CompleteExecutionSnapshot(); err == nil {
		t.Fatal("execution snapshot closed before the main Agent answered available feedback")
	}
	if _, err := runtime.RecordReviewResponse(ReviewResponseInput{ReviewID: checkpointRequest.ReviewID, Disposition: "acknowledge", Reason: "the evidence is current", NextValidationPoint: "close the checkpoint"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CompleteExecutionSnapshot(); err != nil {
		t.Fatal(err)
	}
}

func TestMissingEvidenceAtCheckpointCreatesAdvisoryReview(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	condition := mustCondition(t, runtime)
	if _, err := runtime.UpdateExecutionSnapshot(ExecutionSnapshotInput{FocusID: "focus-missing", ConditionID: condition, Objective: "reach the validation point", Value: "tests missing evidence handling", InvolvedScope: []string{"internal/component"}, ExpectedEvidence: []string{"evidence-missing"}, CheckpointID: "checkpoint-missing", CheckpointDescription: "expected evidence should exist"}); err != nil {
		t.Fatal(err)
	}
	respondToCurrentReview(t, runtime)
	if err := runtime.CompleteExecutionSnapshot(); err == nil {
		t.Fatal("missing evidence incorrectly completed the execution snapshot")
	}
	state, err := runtime.LoadState()
	if err != nil || state.CurrentFocus == nil || state.Review.Status != "requested" || state.Review.Trigger == nil || !strings.Contains(*state.Review.Trigger, "evidence-checkpoint-missed") {
		t.Fatalf("missing checkpoint evidence did not persist an advisory review: err=%v status=%s trigger=%v", err, state.Review.Status, state.Review.Trigger)
	}
}

func TestChangedEvidenceInvalidatesCompletionAndRejectsStaleFinish(t *testing.T) {
	runtime := testRuntime(t)
	runtime.Config.EvidenceRoots = []string{runtime.RuntimeDir}
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	condition := mustCondition(t, runtime)
	if _, err := runtime.UpdateExecutionSnapshot(ExecutionSnapshotInput{FocusID: "focus-completion", ConditionID: condition, Objective: "establish completion evidence", Value: "binds completion to immutable evidence", InvolvedScope: []string{"internal/component"}, ExpectedEvidence: []string{"evidence-completion"}, CheckpointID: "checkpoint-completion", CheckpointDescription: "completion evidence passes"}); err != nil {
		t.Fatal(err)
	}
	respondToCurrentReview(t, runtime)
	evidencePath := filepath.Join(runtime.RuntimeDir, "completion.json")
	if err := os.WriteFile(evidencePath, []byte(`{"status":"passed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordEvidence(EvidenceRecord{EvidenceID: "evidence-completion", Scope: []string{"completion"}, Path: evidencePath, ValidatorStatus: "passed", ValidatorSource: "unit-test"}); err != nil {
		t.Fatal(err)
	}
	respondToCurrentReview(t, runtime)
	if err := runtime.CompleteExecutionSnapshot(); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	for index := range state.CompletionConditions {
		state.CompletionConditions[index].Status = "met"
		state.CompletionConditions[index].EvidenceIDs = []string{"evidence-completion"}
	}
	if err := runtime.saveState(state); err != nil {
		t.Fatal(err)
	}
	request, err := runtime.RequestCompletionReview("candidate-1")
	if err != nil || request == nil {
		t.Fatalf("completion review: %v", err)
	}
	feedback := validContinueFeedback(request)
	feedback.Recommendation = "goal_complete"
	feedback.HighestPriorityGap = nil
	feedback.MacroAssessment.Unmet = []string{}
	feedback.MacroAssessment.Completed = []string{"all completion conditions"}
	feedback.ValidatedEvidenceIDs = []string{"evidence-completion"}
	if _, err := runtime.AcceptReview(feedback); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordReviewResponse(ReviewResponseInput{ReviewID: request.ReviewID, Disposition: "acknowledge", Reason: "completion evidence reviewed", NextValidationPoint: "finish"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, []byte(`{"status":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Finish(); err == nil {
		t.Fatal("changed evidence was allowed to close the task")
	}
	state, err = runtime.LoadState()
	if err != nil || state.Status != "active" || state.Review.Status != "requested" || len(state.ReusableResults) != 0 {
		t.Fatalf("changed evidence did not reopen completion and request review: err=%v status=%s review=%s reusable=%d", err, state.Status, state.Review.Status, len(state.ReusableResults))
	}
	for _, item := range state.CompletionConditions {
		if item.Status == "met" || len(item.EvidenceIDs) != 0 {
			t.Fatal("completion condition retained invalid evidence")
		}
	}
}

func TestGovernorRecommendationCannotGateMechanicallyValidFinish(t *testing.T) {
	runtime := testRuntime(t)
	runtime.Config.EvidenceRoots = []string{runtime.RuntimeDir}
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	condition := mustCondition(t, runtime)
	if _, err := runtime.UpdateExecutionSnapshot(ExecutionSnapshotInput{FocusID: "focus-advisory-finish", ConditionID: condition, Objective: "establish completion facts", Value: "proves the main Agent retains completion control", InvolvedScope: []string{"internal/component"}, ExpectedEvidence: []string{"evidence-advisory-finish"}, CheckpointID: "checkpoint-advisory-finish", CheckpointDescription: "completion facts pass"}); err != nil {
		t.Fatal(err)
	}
	respondToCurrentReview(t, runtime)
	evidencePath := filepath.Join(runtime.RuntimeDir, "advisory-finish.json")
	if err := os.WriteFile(evidencePath, []byte(`{"status":"passed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordEvidence(EvidenceRecord{EvidenceID: "evidence-advisory-finish", Scope: []string{"completion"}, Path: evidencePath, ValidatorStatus: "passed", ValidatorSource: "unit-test"}); err != nil {
		t.Fatal(err)
	}
	respondToCurrentReview(t, runtime)
	if err := runtime.CompleteExecutionSnapshot(); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	for index := range state.CompletionConditions {
		state.CompletionConditions[index].Status = "met"
		state.CompletionConditions[index].EvidenceIDs = []string{"evidence-advisory-finish"}
	}
	if err := runtime.saveState(state); err != nil {
		t.Fatal(err)
	}
	request, err := runtime.RequestCompletionReview("candidate-advisory-finish")
	if err != nil || request == nil {
		t.Fatalf("completion review: %v", err)
	}
	feedback := validContinueFeedback(request)
	feedback.ValidatedEvidenceIDs = []string{"evidence-advisory-finish"}
	if _, err := runtime.AcceptReview(feedback); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordReviewResponse(ReviewResponseInput{ReviewID: request.ReviewID, Disposition: "decline", Reason: "mechanical completion evidence proves that no further work is required", NextValidationPoint: "finish mechanical verification"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Finish(); err != nil {
		t.Fatalf("Governor advice became a completion gate: %v", err)
	}
}

func TestUnavailableCompletionReviewDoesNotBlockMechanicallyValidFinish(t *testing.T) {
	runtime := testRuntime(t)
	runtime.Config.EvidenceRoots = []string{runtime.RuntimeDir}
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(runtime.RuntimeDir, "missed-finish.json")
	if err := os.WriteFile(evidencePath, []byte(`{"status":"passed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	evidenceHash, err := fileHash(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	state.ReusableResults = []ReusableResult{{ResultID: "evidence-missed-finish", Scope: []string{"completion"}, EvidencePath: evidencePath, InputHash: evidenceHash}}
	for index := range state.CompletionConditions {
		state.CompletionConditions[index].Status = "met"
		state.CompletionConditions[index].EvidenceIDs = []string{"evidence-missed-finish"}
	}
	if err := runtime.saveState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RequestCompletionReview("candidate-missed-finish"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.MarkReviewMissed("Governor unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Finish(); err != nil {
		t.Fatalf("unavailable Governor blocked mechanical completion: %v", err)
	}
}

func TestPrematureCheckpointDescriptionIsRepairedAsNextAction(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	condition := mustCondition(t, runtime)
	input := ExecutionSnapshotInput{FocusID: "focus-next-action", ConditionID: condition, Objective: "run the targeted verification", Value: "closes the current gap", InvolvedScope: []string{"internal/component"}, ExpectedEvidence: []string{"evidence-1"}, CheckpointID: "checkpoint-1", CheckpointDescription: "targeted verification has passed"}
	if _, err := runtime.UpdateExecutionSnapshot(input); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if valueOr(state.NextAction, "") != input.Objective {
		t.Fatal("new execution snapshot did not preserve the active objective as next action")
	}
	state.NextAction = stringPointer(input.CheckpointDescription)
	if err := runtime.saveState(state); err != nil {
		t.Fatal(err)
	}
	if err := runtime.migrateLegacyStateIfNeeded(); err != nil {
		t.Fatal(err)
	}
	state, err = runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if valueOr(state.NextAction, "") != input.Objective {
		t.Fatal("existing v2 state retained a premature checkpoint outcome as next action")
	}
}

func TestSessionStartIgnoresNonGovernanceSources(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ensureHookOwner(HookInput{SessionID: "main"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(runtime.statePath())
	if err != nil {
		t.Fatal(err)
	}
	if got := hookJSON(t, runtime, "session-start", map[string]any{"session_id": "main", "source": "fork", "hook_event_name": "SessionStart"}); got != "{}" {
		t.Fatalf("non-governance SessionStart source injected context: %s", got)
	}
	after, err := os.ReadFile(runtime.statePath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("non-governance SessionStart source changed governance state")
	}
}

func TestExecCommandGovernanceFailureIsRecognized(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ensureHookOwner(HookInput{SessionID: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RequestFixedReview("activation", "exec-command-failure"); err != nil {
		t.Fatal(err)
	}
	input := HookInput{
		SessionID: "main", ToolName: "exec_command", ToolUseID: "failed-accept-review",
		ToolInput:    json.RawMessage(`{"cmd":".codex/governance/governance-hook.ps1 accept-review --json-base64 bad"}`),
		ToolResponse: json.RawMessage(`{"isError":true,"error":"failed to persist feedback"}`),
	}
	if !isGovernorReviewChainAttempt(runtime, input) {
		t.Fatal("exec_command governance persistence failure was not recognized")
	}
	_ = hookJSON(t, runtime, "post-tool-use", input)
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Review.Status != "missed" || state.Status != "active" {
		t.Fatal("exec_command governance persistence failure did not fail open")
	}
}

func TestHandoffTransfersReviewOwnershipWithoutFreezingWork(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ensureHookOwner(HookInput{SessionID: "main-a"}); err != nil {
		t.Fatal(err)
	}
	ticket, err := runtime.PrepareHandoff("main-a")
	if err != nil {
		t.Fatal(err)
	}
	state, _ := runtime.LoadState()
	if state.Status != "active" {
		t.Fatal("preparing handoff froze the task")
	}
	if err := runtime.BindHandoff(ticket.HandoffID, "main-b"); err != nil {
		t.Fatal(err)
	}
	prompt := "[ownward-governance-handoff id=" + ticket.HandoffID + " token=" + ticket.Token + "]"
	_ = hookJSON(t, runtime, "user-prompt-submit", map[string]any{"session_id": "main-b", "prompt": prompt})
	state, _ = runtime.LoadState()
	if state.Owner == nil || state.Owner.SessionID != "main-b" || state.Status != "active" {
		t.Fatal("handoff did not transfer advisory ownership cleanly")
	}
}

func TestLegacyStateMigrationPreservesProgressAndRemovesActiveControl(t *testing.T) {
	runtime := testRuntime(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	legacy := map[string]any{
		"schema_version": 1, "run_id": "legacy-run", "status": "running", "authority_hash": "sha256:" + strings.Repeat("a", 64),
		"completion_conditions": []map[string]any{{"condition_id": "condition:1:test", "status": "in_progress", "evidence_ids": []string{"result-1"}}},
		"current_work_packet": map[string]any{
			"packet_id": "packet-1", "condition_id": "condition:1:test", "objective": "preserve objective", "value": "preserve value",
			"allowed_scope": []string{"src"}, "excluded_scope": []string{"do not touch docs"}, "expected_evidence": []string{"result-1"},
			"evidence_checkpoint": map[string]any{"checkpoint_id": "cp-1", "description": "result exists", "reached": true},
			"plan_hash":           "sha256:" + strings.Repeat("b", 64), "approval": map[string]any{"status": "approved"},
			"started_at": now, "last_evidence_at": now, "checkpoint": "cp-1", "failure_signatures": []string{"legacy-timeout"}, "failure_events": []any{}, "failure_repairs": []any{},
		},
		"pending_intervention": nil, "explicit_resource_constraints": []any{},
		"reusable_results": []map[string]any{{"result_id": "result-1", "scope": []string{"test"}, "evidence_path": "evidence.json", "input_hash": "sha256:" + strings.Repeat("c", 64)}},
		"next_action":      "wait for approval", "review": map[string]any{"required": true, "fixed_review_generation": 7},
		"owner": map[string]any{"session_id": "main", "transcript_path": "history", "owner_epoch": 1, "acquired_at": now}, "handoff": nil, "infrastructure_failure": nil,
	}
	if err := atomicWriteJSON(runtime.statePath(), legacy); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteJSON(runtime.requestPath(), map[string]any{"schema_version": 1, "review_id": "old"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.migrateLegacyStateIfNeeded(); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentFocus == nil || state.CurrentFocus.FocusID != "packet-1" || state.CurrentFocus.Objective != "preserve objective" || len(state.ReusableResults) != 1 || state.Owner == nil {
		t.Fatal("migration lost valid progress, evidence, or ownership")
	}
	if len(state.CurrentFocus.InvolvedScope) != 2 || !strings.HasPrefix(state.CurrentFocus.InvolvedScope[0], "原执行范围说明（仅作上下文）：") || !strings.HasPrefix(state.CurrentFocus.InvolvedScope[1], "原边界说明（仅作上下文）：") {
		t.Fatal("migration failed to preserve legacy scope meaning as non-controlling context")
	}
	if state.Review.Status != "idle" || state.Review.FixedReviewGeneration != 7 {
		t.Fatal("legacy control review remained active or generation was lost")
	}
	if len(state.CurrentFocus.FailureEvents) != 1 || state.CurrentFocus.FailureEvents[0].Signature != "legacy-timeout" || state.CurrentFocus.FailureEvents[0].Trust != "legacy_unverified" {
		t.Fatal("migration lost the legacy failure fact or made it count as verified")
	}
	if _, err := os.Stat(runtime.requestPath()); !os.IsNotExist(err) {
		t.Fatal("legacy active request remained live")
	}
	if _, err := os.Stat(filepath.Join(runtime.RuntimeDir, "migrations", "advisory-v2", "review-request.v1.json")); err != nil {
		t.Fatal("legacy request was not archived for audit")
	}
}

func TestAdvisoryV2MigrationFinalizationIsReentrant(t *testing.T) {
	runtime := testRuntime(t)
	state, err := runtime.Init()
	if err != nil {
		t.Fatal(err)
	}
	archiveDir := filepath.Join(runtime.RuntimeDir, "migrations", "advisory-v2")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveDir, "state.v1.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.migrateLegacyStateIfNeeded(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, "migration.json")); err != nil {
		t.Fatal("an interrupted migration did not write its completion marker")
	}
	exists, err := runtime.eventKindExists("advisory_v2_migrated", state.RunID)
	if err != nil || !exists {
		t.Fatal("an interrupted migration did not restore its audit event")
	}
	if err := runtime.migrateLegacyStateIfNeeded(); err != nil {
		t.Fatal(err)
	}
}

func TestDelegatedTaskOwnerIsPreservedWithoutLosingState(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(runtime.RuntimeDir, "subagent.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"session_meta","payload":{"thread_source":"subagent"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	state.Owner = &OwnerState{SessionID: "delegated-main", TranscriptPath: transcript, OwnerEpoch: 3, AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := runtime.saveState(state); err != nil {
		t.Fatal(err)
	}
	if err := runtime.migrateLegacyStateIfNeeded(); err != nil {
		t.Fatal(err)
	}
	state, err = runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Owner == nil || state.Owner.SessionID != "delegated-main" || state.RunID == "" || len(state.CompletionConditions) == 0 {
		t.Fatal("a delegated main task owner or valid governance state was lost")
	}
}

func TestStrictJSONRejectsUnknownFields(t *testing.T) {
	var input ReviewResponseInput
	if err := decodeStrict(strings.NewReader(`{"review_id":"r","disposition":"acknowledge","reason":"x","next_validation_point":"y","unknown":true}`), &input); err == nil {
		t.Fatal("strict JSON accepted an unknown field")
	}
}

func TestDoctor(t *testing.T) {
	runtime := testRuntime(t)
	report, err := runtime.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "passed" || len(report.Checks) < 4 {
		t.Fatalf("unexpected doctor report: %#v", report)
	}
}

func mustCondition(t *testing.T, runtime *Runtime) string {
	t.Helper()
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.CompletionConditions) == 0 {
		t.Fatal("fixture has no completion conditions")
	}
	return state.CompletionConditions[0].ConditionID
}
