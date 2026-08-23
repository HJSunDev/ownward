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
	for _, required := range []string{"SessionStart", "UserPromptSubmit", "PreCompact", "PreToolUse", "PostToolUse"} {
		if !strings.Contains(hooksText, `"`+required+`"`) {
			return nil, fmt.Errorf("hooks.json is missing %s", required)
		}
	}
	if strings.Contains(hooksText, `"Stop"`) {
		return nil, errors.New("hooks.json must not register Stop as a governance event")
	}
	if strings.Contains(hooksText, "Activating autonomous governance") {
		return nil, errors.New("ordinary UserPromptSubmit must not display a governance activation status")
	}
	templateData, err := os.ReadFile(filepath.Join(runtime.Root, ".agents", "skills", "autonomous-development-governance", "assets", "governance-runtime", "hooks.template.json"))
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(templateData), `"Stop"`) || strings.Contains(string(templateData), "Activating mainline governance") {
		return nil, errors.New("reusable hook template still installs intrusive Stop or UserPrompt activation behavior")
	}
	if strings.Contains(hooksText, "SubagentStart") || strings.Contains(hooksText, "SubagentStop") || strings.Contains(hooksText, "<") || strings.Contains(hooksText, ">") {
		return nil, errors.New("hooks.json contains recursive subagent hooks or unresolved placeholders")
	}
	checks = append(checks, "hook lifecycle excludes Stop and recursive subagent triggers")

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
	if err := validateAgentCapabilityMatrix(runtime.Root, runtime.Config, projectConfigData, hooksText); err != nil {
		return nil, err
	}
	checks = append(checks, "every allowed subagent role has an explicit fail-closed product MCP capability")

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
	firstRequest, err := fixture.RequestFixedReview("session-start", "doctor-startup")
	if err != nil {
		return nil, err
	}
	secondRequest, err := fixture.RequestFixedReview("session-start", "doctor-startup")
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
	failureEventInput := FailureEventInput{
		Signature: "doctor-first-failure", SourceKind: "codex_hook", SourceExecution: "doctor:turn-1",
		ToolUseID: "doctor-tool-1", EvidenceHash: sha256Value([]byte("doctor failure evidence")),
	}
	if _, err := fixture.RecordFailureEvent(failureEventInput); err != nil {
		return nil, err
	}
	if duplicate, err := fixture.RecordFailureEvent(failureEventInput); err != nil || duplicate != nil {
		return nil, errors.New("duplicate failure event was not idempotent")
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
	if err != nil || state.CurrentWorkPacket == nil || state.CurrentWorkPacket.LastEvidenceAt == nil || len(state.CurrentWorkPacket.FailureEvents) != 1 || len(state.CurrentWorkPacket.FailureSignatures) != 0 {
		return nil, errors.New("fixture did not persist work-packet continuity facts")
	}
	checks = append(checks, "verified failure events are identity-bound and duplicate delivery is idempotent")
	continuityBefore := *state.CurrentWorkPacket
	continuityBefore.Approval = nil
	continuityHashBefore, err := hashJSON(continuityBefore)
	if err != nil {
		return nil, err
	}
	continueRequest, err := fixture.RequestFixedReview("session-start", "doctor-compact")
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

	interventionRequest, err := fixture.RequestAdvisoryReview("doctor-user-decision", "doctor user decision")
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
	recoveredRequest, err := fixture.RequestFixedReview("session-start", "doctor-resume-after-resolution")
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
	silentBefore, err := runtimeControlSnapshot(&inactive)
	if err != nil {
		return nil, err
	}
	ordinaryOutput := &bytes.Buffer{}
	if err := inactive.hookUserPrompt(HookInput{SessionID: "doctor", TurnID: "ordinary", Prompt: "只回答当前问题"}, ordinaryOutput); err != nil || strings.TrimSpace(ordinaryOutput.String()) != "{}" {
		return nil, errors.New("ordinary active-run prompt was not silent")
	}
	stopOutput := &bytes.Buffer{}
	if err := inactive.hookStop(HookInput{SessionID: "doctor"}, stopOutput); err != nil || strings.TrimSpace(stopOutput.String()) != "{}" {
		return nil, errors.New("compatibility Stop was not a strict no-op")
	}
	silentAfter, err := runtimeControlSnapshot(&inactive)
	if err != nil || silentBefore != silentAfter {
		return nil, errors.New("ordinary prompt or compatibility Stop changed governance state")
	}
	checks = append(checks, "ordinary active-run prompts and compatibility Stop are silent and side-effect free")
	ownerBefore, err := inactive.LoadState()
	if err != nil || ownerBefore.Owner == nil || ownerBefore.Owner.SessionID != "doctor" {
		return nil, errors.New("activation did not bind a single active governance owner")
	}
	competitorOutput := &bytes.Buffer{}
	if err := inactive.hookSessionStart(HookInput{SessionID: "doctor-competitor", Source: "startup"}, competitorOutput); err != nil {
		return nil, err
	}
	if !strings.Contains(competitorOutput.String(), "read-only") {
		return nil, errors.New("non-owner SessionStart did not provide its one-time read-only fact")
	}
	nonOwnerPrompt := &bytes.Buffer{}
	if err := inactive.hookUserPrompt(HookInput{SessionID: "doctor-competitor", Prompt: "普通问题"}, nonOwnerPrompt); err != nil || strings.TrimSpace(nonOwnerPrompt.String()) != "{}" {
		return nil, errors.New("non-owner ordinary prompt repeated governance instructions")
	}
	ownerAfter, _ := inactive.LoadState()
	if ownerAfter.Owner.SessionID != "doctor" || ownerAfter.Review.FixedReviewGeneration != ownerBefore.Review.FixedReviewGeneration {
		return nil, errors.New("a competing task changed governance ownership or review generation")
	}
	ticket, err := inactive.PrepareHandoff("doctor")
	if err != nil {
		return nil, err
	}
	if err := inactive.BindHandoff(ticket.HandoffID, "doctor-successor"); err != nil {
		return nil, err
	}
	handoffOutput := &bytes.Buffer{}
	handoffPrompt := "continue [ownward-governance-handoff id=" + ticket.HandoffID + " token=" + ticket.Token + "]"
	if err := inactive.hookUserPrompt(HookInput{SessionID: "doctor-wrong-target", Prompt: handoffPrompt}, &bytes.Buffer{}); err != nil {
		return nil, err
	}
	stillOwned, _ := inactive.LoadState()
	if stillOwned.Owner.SessionID != "doctor" || stillOwned.Handoff == nil {
		return nil, errors.New("a task other than the bound handoff target acquired governance ownership")
	}
	if err := inactive.hookUserPrompt(HookInput{SessionID: "doctor-successor", Prompt: handoffPrompt}, handoffOutput); err != nil {
		return nil, err
	}
	handedOff, _ := inactive.LoadState()
	if handedOff.Owner.SessionID != "doctor-successor" || handedOff.Owner.OwnerEpoch != 2 || handedOff.Handoff != nil {
		return nil, errors.New("one-time governance handoff did not transfer ownership")
	}
	checks = append(checks, "task ownership is single-writer and transfers through a bound one-time handoff")

	failureFixture := *runtime
	failureFixture.RuntimeDir = filepath.Join(runtime.RuntimeDir, ".doctor-failure-"+newID("fixture"))
	failureFixture.Config.EvidenceRoots = []string{filepath.Join(failureFixture.RuntimeDir, "evidence")}
	defer os.RemoveAll(failureFixture.RuntimeDir)
	if _, err := failureFixture.Init(); err != nil {
		return nil, err
	}
	if err := failureFixture.hookSessionStart(HookInput{SessionID: "failure-owner", Source: "startup"}, &bytes.Buffer{}); err != nil {
		return nil, err
	}
	failureInput := HookInput{SessionID: "failure-owner", ToolName: "Agent", ToolInput: json.RawMessage(`{"name":"governor"}`), ToolResponse: json.RawMessage(`{"isError":true,"message":"injected Governor startup failure"}`)}
	if err := failureFixture.hookPostToolUse(failureInput, &bytes.Buffer{}); err != nil {
		return nil, err
	}
	failureStopOutput := &bytes.Buffer{}
	if err := failureFixture.hookStop(HookInput{SessionID: "failure-owner"}, failureStopOutput); err != nil || strings.Contains(failureStopOutput.String(), `"decision":"block"`) {
		return nil, errors.New("latched Governor infrastructure failure still caused a Stop retry loop")
	}
	failureState, _ := failureFixture.LoadState()
	if failureState.InfrastructureFailure == nil || failureState.CurrentWorkPacket != nil {
		return nil, errors.New("Governor failure latch did not preserve the closed control-plane state")
	}
	checks = append(checks, "Governor infrastructure failures latch once, keep product writes closed and allow the task to end")

	return &DoctorReport{Status: "passed", Checks: checks}, nil
}

func runtimeControlSnapshot(runtime *Runtime) (string, error) {
	values := map[string]string{}
	for _, path := range []string{runtime.statePath(), runtime.requestPath(), runtime.eventsPath()} {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		values[path] = sha256Value(data)
	}
	return hashJSON(values)
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

func validateAgentCapabilityMatrix(root string, config Config, projectConfigData []byte, hooksText string) error {
	projectMCP := tomlSection(string(projectConfigData), "mcp_servers.ownward")
	for _, key := range []string{"command", "args", "cwd"} {
		if tomlAssignment(projectMCP, key) == "" {
			return fmt.Errorf("project Ownward MCP transport is incomplete: missing %s", key)
		}
	}
	if !strings.Contains(hooksText, "Agent") {
		return errors.New("Agent lifecycle is absent from governance tool hooks")
	}
	expected := map[string]string{}
	for _, capability := range config.AgentCapabilities {
		expected[capability.Role] = capability.ProductMCP
		path := filepath.Join(root, ".codex", "agents", capability.Role+".toml")
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("agent capability %q has no project configuration: %w", capability.Role, err)
		}
		section := tomlSection(string(data), "mcp_servers.ownward")
		if section == "" {
			return fmt.Errorf("agent %q does not explicitly declare Ownward MCP capability", capability.Role)
		}
		if capability.ProductMCP == "disabled" {
			for _, key := range []string{"command", "args", "cwd"} {
				if tomlAssignment(section, key) == "" {
					return fmt.Errorf("agent %q has a partial disabled Ownward transport: missing %s", capability.Role, key)
				}
			}
			if !strings.EqualFold(tomlAssignment(section, "enabled"), "false") || !strings.EqualFold(tomlAssignment(section, "required"), "false") {
				return fmt.Errorf("agent %q must explicitly disable and unrequire Ownward MCP", capability.Role)
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, ".codex", "agents"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".toml") {
			continue
		}
		role := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if _, exists := expected[role]; !exists {
			return fmt.Errorf("project agent role %q is absent from the capability matrix", role)
		}
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
