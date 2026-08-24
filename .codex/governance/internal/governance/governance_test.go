package governance

import (
	"bytes"
	"encoding/json"
	"errors"
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
		PreservedResultIDs: []string{}, SuggestedInvalidations: []string{}, ValidatedEvidenceIDs: []string{}, AuthorityClaims: []AuthorityClaim{}, Assumptions: []ReviewAssumption{}, Reason: "current path is the best evidenced route",
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

func TestGoalModeReplayRemainsIdempotentAfterOtherReviews(t *testing.T) {
	runtime := testRuntime(t)
	prompt := runtime.Config.ActivationMarker + "\ncontinue the product goal"
	_ = hookJSON(t, runtime, "user-prompt-submit", map[string]any{"session_id": "main", "turn_id": "activation-1", "prompt": prompt})
	respondToCurrentReview(t, runtime)
	request, err := runtime.RequestFixedReview("session-start", "explicit-check")
	if err != nil || request == nil {
		t.Fatalf("explicit review: %v", err)
	}
	respondToCurrentReview(t, runtime)
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	reviewID := valueOr(state.Review.ReviewID, "")
	generation := state.Review.FixedReviewGeneration
	if got := hookJSON(t, runtime, "user-prompt-submit", map[string]any{"session_id": "main", "turn_id": "activation-replay", "prompt": prompt}); got != "{}" {
		t.Fatalf("a consumed Goal Mode activation emitted governance work again: %s", got)
	}
	state, err = runtime.LoadState()
	if err != nil || valueOr(state.Review.ReviewID, "") != reviewID || state.Review.FixedReviewGeneration != generation {
		t.Fatal("a consumed Goal Mode activation changed the current review")
	}
}

func TestExistingStateLearnsMissingActivationIdentityWithoutExtraReview(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ensureHookOwner(HookInput{SessionID: "main"}); err != nil {
		t.Fatal(err)
	}
	before, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	prompt := runtime.Config.ActivationMarker + "\ncontinue the existing product goal"
	if got := hookJSON(t, runtime, "user-prompt-submit", map[string]any{"session_id": "main", "turn_id": "activation-after-migration", "prompt": prompt}); got != "{}" {
		t.Fatalf("an existing run with an unknown legacy activation emitted a redundant review: %s", got)
	}
	after, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if after.ActivationSourceID == nil || *after.ActivationSourceID != activationSourceID(prompt) || after.Review.FixedReviewGeneration != before.Review.FixedReviewGeneration || after.Review.ReviewID != nil {
		t.Fatal("existing governance state did not learn the activation identity without changing review state")
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
	if err != nil || request != nil {
		t.Fatalf("update snapshot: %v", err)
	}
	request, err = runtime.RequestFixedReview("activation", "advisory-control-test")
	if err != nil || request == nil {
		t.Fatalf("request review: %v", err)
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
	if !strings.Contains(first, "additionalContext") || !strings.Contains(first, "advisory governance review") || !strings.Contains(first, `agent_type=\"governor\"`) || !strings.Contains(first, `fork_turns=\"none\"`) {
		t.Fatalf("compact recovery did not deliver model-visible advisory context: %s", first)
	}
	state, _ := runtime.LoadState()
	reviewID := valueOr(state.Review.ReviewID, "")
	generation := state.Review.FixedReviewGeneration
	replayed := hookJSON(t, runtime, "session-start", input)
	state, _ = runtime.LoadState()
	if !strings.Contains(replayed, "additionalContext") || valueOr(state.Review.ReviewID, "") != reviewID || state.Review.FixedReviewGeneration != generation {
		t.Fatal("same compact boundary did not restore the existing review idempotently")
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
	if err != nil || state.Review.Status != "feedback_ready" {
		t.Fatalf("available feedback was discarded instead of awaiting the main Agent response: %v", err)
	}
	if _, err := os.Stat(runtime.reviewPath()); err != nil {
		t.Fatal("available feedback was not preserved in the current review file")
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

func TestReviewLifecycleUsesOnlyFixedCurrentFiles(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	first, err := runtime.RequestFixedReview("activation", "current-file-1")
	if err != nil || first == nil {
		t.Fatalf("first review: %v", err)
	}
	path, err := runtime.AcceptReview(validContinueFeedback(first))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(path) != filepath.Clean(runtime.reviewPath()) {
		t.Fatalf("feedback was not stored in the fixed current file: %s", path)
	}
	if _, err := runtime.RecordReviewResponse(ReviewResponseInput{ReviewID: first.ReviewID, Disposition: "acknowledge", Reason: "first review considered", NextValidationPoint: "next boundary"}); err != nil {
		t.Fatal(err)
	}
	second, err := runtime.RequestFixedReview("session-start", "current-file-2")
	if err != nil || second == nil {
		t.Fatalf("second review: %v", err)
	}
	if _, err := os.Stat(runtime.reviewPath()); !os.IsNotExist(err) {
		t.Fatal("a new request did not invalidate the previous current feedback file")
	}
	if _, err := runtime.AcceptReview(validContinueFeedback(second)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runtime.RuntimeDir, "reviews")); !os.IsNotExist(err) {
		t.Fatal("review lifecycle created a historical review directory")
	}
	if _, err := os.Stat(filepath.Join(runtime.RuntimeDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatal("review lifecycle created an append-only event stream")
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

func TestGenericPostToolPayloadIsIgnoredBecauseFailureIsAmbiguous(t *testing.T) {
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
	before, _ := os.ReadFile(runtime.statePath())
	if got := hookJSON(t, runtime, "post-tool-use", map[string]any{"session_id": "main", "turn_id": "turn-failure", "tool_name": "Bash", "tool_use_id": "tool-failure", "tool_response": ""}); got != "{}" {
		t.Fatalf("ambiguous generic PostToolUse payload produced governance output: %s", got)
	}
	after, _ := os.ReadFile(runtime.statePath())
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || len(state.CurrentFocus.FailureEvents) != 0 {
		t.Fatal("ambiguous generic PostToolUse payload changed governance state")
	}
	if _, err := os.Stat(filepath.Join(runtime.RuntimeDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatal("failure handling created a legacy governance event log")
	}
}

func TestHookDiagnosticStoresOnlyASafeHash(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	secret := "private-error-detail"
	runtime.recordHookDiagnostic("user-prompt-submit", errors.New(secret))
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.LastDiagnostic == nil || !strings.HasPrefix(state.LastDiagnostic.Summary, "sha256:") || strings.Contains(state.LastDiagnostic.Summary, secret) {
		t.Fatal("hook diagnostic did not reduce the error to a safe hash")
	}
}

func TestStructuredCheckpointFailureClassifiesRepeatedFailure(t *testing.T) {
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
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	firstRequest, err := runtime.RecordCheckpointResult(CheckpointResultInput{FocusID: state.CurrentFocus.FocusID, CheckpointID: "checkpoint-1", Outcome: "failed", FailureCategory: "connection-reset", SourceExecution: "probe-1", ResultHash: "sha256:" + strings.Repeat("a", 64), EvidenceIDs: []string{}})
	if err != nil || firstRequest == nil {
		t.Fatalf("first checkpoint failure: %v", err)
	}
	respondToCurrentReview(t, runtime)
	state, err = runtime.LoadState()
	if err != nil || len(state.CurrentFocus.FailureEvents) != 1 {
		t.Fatalf("first failure was not recorded: %v", err)
	}
	previous := state.CurrentFocus.FailureEvents[0]
	state.CurrentFocus.FailureRepairs = append(state.CurrentFocus.FailureRepairs, FailureRepair{RepairID: "repair-stable-class", Signature: "connection-reset", PreviousEventID: previous.EventID, FocusID: state.CurrentFocus.FocusID, RepairGeneration: 1, RepositoryIdentity: previous.RepositoryIdentity, CandidateIdentity: previous.CandidateIdentity, ConfigIdentity: previous.ConfigIdentity, RuntimeIdentity: previous.RuntimeIdentity, EvidenceIDs: []string{}, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err := runtime.saveState(state); err != nil {
		t.Fatal(err)
	}
	secondRequest, err := runtime.RecordCheckpointResult(CheckpointResultInput{FocusID: state.CurrentFocus.FocusID, CheckpointID: "checkpoint-1", Outcome: "failed", FailureCategory: "connection-reset", SourceExecution: "probe-2", ResultHash: "sha256:" + strings.Repeat("b", 64), EvidenceIDs: []string{}})
	state, err = runtime.LoadState()
	if err != nil || secondRequest == nil || state.Review.Status != "requested" || state.Review.Trigger == nil || !strings.Contains(*state.Review.Trigger, "repeated-failure") {
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
	evidencePath := filepath.Join(runtime.RuntimeDir, "reusable.json")
	if err := os.WriteFile(evidencePath, []byte(`{"status":"passed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordEvidence(EvidenceRecord{EvidenceID: "evidence-reusable", Scope: []string{"condition"}, Path: evidencePath, ValidatorStatus: "passed", ValidatorSource: "unit-test"}); err != nil {
		t.Fatal(err)
	}
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
	if request, err := runtime.UpdateExecutionSnapshot(ExecutionSnapshotInput{FocusID: "focus-evidence-snapshot", ConditionID: condition, Objective: "bind evidence to the review that can see it", Value: "prevents stale Governor claims", InvolvedScope: []string{"internal/component"}, ExpectedEvidence: []string{"evidence-snapshot"}, CheckpointID: "checkpoint-evidence-snapshot", CheckpointDescription: "evidence is visible to the review"}); err != nil || request != nil {
		t.Fatalf("execution focus incorrectly created a review: %v", err)
	}
	firstRequest, err := runtime.RequestFixedReview("activation", "stale-feedback")
	if err != nil || firstRequest == nil {
		t.Fatalf("create first review: %v", err)
	}
	evidencePath := filepath.Join(runtime.RuntimeDir, "snapshot-evidence.json")
	if err := os.WriteFile(evidencePath, []byte(`{"status":"passed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpointRequest, err := runtime.RecordEvidence(EvidenceRecord{EvidenceID: "evidence-snapshot", Scope: []string{"condition"}, Path: evidencePath, ValidatorStatus: "passed", ValidatorSource: "unit-test"})
	if err != nil || checkpointRequest != nil {
		t.Fatalf("successful evidence incorrectly created a review: %v", err)
	}
	state, err := runtime.LoadState()
	if err != nil || state.Review.Pending != nil {
		t.Fatal("successful evidence incorrectly created a pending review")
	}
	staleFeedback := validContinueFeedback(firstRequest)
	if _, err := runtime.AcceptReview(staleFeedback); err != nil {
		t.Fatal(err)
	}
	state, _ = runtime.LoadState()
	if state.Review.Status != "superseded" {
		t.Fatal("complete but stale feedback was not preserved as superseded")
	}
	if err := runtime.CompleteExecutionSnapshot(); err == nil {
		t.Fatal("execution snapshot closed before the main Agent answered available feedback")
	}
	if _, err := runtime.RecordReviewResponse(ReviewResponseInput{ReviewID: firstRequest.ReviewID, Disposition: "acknowledge", Reason: "the feedback used the previous evidence boundary", NextValidationPoint: "review the merged checkpoint event"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CompleteExecutionSnapshot(); err != nil {
		t.Fatal(err)
	}
	state, _ = runtime.LoadState()
	if state.Review.Status != "requested" || state.Review.Trigger == nil || !strings.Contains(*state.Review.Trigger, "stage-boundary") {
		t.Fatal("stage completion did not create its natural-boundary review")
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
	evidencePath := filepath.Join(runtime.RuntimeDir, "completion.json")
	if err := os.WriteFile(evidencePath, []byte(`{"status":"passed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordEvidence(EvidenceRecord{EvidenceID: "evidence-completion", Scope: []string{"completion"}, Path: evidencePath, ValidatorStatus: "passed", ValidatorSource: "unit-test"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CompleteExecutionSnapshot(); err != nil {
		t.Fatal(err)
	}
	respondToCurrentReview(t, runtime)
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
	evidencePath := filepath.Join(runtime.RuntimeDir, "advisory-finish.json")
	if err := os.WriteFile(evidencePath, []byte(`{"status":"passed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordEvidence(EvidenceRecord{EvidenceID: "evidence-advisory-finish", Scope: []string{"completion"}, Path: evidencePath, ValidatorStatus: "passed", ValidatorSource: "unit-test"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CompleteExecutionSnapshot(); err != nil {
		t.Fatal(err)
	}
	respondToCurrentReview(t, runtime)
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

func TestInvalidNativeGovernorResultFailsOpen(t *testing.T) {
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
	_ = hookJSON(t, runtime, "subagent-start", HookInput{SessionID: "main", AgentID: "governor-1", AgentType: runtime.Config.GovernorAgentName})
	invalid := "not-json"
	_ = hookJSON(t, runtime, "subagent-stop", HookInput{SessionID: "main", AgentID: "governor-1", AgentType: runtime.Config.GovernorAgentName, LastAssistantMessage: &invalid})
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Review.Status != "missed" || state.Status != "active" {
		t.Fatal("invalid native Governor result did not fail open")
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
	_, err := runtime.Init()
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
	if _, err := os.Stat(filepath.Join(runtime.RuntimeDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatal("migration finalization recreated the removed event stream")
	}
	if err := runtime.migrateLegacyStateIfNeeded(); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentStateMigrationPreservesActiveFactsAndRemovesHistory(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	request, err := runtime.RequestFixedReview("activation", "legacy-activation-source")
	if err != nil || request == nil {
		t.Fatalf("request review: %v", err)
	}
	if _, err := runtime.AcceptReview(validContinueFeedback(request)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordReviewResponse(ReviewResponseInput{ReviewID: request.ReviewID, Disposition: "acknowledge", Reason: "preserve this response", NextValidationPoint: "continue from the preserved point"}); err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(runtime.RuntimeDir, "reviews")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyReview := filepath.Join(legacyDir, request.ReviewID+".json")
	if err := os.Rename(runtime.reviewPath(), legacyReview); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(runtime.statePath())
	if err != nil {
		t.Fatal(err)
	}
	var legacyState map[string]any
	if err := json.Unmarshal(raw, &legacyState); err != nil {
		t.Fatal(err)
	}
	legacyState["review"].(map[string]any)["feedback_path"] = filepath.ToSlash(mustRelative(runtime.Root, legacyReview))
	if err := atomicWriteJSON(runtime.statePath(), legacyState); err != nil {
		t.Fatal(err)
	}
	event := map[string]any{"kind": "review_requested", "fields": map[string]any{"trigger": map[string]any{"type": "activation", "source_id": "legacy-activation-source"}}}
	eventData, _ := json.Marshal(event)
	if err := os.WriteFile(filepath.Join(runtime.RuntimeDir, "events.jsonl"), append(eventData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.migrateLegacyStateIfNeeded(); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Review.Status != "responded" || state.Review.Response == nil || state.ActivationSourceID == nil || *state.ActivationSourceID != "legacy-activation-source" {
		t.Fatal("current-state migration lost the active response or activation identity")
	}
	if !runtime.currentReviewValid(state) {
		t.Fatal("current-state migration did not preserve the current validated Governor feedback")
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatal("current-state migration retained the historical review directory")
	}
	if _, err := os.Stat(filepath.Join(runtime.RuntimeDir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatal("current-state migration retained the append-only event stream")
	}
	if _, err := os.Stat(filepath.Join(runtime.RuntimeDir, "migrations", "current-state-v1", "migration.json")); err != nil {
		t.Fatal("current-state migration did not write its explicit completion marker")
	}
	if err := runtime.migrateLegacyStateIfNeeded(); err != nil {
		t.Fatal("current-state migration is not reentrant")
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

func TestFastHookClassificationKeepsOrdinaryWorkOffTheRuntime(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".codex", "governance")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"runtime_directory":".codex/governance/runtime","activation_marker":"[fixture-governance:enable]"}`)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	ordinary, _ := json.Marshal(HookInput{Prompt: "ordinary task"})
	if FastHookRelevantAt("user-prompt-submit", ordinary, root) {
		t.Fatal("ordinary prompt entered the governance runtime")
	}
	misplaced, _ := json.Marshal(HookInput{Prompt: "intro\n[fixture-governance:enable]"})
	if FastHookRelevantAt("user-prompt-submit", misplaced, root) {
		t.Fatal("a marker outside the first nonempty line entered the runtime")
	}
	activation, _ := json.Marshal(HookInput{Prompt: "[fixture-governance:enable]\nrun goal"})
	if !FastHookRelevantAt("user-prompt-submit", activation, root) {
		t.Fatal("the exact activation marker did not enter the runtime")
	}
	session, _ := json.Marshal(HookInput{SessionID: "fixture-owner", Source: "startup"})
	if FastHookRelevantAt("session-start", session, root) {
		t.Fatal("unactivated SessionStart entered the runtime")
	}
	if err := os.MkdirAll(filepath.Join(configDir, "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "runtime", "state.json"), []byte(`{"status":"active","owner":{"session_id":"fixture-owner"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !FastHookRelevantAt("session-start", session, root) {
		t.Fatal("an activated SessionStart was not routed to the runtime")
	}
	foreignSession, _ := json.Marshal(HookInput{SessionID: "other-task", Source: "resume"})
	if FastHookRelevantAt("session-start", foreignSession, root) {
		t.Fatal("another task's SessionStart entered the runtime")
	}
	otherAgent, _ := json.Marshal(HookInput{AgentType: "worker", AgentID: "worker-1"})
	if FastHookRelevantAt("subagent-stop", otherAgent, root) {
		t.Fatal("an unrelated subagent entered the governance runtime")
	}
	ownedGovernor, _ := json.Marshal(HookInput{SessionID: "fixture-owner", AgentType: "governor", AgentID: "governor-1"})
	if !FastHookRelevantAt("subagent-start", ownedGovernor, root) {
		t.Fatal("the owning task's Governor did not enter the runtime")
	}
	foreignGovernor, _ := json.Marshal(HookInput{SessionID: "other-task", AgentType: "governor", AgentID: "governor-2"})
	if FastHookRelevantAt("subagent-start", foreignGovernor, root) {
		t.Fatal("another task's Governor entered the runtime")
	}
}

func TestLifecycleAndGovernorHooksAreIsolatedToTheOwningTask(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ensureHookOwner(HookInput{SessionID: "main-owner"}); err != nil {
		t.Fatal(err)
	}
	request, err := runtime.RequestFixedReview("activation", "owner-isolation")
	if err != nil || request == nil {
		t.Fatalf("owner review: %v", err)
	}

	sessionPayload, _ := json.Marshal(HookInput{SessionID: "other-task", Source: "resume"})
	if FastHookRelevantAt("session-start", sessionPayload, runtime.Root) {
		t.Fatal("another task's lifecycle event entered the governance runtime")
	}
	_ = hookJSON(t, runtime, "subagent-start", HookInput{SessionID: "other-task", AgentID: "foreign-governor", AgentType: runtime.Config.GovernorAgentName})
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Review.GovernorAgentID != nil {
		t.Fatal("another task's Governor was bound to the current review")
	}
	_ = hookJSON(t, runtime, "subagent-start", HookInput{SessionID: "main-owner", AgentID: "owned-governor", AgentType: runtime.Config.GovernorAgentName})
	state, _ = runtime.LoadState()
	if state.Review.GovernorAgentID == nil || *state.Review.GovernorAgentID != "owned-governor" {
		t.Fatal("the owning task's Governor was not bound")
	}
}

func TestAuthorityClaimsRequireARealStableLocator(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	request, err := runtime.RequestFixedReview("activation", "authority-locator")
	if err != nil {
		t.Fatal(err)
	}
	path := "docs/delivery/goal.md"
	var sourceHash string
	for _, ref := range request.AuthorityRefs {
		if filepath.ToSlash(ref.Path) == path {
			sourceHash = ref.Hash
			break
		}
	}
	data, err := os.ReadFile(resolvePath(runtime.Root, path))
	if err != nil || sourceHash == "" {
		t.Fatalf("authority source unavailable: %v", err)
	}
	heading := ""
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			heading = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
			break
		}
	}
	result := validContinueFeedback(request)
	result.AuthorityClaims = []AuthorityClaim{{Claim: "the cited section is authoritative", SourcePath: path, StableLocator: "heading:" + heading, SourceHash: sourceHash}}
	if _, err := runtime.AcceptReview(result); err != nil {
		t.Fatalf("valid stable authority locator was rejected: %v", err)
	}

	invalidRuntime := testRuntime(t)
	if _, err := invalidRuntime.Init(); err != nil {
		t.Fatal(err)
	}
	invalidRequest, err := invalidRuntime.RequestFixedReview("activation", "invalid-authority-locator")
	if err != nil {
		t.Fatal(err)
	}
	invalid := validContinueFeedback(invalidRequest)
	invalid.AuthorityClaims = []AuthorityClaim{{Claim: "unsupported hard claim", SourcePath: path, StableLocator: "heading:missing section", SourceHash: sourceHash}}
	if _, err := invalidRuntime.AcceptReview(invalid); err == nil {
		t.Fatal("a nonexistent authority locator was accepted")
	}
}

func TestProgressDeltaTracksCompletionMovementAndIgnoresUnrelatedEvidence(t *testing.T) {
	runtime := testRuntime(t)
	runtime.Config.EvidenceRoots = []string{runtime.RuntimeDir}
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	baselineRequest, err := runtime.RequestFixedReview("activation", "progress-baseline")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AcceptReview(validContinueFeedback(baselineRequest)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordReviewResponse(ReviewResponseInput{ReviewID: baselineRequest.ReviewID, Disposition: "acknowledge", Reason: "baseline established", NextValidationPoint: "next condition"}); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	state.CompletionConditions[0].Status = "met"
	if err := runtime.saveState(state); err != nil {
		t.Fatal(err)
	}
	stageTrigger, _ := newReviewTrigger("event", "stage-boundary", "condition-advanced", "stage boundary")
	request, err := runtime.requestReview(stageTrigger)
	if err != nil || request.ProgressDelta.NetProgress != "advanced" {
		t.Fatalf("completion movement was not reported as progress: %#v %v", request.ProgressDelta, err)
	}

	unrelatedPath := filepath.Join(runtime.RuntimeDir, "unrelated.json")
	if err := os.WriteFile(unrelatedPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelatedHash, _ := fileHash(unrelatedPath)
	state, _ = runtime.LoadState()
	state.ReusableResults = append(state.ReusableResults, ReusableResult{ResultID: "unrelated", Scope: []string{"condition:unrelated"}, EvidencePath: unrelatedPath, InputHash: unrelatedHash})
	if err := runtime.saveState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.AcceptReview(validContinueFeedback(request)); err != nil {
		t.Fatal(err)
	}
	state, _ = runtime.LoadState()
	if state.Review.Status != "feedback_ready" {
		t.Fatalf("unrelated evidence made feedback stale: %s", state.Review.Status)
	}
}

func TestPendingReviewEventsAreMergedAndGovernorAbsenceFailsOpenAtBoundary(t *testing.T) {
	runtime := testRuntime(t)
	runtime.Config.EvidenceRoots = []string{runtime.RuntimeDir}
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	condition := mustCondition(t, runtime)
	if _, err := runtime.UpdateExecutionSnapshot(ExecutionSnapshotInput{FocusID: "focus-boundary", ConditionID: condition, Objective: "reach one evidence checkpoint", Value: "prove fail-open advisory semantics", InvolvedScope: []string{"governance"}, ExpectedEvidence: []string{"checkpoint-evidence"}, CheckpointID: "checkpoint-boundary", CheckpointDescription: "checkpoint evidence exists"}); err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(runtime.RuntimeDir, "checkpoint.json")
	if err := os.WriteFile(evidencePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	request, err := runtime.RequestFixedReview("activation", "active-review")
	if err != nil || request == nil {
		t.Fatalf("fixed review was not created: %v", err)
	}
	if recorded, err := runtime.RecordEvidence(EvidenceRecord{EvidenceID: "checkpoint-evidence", Path: evidencePath, Scope: []string{condition}, ValidatorStatus: "passed", ValidatorSource: "fixture"}); err != nil || recorded != nil {
		t.Fatalf("successful evidence incorrectly created a review: %v", err)
	}
	if err := runtime.CompleteExecutionSnapshot(); err != nil {
		t.Fatalf("unavailable Governor blocked a mechanically valid boundary: %v", err)
	}
	state, _ := runtime.LoadState()
	if state.CurrentFocus != nil || state.CompletionConditions[0].Status != "met" || state.Review.Status != "requested" || state.Review.Pending == nil || len(state.Review.Pending.TriggerTypes) != 1 || state.Review.Pending.TriggerTypes[0] != "stage-boundary" {
		t.Fatalf("boundary did not fail open cleanly: %#v", state.Review)
	}
}

func TestIncidentReplayExposesZeroNetProgressBeforeAnotherCostlyAttempt(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	condition := mustCondition(t, runtime)
	if _, err := runtime.UpdateExecutionSnapshot(ExecutionSnapshotInput{
		FocusID: "qualification-loop", ConditionID: condition,
		Objective: "establish the first valid core baseline", Value: "cross the current formal completion point",
		InvolvedScope: []string{"acceptance-adapter"}, ExpectedEvidence: []string{"formal-qualification"},
		CheckpointID: "qualification", CheckpointDescription: "the current candidate passes qualification",
	}); err != nil {
		t.Fatal(err)
	}
	baseline, err := runtime.RequestFixedReview("activation", "incident-baseline")
	if err != nil || baseline == nil {
		t.Fatalf("baseline review: %v", err)
	}
	if _, err := runtime.AcceptReview(validContinueFeedback(baseline)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordReviewResponse(ReviewResponseInput{ReviewID: baseline.ReviewID, Disposition: "acknowledge", Reason: "baseline facts recorded", NextValidationPoint: "qualification"}); err != nil {
		t.Fatal(err)
	}

	request, err := runtime.RecordCheckpointResult(CheckpointResultInput{
		FocusID: "qualification-loop", CheckpointID: "qualification", Outcome: "failed",
		FailureCategory: "formal-qualification-still-fails", SourceExecution: "candidate-after-local-fixes",
		ResultHash: "sha256:" + strings.Repeat("d", 64), EvidenceIDs: []string{},
	})
	if err != nil || request == nil {
		t.Fatalf("failed formal checkpoint did not create a review: %v", err)
	}
	if request.ProgressDelta.NetProgress != "zero" || request.ProgressDelta.CheckpointOutcome != "failed" || request.ProgressDelta.CriticalConditionID != condition {
		t.Fatalf("incident facts did not expose zero conversion at the completion condition: %#v", request.ProgressDelta)
	}
	if request.CurrentFocus == nil || request.CurrentFocus.ExecutionID == "" || request.Trigger.Type != "evidence-checkpoint-missed" {
		t.Fatalf("incident request lost its execution identity or factual boundary: %#v", request)
	}
}

func TestIncidentReplayKeepsShortUnrelatedWorkAtZeroGovernanceCost(t *testing.T) {
	runtime := testRuntime(t)
	prompt := runtime.Config.ActivationMarker + "\ncontinue the product goal"
	_ = hookJSON(t, runtime, "user-prompt-submit", map[string]any{"session_id": "main", "turn_id": "activation", "prompt": prompt})
	paths := []string{runtime.statePath(), runtime.requestPath(), runtime.reviewPath()}
	before := map[string][]byte{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil {
			before[path] = data
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	if got := hookJSON(t, runtime, "user-prompt-submit", map[string]any{"session_id": "main", "turn_id": "side-doc", "prompt": "write a short unrelated note"}); got != "{}" {
		t.Fatalf("short unrelated work received governance protocol: %s", got)
	}
	for _, event := range []string{"post-tool-use", "pre-compact", "stop"} {
		if got := hookJSON(t, runtime, event, map[string]any{"session_id": "main", "turn_id": "side-doc", "tool_name": "exec", "tool_response": map[string]any{"exit_code": 0}}); got != "{}" {
			t.Fatalf("unrelated short work produced governance output through %s: %s", event, got)
		}
	}
	for _, path := range paths {
		after, err := os.ReadFile(path)
		if original, existed := before[path]; existed {
			if err != nil || !bytes.Equal(original, after) {
				t.Fatalf("short unrelated work changed governance file %s", filepath.Base(path))
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("short unrelated work created governance file %s", filepath.Base(path))
		}
	}
}

func TestMigrationValidationUsesACopyAndIsIdempotent(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.Init(); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	state.SchemaVersion = 2
	source, err := os.MkdirTemp(filepath.Join(runtime.Root, ".codex", "governance"), ".migration-source-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(source)
	if err := atomicWriteJSON(filepath.Join(source, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(source, "state.json"))
	report, err := runtime.ValidateStateMigrationCopy(source)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(source, "state.json"))
	if report.Status != "passed" || !report.RepeatIsIdempotent || !report.SourceUnchanged || !bytes.Equal(before, after) {
		t.Fatalf("migration copy validation was not lossless: %#v", report)
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
