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
