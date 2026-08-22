package governance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type DoctorReport struct {
	Status string   `json:"status"`
	Checks []string `json:"checks"`
}

func (runtime *Runtime) Doctor() (*DoctorReport, error) {
	checks := []string{}
	if err := validateSchemaDocuments(runtime); err != nil {
		return nil, err
	}
	checks = append(checks, "closed JSON contracts are present and parseable")

	hooksData, err := os.ReadFile(filepath.Join(runtime.Root, ".codex", "hooks.json"))
	if err != nil {
		return nil, err
	}
	var hooks map[string]any
	if err := json.Unmarshal(hooksData, &hooks); err != nil {
		return nil, fmt.Errorf("invalid hooks.json: %w", err)
	}
	hooksText := string(hooksData)
	for _, required := range []string{"SessionStart", "UserPromptSubmit", "PreCompact", "PreToolUse", "PostToolUse", "Stop"} {
		if !strings.Contains(hooksText, `"`+required+`"`) {
			return nil, fmt.Errorf("hooks.json is missing %s", required)
		}
	}
	if strings.Contains(hooksText, "SubagentStart") || strings.Contains(hooksText, "SubagentStop") || strings.Contains(hooksText, "<") || strings.Contains(hooksText, ">") {
		return nil, errors.New("hooks.json contains recursive subagent hooks or unresolved placeholders")
	}
	checks = append(checks, "hook lifecycle is complete and excludes recursive subagent triggers")

	configData, _ := json.Marshal(runtime.Config)
	lowerConfig := strings.ToLower(string(configData))
	for _, forbidden := range []string{"max_task_duration", "max_phase_duration", "max_action_duration", "total_wall_clock", "development_deadline"} {
		if strings.Contains(lowerConfig, forbidden) {
			return nil, fmt.Errorf("governance config contains forbidden development time limit %s", forbidden)
		}
	}
	if strings.Contains(runtime.Config.GovernedToolMatcher, "mcp__.*") {
		return nil, errors.New("governed tool matcher must enumerate concrete MCP tools")
	}
	checks = append(checks, "configuration contains no speculative development deadline or broad MCP wildcard")

	governorData, err := os.ReadFile(filepath.Join(runtime.Root, ".codex", "agents", runtime.Config.GovernorAgentName+".toml"))
	if err != nil {
		return nil, err
	}
	projectConfigData, err := os.ReadFile(filepath.Join(runtime.Root, ".codex", "config.toml"))
	if err != nil {
		return nil, err
	}
	if err := validateGovernorConfiguration(projectConfigData, governorData); err != nil {
		return nil, err
	}
	checks = append(checks, "Governor is independently read-only and isolates the stateful project MCP")

	fixture := *runtime
	fixture.RuntimeDir = filepath.Join(runtime.RuntimeDir, ".doctor-"+newID("fixture"))
	fixture.Config.EvidenceRoots = []string{filepath.Join(fixture.RuntimeDir, "evidence")}
	defer os.RemoveAll(fixture.RuntimeDir)
	if _, err := fixture.Init(); err != nil {
		return nil, fmt.Errorf("doctor init: %w", err)
	}
	if _, err := fixture.Init(); err == nil {
		return nil, errors.New("duplicate init unexpectedly overwrote state")
	}
	firstRequest, err := fixture.Resume("doctor-startup")
	if err != nil {
		return nil, err
	}
	secondRequest, err := fixture.Resume("doctor-startup")
	if err != nil || secondRequest.ReviewID != firstRequest.ReviewID {
		return nil, errors.New("pending fixed review was not reused on reentry")
	}
	state, err := fixture.LoadState()
	if err != nil || state.Review.FixedReviewGeneration != 1 {
		return nil, errors.New("fixed review generation is not deterministic")
	}
	for _, readInput := range []HookInput{
		{ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"Get-Content -LiteralPath '.codex/governance/runtime/review-request.json' -Raw"}`)},
		{ToolName: "Bash", ToolInput: json.RawMessage(`{"cmd":"rg -n review_id .codex/governance/runtime/review-request.json"}`)},
	} {
		readOutput := &bytes.Buffer{}
		if err := fixture.hookPreToolUse(readInput, readOutput); err != nil || strings.Contains(readOutput.String(), `"deny"`) {
			return nil, errors.New("pending review blocked a low-cost Governor read")
		}
	}
	checks = append(checks, "pending review keeps canonical low-cost Governor reads available")
	denied := &bytes.Buffer{}
	patchInput := HookInput{ToolName: "apply_patch", ToolInput: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: internal/example.go\n*** End Patch"}`)}
	if err := fixture.hookPreToolUse(patchInput, denied); err != nil || !strings.Contains(denied.String(), `"deny"`) {
		return nil, errors.New("pending review did not block product modification")
	}
	proposal := &WorkPacketProposal{PacketID: "doctor-packet", ConditionID: state.CompletionConditions[0].ConditionID, Objective: "exercise deterministic governance", Value: "prove the control plane", AllowedScope: []string{"internal"}, ExcludedScope: []string{"docs"}, ExpectedEvidence: []string{"doctor-evidence-partial", "doctor-evidence-final"}, CheckpointID: "doctor-checkpoint", CheckpointDescription: "validated fixture evidence exists"}
	result := ReviewResult{
		ReviewID: firstRequest.ReviewID, TriggerInstanceID: firstRequest.TriggerInstanceID, ReviewSnapshotHash: firstRequest.ReviewSnapshotHash,
		Decision: "start", MacroAssessment: MacroAssessment{OverallProgress: "fixture initialized", EvidenceSupport: "repository and state inspected", Unmet: []string{"doctor condition"}},
		HighestPriorityGap: stringPointer("doctor condition"), PathAssessment: PathAssessment{Necessary: true, Efficient: true, Optimal: true},
		PreservedResultIDs: []string{}, InvalidatedItems: []string{}, ValidatedEvidenceIDs: []string{}, NextWorkPacket: proposal, Reason: "approve minimal fixture packet",
	}
	if _, err := fixture.AcceptReview(result); err != nil {
		return nil, fmt.Errorf("accept valid review: %w", err)
	}
	state, err = fixture.ApplyReview()
	if err != nil || state.CurrentWorkPacket == nil || state.CurrentWorkPacket.Approval == nil {
		return nil, errors.New("valid review did not produce an approved work packet")
	}
	allowed := &bytes.Buffer{}
	if err := fixture.hookPreToolUse(patchInput, allowed); err != nil || strings.Contains(allowed.String(), `"deny"`) {
		return nil, errors.New("approved in-scope modification was not allowed")
	}
	excluded := &bytes.Buffer{}
	excludedInput := HookInput{ToolName: "apply_patch", ToolInput: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: docs/example.md\n*** End Patch"}`)}
	if err := fixture.hookPreToolUse(excludedInput, excluded); err != nil || !strings.Contains(excluded.String(), `"deny"`) {
		return nil, errors.New("excluded-scope modification was not denied")
	}
	if _, err := fixture.RecordFailure("doctor-first-failure"); err != nil {
		return nil, err
	}
	evidencePath := filepath.Join(fixture.RuntimeDir, "evidence", "doctor-partial.txt")
	if err := os.MkdirAll(filepath.Dir(evidencePath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(evidencePath, []byte("doctor partial evidence\n"), 0o600); err != nil {
		return nil, err
	}
	checkpointRequest, err := fixture.RecordEvidence(EvidenceRecord{EvidenceID: "doctor-evidence-partial", Path: evidencePath, Scope: []string{"internal"}, ValidatorStatus: "passed", ValidatorSource: "doctor fixture"})
	if err != nil || checkpointRequest != nil {
		return nil, errors.New("partial evidence unexpectedly reached the natural checkpoint")
	}
	state, err = fixture.LoadState()
	if err != nil || state.CurrentWorkPacket == nil || state.CurrentWorkPacket.LastEvidenceAt == nil || len(state.CurrentWorkPacket.FailureSignatures) != 1 {
		return nil, errors.New("fixture did not persist work-packet continuity facts")
	}
	continuityBefore := *state.CurrentWorkPacket
	continuityBefore.Approval = nil
	continuityHashBefore, err := hashJSON(continuityBefore)
	if err != nil {
		return nil, err
	}
	continueRequest, err := fixture.Resume("doctor-compact")
	if err != nil {
		return nil, err
	}
	continueResult := ReviewResult{
		ReviewID: continueRequest.ReviewID, TriggerInstanceID: continueRequest.TriggerInstanceID, ReviewSnapshotHash: continueRequest.ReviewSnapshotHash,
		Decision: "continue", MacroAssessment: MacroAssessment{OverallProgress: "fixture in progress", EvidenceSupport: "partial evidence and failure history inspected", Unmet: []string{"doctor condition"}},
		HighestPriorityGap: stringPointer("doctor condition"), PathAssessment: PathAssessment{Necessary: true, Efficient: true, Optimal: true},
		PreservedResultIDs: []string{"doctor-evidence-partial"}, InvalidatedItems: []string{}, ValidatedEvidenceIDs: []string{}, NextWorkPacket: nil, Reason: "continue the unchanged fixture packet",
	}
	if _, err := fixture.AcceptReview(continueResult); err != nil {
		return nil, err
	}
	state, err = fixture.ApplyReview()
	if err != nil || state.CurrentWorkPacket == nil || state.CurrentWorkPacket.Approval == nil {
		return nil, errors.New("continue did not restore the existing approved work packet")
	}
	continuityAfter := *state.CurrentWorkPacket
	continuityAfter.Approval = nil
	continuityHashAfter, err := hashJSON(continuityAfter)
	if err != nil || continuityHashAfter != continuityHashBefore {
		return nil, errors.New("continue changed persisted work-packet continuity facts")
	}
	checks = append(checks, "continue refreshes approval without rebuilding the existing work packet")

	interventionRequest, err := fixture.RequestReview("doctor-user-decision")
	if err != nil {
		return nil, err
	}
	interventionResult := ReviewResult{
		ReviewID: interventionRequest.ReviewID, TriggerInstanceID: interventionRequest.TriggerInstanceID, ReviewSnapshotHash: interventionRequest.ReviewSnapshotHash,
		Decision: "product_decision_required", MacroAssessment: MacroAssessment{OverallProgress: "fixture paused", EvidenceSupport: "a real product choice is required", Unmet: []string{"doctor condition"}},
		HighestPriorityGap: stringPointer("doctor decision"), PathAssessment: PathAssessment{Necessary: true, Efficient: true, Optimal: true},
		PreservedResultIDs: []string{"doctor-evidence-partial"}, InvalidatedItems: []string{}, ValidatedEvidenceIDs: []string{}, NextWorkPacket: nil,
		ExternalInput: &ExternalInput{Kind: "product_decision", Fact: "two valid product directions remain", ExhaustedPaths: []string{"authority documents do not choose between them"}, MinimumUserInput: "choose direction A or B"}, Reason: "request the minimum product decision",
	}
	if _, err := fixture.AcceptReview(interventionResult); err != nil {
		return nil, err
	}
	state, err = fixture.ApplyReview()
	if err != nil || state.PendingIntervention == nil || state.CurrentWorkPacket == nil {
		return nil, errors.New("user intervention did not preserve the suspended work packet")
	}
	pausedWrite := &bytes.Buffer{}
	if err := fixture.hookPreToolUse(patchInput, pausedWrite); err != nil || !strings.Contains(pausedWrite.String(), `"deny"`) {
		return nil, errors.New("pending user intervention did not block product modification")
	}
	allowedResolution := &bytes.Buffer{}
	resolutionCommand := HookInput{ToolName: "Bash", ToolInput: json.RawMessage(`{"cmd":".codex\\governance\\governance-hook.ps1 resolve-intervention --json-base64 fixture"}`)}
	if err := fixture.hookPreToolUse(resolutionCommand, allowedResolution); err != nil || strings.Contains(allowedResolution.String(), `"deny"`) {
		return nil, errors.New("pending user intervention blocked its exact governance resolution command")
	}
	secretMarker := "doctor-secret-must-not-persist"
	interventionOutput := &bytes.Buffer{}
	if err := fixture.hookUserPrompt(HookInput{SessionID: "doctor", TurnID: "doctor-question", Prompt: "why is this needed? " + secretMarker}, interventionOutput); err != nil || !strings.Contains(interventionOutput.String(), "resolve-intervention") || !strings.Contains(interventionOutput.String(), "doctor-question") {
		return nil, errors.New("pending intervention was not returned to the main agent")
	}
	stateData, _ := os.ReadFile(fixture.statePath())
	eventData, _ := os.ReadFile(fixture.eventsPath())
	if bytes.Contains(stateData, []byte(secretMarker)) || bytes.Contains(eventData, []byte(secretMarker)) {
		return nil, errors.New("UserPromptSubmit persisted raw user content")
	}
	state, err = fixture.LoadState()
	if err != nil || state.PendingIntervention == nil || state.PendingIntervention.Status != "awaiting_user" || state.PendingIntervention.Resolution != nil {
		return nil, errors.New("a user follow-up changed the pending intervention without explicit resolution")
	}
	if _, err := fixture.ResolveIntervention(ResolveInterventionInput{InterventionID: "stale-intervention", SourceTurnID: "doctor-answer", Summary: "choose direction A"}); err == nil {
		return nil, errors.New("stale intervention identity was accepted")
	}
	resolutionRequest, err := fixture.ResolveIntervention(ResolveInterventionInput{InterventionID: state.PendingIntervention.InterventionID, SourceTurnID: "doctor-answer", Summary: "the user selected direction A", EvidenceRefs: []string{"turn:doctor-answer"}})
	if err != nil || resolutionRequest == nil || resolutionRequest.PendingIntervention == nil || resolutionRequest.PendingIntervention.Resolution == nil {
		return nil, errors.New("valid user intervention did not create a reviewable persisted resolution")
	}
	recoveredRequest, err := fixture.Resume("doctor-resume-after-resolution")
	if err != nil || recoveredRequest == nil || recoveredRequest.PendingIntervention == nil || recoveredRequest.PendingIntervention.Resolution == nil {
		return nil, errors.New("resolved intervention was not recoverable after session resume")
	}
	recoveryResult := ReviewResult{
		ReviewID: recoveredRequest.ReviewID, TriggerInstanceID: recoveredRequest.TriggerInstanceID, ReviewSnapshotHash: recoveredRequest.ReviewSnapshotHash,
		Decision: "continue", MacroAssessment: MacroAssessment{OverallProgress: "fixture can resume", EvidenceSupport: "the bound user decision was reviewed", Unmet: []string{"doctor condition"}},
		HighestPriorityGap: stringPointer("doctor condition"), PathAssessment: PathAssessment{Necessary: true, Efficient: true, Optimal: true},
		PreservedResultIDs: []string{"doctor-evidence-partial"}, InvalidatedItems: []string{}, ValidatedEvidenceIDs: []string{}, NextWorkPacket: nil, Reason: "resume the suspended fixture packet",
	}
	if _, err := fixture.AcceptReview(recoveryResult); err != nil {
		return nil, err
	}
	state, err = fixture.ApplyReview()
	if err != nil || state.Status != "running" || state.PendingIntervention != nil || state.CurrentWorkPacket == nil {
		return nil, errors.New("reviewed intervention did not restore running governance")
	}
	resumedPacket := *state.CurrentWorkPacket
	resumedPacket.Approval = nil
	resumedHash, err := hashJSON(resumedPacket)
	if err != nil || resumedHash != continuityHashBefore {
		return nil, errors.New("intervention recovery changed the suspended work packet")
	}
	checks = append(checks, "user interventions persist, survive resume and restore the suspended packet without raw prompt storage")

	finalEvidencePath := filepath.Join(fixture.RuntimeDir, "evidence", "doctor-final.txt")
	if err := os.WriteFile(finalEvidencePath, []byte("doctor final evidence\n"), 0o600); err != nil {
		return nil, err
	}
	checkpointRequest, err = fixture.RecordEvidence(EvidenceRecord{EvidenceID: "doctor-evidence-final", Path: finalEvidencePath, Scope: []string{"internal"}, ValidatorStatus: "passed", ValidatorSource: "doctor fixture"})
	if err != nil || checkpointRequest == nil {
		return nil, errors.New("natural evidence checkpoint did not request review")
	}
	if _, err := readJSONLines(fixture.eventsPath()); err != nil {
		return nil, err
	}
	checks = append(checks, "state, fixed review, approval, scope and evidence checkpoint paths pass isolated self-test")

	inactive := *runtime
	inactive.RuntimeDir = filepath.Join(runtime.RuntimeDir, ".doctor-inactive-"+newID("fixture"))
	defer os.RemoveAll(inactive.RuntimeDir)
	output := &bytes.Buffer{}
	if err := inactive.hookUserPrompt(HookInput{Prompt: "讨论一个普通问题"}, output); err != nil || inactive.StateExists() {
		return nil, errors.New("ordinary prompt unexpectedly activated mainline governance")
	}
	activationOutput := &bytes.Buffer{}
	if err := inactive.hookUserPrompt(HookInput{SessionID: "doctor", TurnID: "activation", Prompt: "目标：持续完成 Ownward 第一版开发"}, activationOutput); err != nil || !inactive.StateExists() || !strings.Contains(activationOutput.String(), "Governor") {
		return nil, errors.New("configured mainline prompt did not activate fixed governance review")
	}
	checks = append(checks, "ordinary discussions stay inactive while the configured mainline prompt activates governance")

	return &DoctorReport{Status: "passed", Checks: checks}, nil
}

func validateGovernorConfiguration(projectConfigData, governorData []byte) error {
	governorText := strings.ToLower(string(governorData))
	if !strings.Contains(governorText, `sandbox_mode = "read-only"`) || strings.Contains(governorText, "hooks = false") {
		return errors.New("Governor must be read-only without disabling project hooks")
	}

	projectMCP := tomlSection(string(projectConfigData), "mcp_servers.ownward")
	if projectMCP == "" {
		return nil
	}
	governorMCP := tomlSection(string(governorData), "mcp_servers.ownward")
	if governorMCP == "" {
		return errors.New("Governor must explicitly isolate the project Ownward MCP")
	}
	for _, key := range []string{"command", "args", "cwd"} {
		if tomlAssignment(governorMCP, key) == "" {
			return fmt.Errorf("Governor Ownward MCP requires a complete standalone transport: missing %s", key)
		}
	}
	if !strings.EqualFold(tomlAssignment(governorMCP, "enabled"), "false") || !strings.EqualFold(tomlAssignment(governorMCP, "required"), "false") {
		return errors.New("Governor must disable and unrequire the project Ownward MCP")
	}
	return nil
}

func tomlSection(data, sectionName string) string {
	wanted := strings.ToLower(strings.TrimSpace(sectionName))
	active := false
	lines := []string{}
	for _, rawLine := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			name := strings.TrimSpace(strings.Trim(trimmed, "[]"))
			if active && !strings.EqualFold(name, wanted) {
				break
			}
			active = strings.EqualFold(name, wanted)
			continue
		}
		if active {
			lines = append(lines, rawLine)
		}
	}
	return strings.Join(lines, "\n")
}

func tomlAssignment(section, key string) string {
	wanted := strings.ToLower(strings.TrimSpace(key))
	for _, rawLine := range strings.Split(section, "\n") {
		line := strings.TrimSpace(strings.SplitN(rawLine, "#", 2)[0])
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && strings.ToLower(strings.TrimSpace(parts[0])) == wanted {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}
