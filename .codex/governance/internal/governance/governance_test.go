package governance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func approvedRuntimeFixture(t *testing.T) *Runtime {
	t.Helper()
	base, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	fixture := *base
	fixture.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
	fixture.Config.EvidenceRoots = []string{filepath.Join(fixture.RuntimeDir, "evidence")}
	if _, err := fixture.Init(); err != nil {
		t.Fatal(err)
	}
	request, err := fixture.Resume("test-start")
	if err != nil {
		t.Fatal(err)
	}
	state, _ := fixture.LoadState()
	proposal := &WorkPacketProposal{
		PacketID: "packet", ConditionID: state.CompletionConditions[0].ConditionID,
		Objective: "test failure truth", Value: "keep events honest", AllowedScope: []string{"internal"},
		ExpectedEvidence: []string{"repair-evidence", "final-evidence"}, CheckpointID: "checkpoint", CheckpointDescription: "done",
	}
	gap := "test gap"
	result := ReviewResult{
		ReviewID: request.ReviewID, TriggerInstanceID: request.TriggerInstanceID, ReviewSnapshotHash: request.ReviewSnapshotHash,
		Decision: "start", MacroAssessment: MacroAssessment{OverallProgress: "start", EvidenceSupport: "fixture", Unmet: []string{"test"}},
		HighestPriorityGap: &gap, PathAssessment: PathAssessment{Necessary: true, Efficient: true, Optimal: true},
		NextWorkPacket: proposal, Reason: "start fixture",
	}
	if _, err := fixture.AcceptReview(result); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.ApplyReview(); err != nil {
		t.Fatal(err)
	}
	return &fixture
}

func TestDoctor(t *testing.T) {
	runtime, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	report, err := runtime.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "passed" || len(report.Checks) < 6 {
		t.Fatalf("unexpected doctor report: %#v", report)
	}
}

func TestStrictJSONRejectsUnknownFields(t *testing.T) {
	input := `{"packet_id":"p","condition_id":"c","objective":"o","value":"v","allowed_scope":["."],"excluded_scope":[],"expected_evidence":["e"],"checkpoint_id":"cp","checkpoint_description":"d","unexpected":true}`
	var proposal WorkPacketProposal
	if err := decodeStrict(strings.NewReader(input), &proposal); err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func TestFailureEventsAreIdempotentAndOnlyRepeatAcrossVerifiedRepair(t *testing.T) {
	runtime := approvedRuntimeFixture(t)
	first := FailureEventInput{Signature: "same failure", SourceKind: "codex_hook", SourceExecution: "session:turn", ToolUseID: "tool-1", EvidenceHash: sha256Value([]byte("first"))}
	if request, err := runtime.RecordFailureEvent(first); err != nil || request != nil {
		t.Fatalf("first failure: %v %#v", err, request)
	}
	if request, err := runtime.RecordFailureEvent(first); err != nil || request != nil {
		t.Fatalf("duplicate failure was not idempotent: %v %#v", err, request)
	}
	second := first
	second.ToolUseID = "tool-2"
	second.EvidenceHash = sha256Value([]byte("second"))
	if request, err := runtime.RecordFailureEvent(second); err != nil || request != nil {
		t.Fatalf("same-generation occurrence triggered review: %v %#v", err, request)
	}
	evidence := filepath.Join(runtime.RuntimeDir, "evidence", "repair.txt")
	if err := os.MkdirAll(filepath.Dir(evidence), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, []byte("verified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordEvidence(EvidenceRecord{EvidenceID: "repair-evidence", Path: evidence, Scope: []string{"internal"}, ValidatorStatus: "passed", ValidatorSource: "test"}); err != nil {
		t.Fatal(err)
	}
	repairInput := FailureRepairInput{Signature: "same failure", PreviousEventID: "failure_" + strings.TrimPrefix(mustHashJSON(map[string]any{"packet_id": "packet", "source_kind": "codex_hook", "source_execution": "session:turn", "tool_use_id": "tool-1"}), "sha256:")[:24], EvidenceIDs: []string{"repair-evidence"}}
	if _, err := runtime.RecordRepair(repairInput); err == nil || !strings.Contains(err.Error(), "unchanged") {
		t.Fatalf("unchanged runtime facts advanced a repair generation: %v", err)
	}
	repairConfig := filepath.Join(t.TempDir(), "governance.json")
	if err := os.WriteFile(repairConfig, []byte(`{"repair":"verified"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.ConfigPath = repairConfig
	repair, err := runtime.RecordRepair(repairInput)
	if err != nil {
		t.Fatal(err)
	}
	conflictingRepair := repairInput
	conflictingRepair.EvidenceIDs = []string{"different-evidence"}
	if _, err := runtime.RecordRepair(conflictingRepair); err == nil || !strings.Contains(err.Error(), "conflicting evidence") {
		t.Fatalf("conflicting repair replay was not rejected: %v", err)
	}
	if replay, err := runtime.RecordRepair(repairInput); err != nil || replay.RepairID != repair.RepairID {
		t.Fatalf("identical repair replay was not idempotent: %v %#v", err, replay)
	}
	third := first
	third.ToolUseID = "tool-3"
	third.EvidenceHash = sha256Value([]byte("third"))
	request, err := runtime.RecordFailureEvent(third)
	if err != nil || request == nil || request.Trigger.Reason != "repeated-failure:same failure" {
		t.Fatalf("post-repair recurrence did not trigger one review: %v %#v", err, request)
	}
	if replay, err := runtime.RecordRepair(repairInput); err != nil || replay.RepairID != repair.RepairID {
		t.Fatalf("repair replay stopped being idempotent after review: %v %#v", err, replay)
	}
	if replay, err := runtime.RecordFailureEvent(third); err != nil || replay != nil {
		t.Fatalf("review-triggering failure replay was not idempotent: %v %#v", err, replay)
	}
}

func TestRepairCannotReuseEvidenceThatPredatesTheFailure(t *testing.T) {
	runtime := approvedRuntimeFixture(t)
	evidence := filepath.Join(runtime.RuntimeDir, "evidence", "old.txt")
	if err := os.MkdirAll(filepath.Dir(evidence), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordEvidence(EvidenceRecord{EvidenceID: "repair-evidence", Path: evidence, Scope: []string{"internal"}, ValidatorStatus: "passed", ValidatorSource: "test"}); err != nil {
		t.Fatal(err)
	}
	input := FailureEventInput{Signature: "failure after old evidence", SourceKind: "codex_hook", SourceExecution: "session:turn", ToolUseID: "tool-old", EvidenceHash: sha256Value([]byte("failure"))}
	if _, err := runtime.RecordFailureEvent(input); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	event := state.CurrentWorkPacket.FailureEvents[len(state.CurrentWorkPacket.FailureEvents)-1]
	changedConfig := filepath.Join(t.TempDir(), "governance.json")
	if err := os.WriteFile(changedConfig, []byte(`{"changed":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.ConfigPath = changedConfig
	_, err = runtime.RecordRepair(FailureRepairInput{Signature: input.Signature, PreviousEventID: event.EventID, EvidenceIDs: []string{"repair-evidence"}})
	if err == nil || !strings.Contains(err.Error(), "predates") {
		t.Fatalf("pre-failure evidence advanced a repair generation: %v", err)
	}
}

func TestFailureEventIdentityRejectsConflictingReplay(t *testing.T) {
	runtime := approvedRuntimeFixture(t)
	input := FailureEventInput{Signature: "one failure", SourceKind: "codex_hook", SourceExecution: "session:turn", ToolUseID: "tool-stable", EvidenceHash: sha256Value([]byte("first"))}
	if _, err := runtime.RecordFailureEvent(input); err != nil {
		t.Fatal(err)
	}
	conflict := input
	conflict.EvidenceHash = sha256Value([]byte("changed"))
	if _, err := runtime.RecordFailureEvent(conflict); err == nil || !strings.Contains(err.Error(), "conflicting facts") {
		t.Fatalf("conflicting replay was not rejected: %v", err)
	}
	conflict = input
	conflict.Signature = "different failure"
	if _, err := runtime.RecordFailureEvent(conflict); err == nil || !strings.Contains(err.Error(), "conflicting facts") {
		t.Fatalf("conflicting signature replay was not rejected: %v", err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.CurrentWorkPacket.FailureEvents) != 1 {
		t.Fatalf("conflicting replay changed failure facts: %#v", state.CurrentWorkPacket.FailureEvents)
	}
}

func TestHookBlocksAndRequestsReviewWhenFailureRecordingConflicts(t *testing.T) {
	runtime := approvedRuntimeFixture(t)
	first := HookInput{
		ToolName: "Bash", TurnID: "turn", ToolUseID: "tool-conflict",
		ToolResponse: json.RawMessage(`{"exit_code":1,"output":"first failure"}`),
	}
	if err := runtime.hookPostToolUse(first, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.ToolResponse = json.RawMessage(`{"exit_code":1,"output":"conflicting replay"}`)
	output := &strings.Builder{}
	if err := runtime.hookPostToolUse(conflict, output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"decision":"block"`) || !strings.Contains(output.String(), "integrity review") {
		t.Fatalf("conflicting failure recording was silently ignored: %s", output.String())
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Review.Required || state.Review.Trigger == nil || *state.Review.Trigger != "event:failure-event-recording-integrity" {
		t.Fatalf("failure recording conflict did not create a truthful review: %#v", state.Review)
	}
}

func TestVerifiedFailureRejectsAMissingEvidenceSnapshot(t *testing.T) {
	runtime := approvedRuntimeFixture(t)
	input := FailureEventInput{Signature: "failure with evidence snapshot", SourceKind: "codex_hook", SourceExecution: "session:turn", ToolUseID: "tool-snapshot", EvidenceHash: sha256Value([]byte("failure"))}
	if _, err := runtime.RecordFailureEvent(input); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	data, err := os.ReadFile(runtime.statePath())
	if err != nil || json.Unmarshal(data, &raw) != nil {
		t.Fatal("cannot load fixture state")
	}
	packet := raw["current_work_packet"].(map[string]any)
	events := packet["failure_events"].([]any)
	delete(events[0].(map[string]any), "known_evidence_ids")
	if err := atomicWriteJSON(runtime.statePath(), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.LoadState(); err == nil || !strings.Contains(err.Error(), "known_evidence_ids") {
		t.Fatalf("verified failure without its evidence snapshot was accepted: %v", err)
	}
}

func mustHashJSON(value any) string {
	hash, err := hashJSON(value)
	if err != nil {
		panic(err)
	}
	return hash
}

func TestLegacyFailuresMigrateAsNonCountingFactsAndReplacePendingReview(t *testing.T) {
	runtime := approvedRuntimeFixture(t)
	request, err := runtime.RequestReview("legacy-pending")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	data, _ := os.ReadFile(runtime.statePath())
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	packet := raw["current_work_packet"].(map[string]any)
	packet["failure_signatures"] = []any{"legacy failure"}
	delete(packet, "failure_events")
	delete(packet, "failure_repairs")
	if err := atomicWriteJSON(runtime.statePath(), raw); err != nil {
		t.Fatal(err)
	}
	if err := runtime.migrateFailureEvents(); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.CurrentWorkPacket.FailureSignatures) != 0 || len(state.CurrentWorkPacket.FailureEvents) != 1 || state.CurrentWorkPacket.FailureEvents[0].Trust != "legacy_unverified" {
		t.Fatalf("legacy failure was not migrated safely: %#v", state.CurrentWorkPacket)
	}
	if state.CurrentWorkPacket.FailureEvents[0].KnownEvidenceIDs == nil {
		t.Fatal("legacy failure did not normalize the evidence snapshot to an empty array")
	}
	if state.Review.ReviewID == nil || *state.Review.ReviewID == request.ReviewID {
		t.Fatal("legacy pending review was not replaced with a current-schema request")
	}
	if _, err := os.Stat(filepath.Join(runtime.reviewsDir(), "superseded", request.ReviewID+".json")); err != nil {
		t.Fatal("legacy review request was not preserved for audit")
	}
}

func TestFailureMigrationResumeKeepsItsAlreadyCreatedCurrentReview(t *testing.T) {
	runtime := approvedRuntimeFixture(t)
	if _, err := runtime.RequestReview("legacy pending review"); err != nil {
		t.Fatal(err)
	}
	state, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	state.CurrentWorkPacket.FailureEvents = nil
	state.CurrentWorkPacket.FailureRepairs = nil
	state.CurrentWorkPacket.FailureSignatures = []string{"legacy failure"}
	if err := atomicWriteJSON(runtime.statePath(), state); err != nil {
		t.Fatal(err)
	}
	if err := runtime.migrateFailureEvents(); err != nil {
		t.Fatal(err)
	}
	migrated, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !migrated.Review.Required || migrated.Review.ReviewID == nil {
		t.Fatal("migration did not create its current-snapshot review")
	}
	reviewID := *migrated.Review.ReviewID
	markerPath := filepath.Join(runtime.RuntimeDir, "failure-event-migration.json")
	marker := map[string]any{"schema": "ownward.governance-migration/v1", "migration_id": "failure-events-v1", "status": "prepared", "replace_review": true}
	if err := atomicWriteJSON(markerPath, marker); err != nil {
		t.Fatal(err)
	}
	if err := runtime.migrateFailureEvents(); err != nil {
		t.Fatal(err)
	}
	resumed, err := runtime.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Review.ReviewID == nil || *resumed.Review.ReviewID != reviewID {
		t.Fatalf("migration resume replaced its already-current review: before=%s after=%v", reviewID, resumed.Review.ReviewID)
	}
	if _, err := os.Stat(filepath.Join(runtime.reviewsDir(), "superseded", reviewID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration resume incorrectly superseded its current review: %v", err)
	}
}

func TestResumeArchivesAStalePendingReviewBeforeReplacingIt(t *testing.T) {
	runtime := approvedRuntimeFixture(t)
	old, err := runtime.RequestReview("old-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	current, err := runtime.Resume("current-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if current == nil || current.ReviewID == old.ReviewID {
		t.Fatal("resume did not create a current-snapshot review")
	}
	if _, err := os.Stat(filepath.Join(runtime.reviewsDir(), "superseded", old.ReviewID+".json")); err != nil {
		t.Fatal("stale pending review was not preserved for audit")
	}
}

func TestGovernorMCPIsolationRequiresStandaloneTransport(t *testing.T) {
	project := []byte("[mcp_servers.ownward]\nenabled = true\nrequired = true\n")
	partial := []byte("sandbox_mode = \"read-only\"\n[mcp_servers.ownward]\nenabled = false\nrequired = false\n")
	if err := validateGovernorConfiguration(project, partial); err == nil || !strings.Contains(err.Error(), "standalone transport") {
		t.Fatalf("partial MCP override was accepted: %v", err)
	}
	complete := []byte("sandbox_mode = \"read-only\"\n[mcp_servers.ownward]\ncommand = 'ownward'\nargs = ['mcp']\ncwd = '.'\nenabled = false\nrequired = false\n")
	if err := validateGovernorConfiguration(project, complete); err != nil {
		t.Fatalf("complete disabled MCP transport was rejected: %v", err)
	}
}

func TestGovernedRunAllowsLiveProcessWithoutTotalDeadline(t *testing.T) {
	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	t.Setenv("GO_WANT_GOVERNANCE_HELPER", "live")
	t.Setenv("GO_GOVERNANCE_HEARTBEAT", heartbeat)
	err := GovernedRun(GovernedRunOptions{
		Command:       []string{os.Args[0], "-test.run=TestGovernanceHelperProcess", "--"},
		HeartbeatPath: heartbeat, StaleAfter: 250 * time.Millisecond, StartupGrace: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGovernedRunStopsStaleProcess(t *testing.T) {
	heartbeat := filepath.Join(t.TempDir(), "heartbeat")
	t.Setenv("GO_WANT_GOVERNANCE_HELPER", "stale")
	t.Setenv("GO_GOVERNANCE_HEARTBEAT", heartbeat)
	started := time.Now()
	err := GovernedRun(GovernedRunOptions{
		Command:       []string{os.Args[0], "-test.run=TestGovernanceHelperProcess", "--"},
		HeartbeatPath: heartbeat, StaleAfter: 120 * time.Millisecond, StartupGrace: 120 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "lost heartbeat") {
		t.Fatalf("expected stale heartbeat failure, got %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("stale process was not terminated promptly")
	}
}

func TestGovernanceHelperProcess(t *testing.T) {
	mode := os.Getenv("GO_WANT_GOVERNANCE_HELPER")
	if mode == "" {
		return
	}
	heartbeat := os.Getenv("GO_GOVERNANCE_HEARTBEAT")
	write := func() {
		_ = os.WriteFile(heartbeat, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600)
	}
	write()
	if mode == "live" {
		for index := 0; index < 8; index++ {
			time.Sleep(50 * time.Millisecond)
			write()
		}
		return
	}
	time.Sleep(5 * time.Second)
}

func TestReviewResultConditionalValidation(t *testing.T) {
	result := ReviewResult{
		ReviewID: "r", TriggerInstanceID: "t", ReviewSnapshotHash: "sha256:" + strings.Repeat("0", 64), Decision: "continue",
		MacroAssessment:    MacroAssessment{OverallProgress: "p", EvidenceSupport: "e", Completed: []string{}, Unmet: []string{"u"}},
		HighestPriorityGap: stringPointer("u"), PathAssessment: PathAssessment{Necessary: true, Efficient: false, Optimal: true},
		PreservedResultIDs: []string{}, InvalidatedItems: []string{}, ValidatedEvidenceIDs: []string{},
		NextWorkPacket: nil, Reason: "reason",
	}
	if err := validateReviewResult(&result); err == nil {
		data, _ := json.Marshal(result)
		t.Fatalf("invalid continue review was accepted: %s", data)
	}
	result.PathAssessment.Efficient = true
	result.NextWorkPacket = &WorkPacketProposal{PacketID: "p", ConditionID: "c", Objective: "o", Value: "v", AllowedScope: []string{"."}, ExpectedEvidence: []string{"e"}, CheckpointID: "cp", CheckpointDescription: "d"}
	if err := validateReviewResult(&result); err == nil {
		t.Fatal("continue accepted a replacement work packet")
	}
	result.Decision = "start"
	if err := validateReviewResult(&result); err != nil {
		t.Fatalf("valid start review was rejected: %v", err)
	}
}

func TestStateLockIgnoresStaleSentinelAndRejectsLiveContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	if err := os.WriteFile(path, []byte("dead-process\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := acquireStateLock(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("stale lock file blocked recovery: %v", err)
	}
	defer first.release()
	if _, err := acquireStateLock(path, 40*time.Millisecond); err == nil || !strings.Contains(err.Error(), "live writer") {
		t.Fatalf("live lock contention was not reported structurally: %v", err)
	}
}

func TestGovernanceOwnershipAndOneTimeHandoff(t *testing.T) {
	runtime, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	fixture := *runtime
	fixture.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
	fixture.Config.EvidenceRoots = []string{filepath.Join(fixture.RuntimeDir, "evidence")}
	if _, err := fixture.Init(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.hookSessionStart(HookInput{SessionID: "owner-a", TranscriptPath: "a.jsonl", Source: "startup"}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	before, err := fixture.LoadState()
	if err != nil || before.Owner == nil || before.Owner.SessionID != "owner-a" {
		t.Fatalf("first session did not become owner: %#v %v", before, err)
	}
	if err := fixture.hookSessionStart(HookInput{SessionID: "competitor-b", Source: "startup"}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	afterCompetitor, _ := fixture.LoadState()
	if afterCompetitor.Owner.SessionID != "owner-a" || afterCompetitor.Review.FixedReviewGeneration != before.Review.FixedReviewGeneration {
		t.Fatal("competing session changed owner or review generation")
	}
	ticket, err := fixture.PrepareHandoff("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	bindAllowed := &strings.Builder{}
	bindCommand := HookInput{SessionID: "owner-a", ToolName: "Bash", ToolInput: json.RawMessage(`{"cmd":".codex\\governance\\governance-hook.ps1 bind-handoff --handoff-id fixture --target-thread-id thread-b"}`)}
	if err := fixture.hookPreToolUse(bindCommand, bindAllowed); err != nil || strings.Contains(bindAllowed.String(), `"deny"`) {
		t.Fatalf("source owner could not bind its prepared handoff: %v %s", err, bindAllowed.String())
	}
	frozenProduct := &strings.Builder{}
	if err := fixture.hookPreToolUse(HookInput{SessionID: "owner-a", ToolName: "apply_patch", ToolInput: json.RawMessage(`{"patch":"*** Update File: internal/example.go"}`)}, frozenProduct); err != nil || !strings.Contains(frozenProduct.String(), "handoff is pending") {
		t.Fatalf("prepared handoff did not freeze source product work: %v %s", err, frozenProduct.String())
	}
	competitorBind := &strings.Builder{}
	if err := fixture.hookPreToolUse(HookInput{SessionID: "competitor-b", ToolName: "Bash", ToolInput: bindCommand.ToolInput}, competitorBind); err != nil || !strings.Contains(competitorBind.String(), "not the active governance owner") {
		t.Fatalf("competing task could bind another owner's handoff: %v %s", err, competitorBind.String())
	}
	if err := fixture.BindHandoff(ticket.HandoffID, "owner-b"); err != nil {
		t.Fatal(err)
	}
	if consumed, err := fixture.consumeHandoff(HookInput{SessionID: "wrong-target", Prompt: "[ownward-governance-handoff id=" + ticket.HandoffID + " token=" + ticket.Token + "]"}); err == nil || consumed {
		t.Fatal("a valid handoff token was accepted by a task other than its bound target")
	}
	if err := fixture.hookUserPrompt(HookInput{SessionID: "owner-b", TranscriptPath: "b.jsonl", Prompt: "continue [ownward-governance-handoff id=" + ticket.HandoffID + " token=" + ticket.Token + "]"}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	transferred, _ := fixture.LoadState()
	if transferred.Owner.SessionID != "owner-b" || transferred.Owner.OwnerEpoch != 2 || transferred.Handoff != nil {
		t.Fatalf("handoff did not transfer exactly once: %#v", transferred.Owner)
	}
	if consumed, err := fixture.consumeHandoff(HookInput{SessionID: "owner-c", Prompt: "[ownward-governance-handoff id=" + ticket.HandoffID + " token=" + ticket.Token + "]"}); err == nil || consumed {
		t.Fatal("consumed handoff token was accepted twice")
	}
	denied := &strings.Builder{}
	if err := fixture.hookPreToolUse(HookInput{SessionID: "owner-a", ToolName: "apply_patch", ToolInput: json.RawMessage(`{"patch":"*** Update File: internal/example.go"}`)}, denied); err != nil || !strings.Contains(denied.String(), "not the active governance owner") {
		t.Fatalf("retired owner was not made read-only: %v %s", err, denied.String())
	}
}

func TestGovernorFailureLatchesAndStopDoesNotLoop(t *testing.T) {
	runtime, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	fixture := *runtime
	fixture.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
	fixture.Config.EvidenceRoots = []string{filepath.Join(fixture.RuntimeDir, "evidence")}
	if _, err := fixture.Init(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.hookSessionStart(HookInput{SessionID: "owner", Source: "startup"}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	failure := HookInput{SessionID: "owner", ToolName: "Agent", ToolInput: json.RawMessage(`{"name":"governor"}`), ToolResponse: json.RawMessage(`{"isError":true,"message":"required MCP failed"}`)}
	if err := fixture.hookPostToolUse(failure, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	state, _ := fixture.LoadState()
	if state.InfrastructureFailure == nil {
		t.Fatal("Governor infrastructure failure was not latched")
	}
	stop := &strings.Builder{}
	if err := fixture.hookStop(HookInput{SessionID: "owner"}, stop); err != nil || strings.Contains(stop.String(), `"decision":"block"`) {
		t.Fatalf("latched infrastructure failure caused a Stop loop: %v %s", err, stop.String())
	}
	prompt := &strings.Builder{}
	if err := fixture.hookUserPrompt(HookInput{SessionID: "owner", Prompt: "continue"}, prompt); err != nil || !strings.Contains(prompt.String(), "Do not retry") {
		t.Fatalf("latched failure did not return deterministic recovery context: %v %s", err, prompt.String())
	}
}

func TestGovernorResultFailureAlsoLatchesAndStopsRetry(t *testing.T) {
	runtime, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	fixture := *runtime
	fixture.RuntimeDir = filepath.Join(t.TempDir(), "runtime")
	fixture.Config.EvidenceRoots = []string{filepath.Join(fixture.RuntimeDir, "evidence")}
	if _, err := fixture.Init(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.hookSessionStart(HookInput{SessionID: "owner", Source: "startup"}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	failure := HookInput{
		SessionID: "owner", ToolName: "Bash",
		ToolInput:    json.RawMessage(`{"cmd":".codex\\governance\\governance-hook.ps1 accept-review --json-base64 invalid"}`),
		ToolResponse: json.RawMessage(`{"exit_code":1,"output":"review result failed schema validation"}`),
	}
	if err := fixture.hookPostToolUse(failure, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	state, _ := fixture.LoadState()
	if state.InfrastructureFailure == nil {
		t.Fatal("Governor result validation failure was not latched")
	}
	stop := &strings.Builder{}
	if err := fixture.hookStop(HookInput{SessionID: "owner"}, stop); err != nil || strings.Contains(stop.String(), `"decision":"block"`) {
		t.Fatalf("Governor result failure still caused a Stop retry loop: %v %s", err, stop.String())
	}
}

func TestAgentCapabilityMatrixRejectsUnknownRoleAndClassifiesGovernorStructurally(t *testing.T) {
	runtime, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	inactive := *runtime
	inactive.RuntimeDir = filepath.Join(t.TempDir(), "inactive-runtime")
	unknown := &strings.Builder{}
	if err := inactive.hookPreToolUse(HookInput{ToolName: "spawn_agent", ToolInput: json.RawMessage(`{"agent_type":"product-design-critic","message":"review the UI"}`)}, unknown); err != nil || !strings.Contains(unknown.String(), "absent from the explicit capability matrix") {
		t.Fatalf("unknown inherited role was not denied: %v %s", err, unknown.String())
	}
	known := &strings.Builder{}
	if err := inactive.hookPreToolUse(HookInput{ToolName: "spawn_agent", ToolInput: json.RawMessage(`{"agent_type":"worker","message":"inspect Governor failures"}`)}, known); err != nil || strings.Contains(known.String(), `"deny"`) {
		t.Fatalf("registered role was denied while governance was inactive: %v %s", err, known.String())
	}
	if isGovernorAgentAttempt(runtime, HookInput{ToolName: "spawn_agent", ToolInput: json.RawMessage(`{"agent_type":"worker","message":"inspect Governor failures"}`)}) {
		t.Fatal("a worker message mentioning Governor was misclassified as a Governor launch")
	}
	if !isGovernorAgentAttempt(runtime, HookInput{ToolName: "spawn_agent", ToolInput: json.RawMessage(`{"agent_type":"governor","message":"review"}`)}) {
		t.Fatal("an explicit Governor launch was not recognized")
	}
}

func TestGovernanceControlsRequireTheExactAction(t *testing.T) {
	if !isExactInfrastructureRecoveryControl(`.codex\governance\governance-hook.ps1 status`) {
		t.Fatal("exact status command was rejected")
	}
	if !isExactInfrastructureRecoveryControl(`sh ".codex/governance/governance-hook.sh" status`) {
		t.Fatal("exact shell status command was rejected")
	}
	for _, command := range []string{
		`.codex\governance\governance-hook.ps1 finish status`,
		`.codex\governance\governance-hook.ps1 status-extra`,
		`.codex/governance/governance-hook.sh apply-review status`,
		`echo .codex/governance/governance-hook.sh status`,
		`Remove-Item fixture .codex\governance\governance-hook.ps1 status`,
		`cmd /c .codex\governance\governance-hook.ps1 status`,
	} {
		if isExactInfrastructureRecoveryControl(command) {
			t.Fatalf("non-recovery action bypassed the infrastructure latch: %s", command)
		}
	}
}

func TestLowCostReadsCannotHideWritesOrProcessExecution(t *testing.T) {
	for _, command := range []string{
		`rg --files`,
		`Get-Content docs/delivery/goal.md`,
		`git diff -- docs/delivery/goal.md`,
		`go env GOROOT`,
		`go version`,
	} {
		if !isLowCostRead("Bash", command) {
			t.Fatalf("canonical read command was rejected: %s", command)
		}
	}
	for _, command := range []string{
		`rg --pre malicious.exe pattern .`,
		`rg --hostname-bin=malicious.exe pattern .`,
		`git diff --output=fixture.patch`,
		`git show --ext-diff HEAD`,
		`go env -w GOPROXY=off`,
		`go env -u GOPROXY`,
		`go list -mod=mod ./...`,
	} {
		if isLowCostRead("Bash", command) {
			t.Fatalf("side-effecting command was misclassified as a low-cost read: %s", command)
		}
	}
}

func TestControlPlaneRepairScopeExcludesProductAndGovernanceContracts(t *testing.T) {
	allowed := []string{
		".codex/config.toml",
		".codex/hooks.json",
		".codex/agents/governor.toml",
		".codex/governance/cmd/governance-cli/main.go",
		".codex/governance/internal/governance/hooks.go",
	}
	for _, path := range allowed {
		if !repairAllowed(path) {
			t.Fatalf("control-plane repair rejected an allowed path: %s", path)
		}
	}
	denied := []string{
		"internal/core/kernel.go",
		"docs/product/requirements.md",
		".codex/governance/config.json",
		".codex/governance/state.schema.json",
		".codex/governance/runtime/state.json",
		".codex/agents/README.md",
	}
	for _, path := range denied {
		if repairAllowed(path) {
			t.Fatalf("control-plane repair admitted a protected or product path: %s", path)
		}
	}
}
