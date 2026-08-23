package governance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
