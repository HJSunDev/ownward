package governance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (runtime *Runtime) HandleHook(event string, reader io.Reader, writer io.Writer) error {
	var input HookInput
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode hook input: %w", err)
	}
	switch event {
	case "user-prompt-submit":
		return runtime.hookUserPrompt(input, writer)
	case "session-start":
		return runtime.hookSessionStart(input, writer)
	case "pre-compact":
		return runtime.hookPreCompact(input, writer)
	case "pre-tool-use":
		return runtime.hookPreToolUse(input, writer)
	case "post-tool-use":
		return runtime.hookPostToolUse(input, writer)
	case "stop":
		return runtime.hookStop(input, writer)
	default:
		return fmt.Errorf("unknown governance hook event %q", event)
	}
}

func (runtime *Runtime) hookUserPrompt(input HookInput, writer io.Writer) error {
	canonical, err := runtime.matchesActivationPrompt(input.Prompt)
	if err != nil {
		return err
	}
	if !runtime.StateExists() {
		if !canonical {
			return writeJSON(writer, map[string]any{})
		}
		if _, err := runtime.Init(); err != nil {
			return err
		}
		if _, err := runtime.RequestFixedReview("activation", firstNonempty(input.TurnID, hookInstanceKey(input))); err != nil {
			return err
		}
	}
	consumed, err := runtime.consumeHandoff(input)
	if err != nil {
		return writeAdditionalContext(writer, "UserPromptSubmit", "Governance handoff was rejected: "+err.Error())
	} else if consumed {
		// Continue below with the new owner and the persisted work packet.
	}
	if !canonical && !consumed {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.PendingIntervention == nil {
			return writeJSON(writer, map[string]any{})
		}
	}
	owned, err := runtime.ensureHookOwner(input)
	if err != nil {
		return err
	}
	if !owned {
		return writeJSON(writer, map[string]any{})
	}
	state, err := runtime.LoadState()
	if err != nil {
		return err
	}
	if state.Handoff != nil {
		return writeJSON(writer, map[string]any{})
	}
	if latched, err := runtime.reconcileInfrastructureFailure(input); err != nil {
		return err
	} else if latched {
		return writeJSON(writer, map[string]any{})
	}
	state, err = runtime.LoadState()
	if err != nil {
		return err
	}
	if state.Status == "complete" {
		return writeJSON(writer, map[string]any{})
	}
	if state.PendingIntervention != nil {
		return writeAdditionalContext(writer, "UserPromptSubmit", runtime.interventionInstruction(state, input.TurnID))
	}
	if !canonical {
		return writeJSON(writer, map[string]any{})
	}
	if state.Review.Required {
		return writeAdditionalContext(writer, "UserPromptSubmit", runtime.reviewInstruction(state))
	}
	request, err := runtime.RequestFixedReview("explicit-resume", firstNonempty(input.TurnID, hookInstanceKey(input)))
	if err != nil {
		return err
	}
	if request == nil {
		return writeJSON(writer, map[string]any{})
	}
	state, err = runtime.LoadState()
	if err != nil {
		return err
	}
	return writeAdditionalContext(writer, "UserPromptSubmit", runtime.reviewInstruction(state))
}

func (runtime *Runtime) matchesActivationPrompt(prompt string) (bool, error) {
	for _, pattern := range runtime.Config.ActivationPromptPatterns {
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return false, fmt.Errorf("invalid activation prompt pattern: %w", err)
		}
		if expression.MatchString(prompt) {
			return true, nil
		}
	}
	return false, nil
}

func (runtime *Runtime) hookSessionStart(input HookInput, writer io.Writer) error {
	if !runtime.StateExists() {
		return writeJSON(writer, map[string]any{})
	}
	state, err := runtime.LoadState()
	if err != nil {
		return err
	}
	if state.Status == "complete" {
		return writeAdditionalContext(writer, "SessionStart", "Ownward autonomous governance already records this goal as complete; do not overwrite its state.")
	}
	owned, err := runtime.ensureHookOwner(input)
	if err != nil {
		return err
	}
	if !owned {
		return writeAdditionalContext(writer, "SessionStart", "Another task owns this governance run. This task is read-only; use the explicit one-time handoff flow to continue it here.")
	}
	state, err = runtime.LoadState()
	if err != nil {
		return err
	}
	if state.Handoff != nil {
		return writeAdditionalContext(writer, "SessionStart", runtime.handoffInstruction(state))
	}
	if latched, err := runtime.reconcileInfrastructureFailure(input); err != nil {
		return err
	} else if latched {
		state, _ = runtime.LoadState()
		return writeAdditionalContext(writer, "SessionStart", runtime.infrastructureInstruction(state))
	}
	if state.Review.Required {
		return writeAdditionalContext(writer, "SessionStart", runtime.reviewInstruction(state))
	}
	if state.PendingIntervention != nil {
		return writeAdditionalContext(writer, "SessionStart", runtime.interventionInstruction(state, ""))
	}
	request, err := runtime.RequestFixedReview("session-start", firstNonempty(input.Source, "unknown")+":"+hookInstanceKey(input))
	if err != nil {
		return err
	}
	if request == nil {
		state, err = runtime.LoadState()
		if err != nil {
			return err
		}
		if state.PendingIntervention != nil {
			return writeAdditionalContext(writer, "SessionStart", runtime.interventionInstruction(state, ""))
		}
		return writeJSON(writer, map[string]any{})
	}
	state, err = runtime.LoadState()
	if err != nil {
		return err
	}
	return writeAdditionalContext(writer, "SessionStart", runtime.reviewInstruction(state))
}

func (runtime *Runtime) hookPreCompact(input HookInput, writer io.Writer) error {
	if !runtime.StateExists() {
		return writeJSON(writer, map[string]any{})
	}
	if _, err := runtime.LoadState(); err != nil {
		return fmt.Errorf("governance state is not safely persisted: %w", err)
	}
	if owned, err := runtime.ensureHookOwner(input); err != nil {
		return err
	} else if !owned {
		return writeJSON(writer, map[string]any{})
	}
	return writeJSON(writer, map[string]any{})
}

func (runtime *Runtime) hookPreToolUse(input HookInput, writer io.Writer) error {
	command := toolCommand(input)
	if isLowCostRead(input.ToolName, command) {
		return writeJSON(writer, map[string]any{})
	}
	if role, isAgent, err := requestedAgentRole(input); isAgent {
		if err != nil {
			return writePreToolDecision(writer, "deny", "cannot determine requested subagent role: "+err.Error())
		}
		if _, exists := runtime.agentCapability(role); !exists {
			return writePreToolDecision(writer, "deny", "subagent role is absent from the explicit capability matrix: "+role)
		}
	}
	if !runtime.StateExists() {
		return writeJSON(writer, map[string]any{})
	}
	state, err := runtime.LoadState()
	if err != nil {
		return writePreToolDecision(writer, "deny", "governance state is invalid: "+err.Error())
	}
	owned, ownerErr := runtime.ensureHookOwner(input)
	if ownerErr != nil {
		return writePreToolDecision(writer, "deny", "cannot verify governance owner: "+ownerErr.Error())
	}
	if !owned {
		return writePreToolDecision(writer, "deny", "this task is not the active governance owner")
	}
	if state.Handoff != nil {
		if isExactPendingHandoffControl(command) {
			return writeJSON(writer, map[string]any{})
		}
		return writePreToolDecision(writer, "deny", "governance handoff is pending; product work is frozen until the source owner binds or cancels it")
	}
	if state.InfrastructureFailure != nil {
		if isExactInfrastructureRecoveryControl(command) || runtime.isRepairStagingChange(changedPaths(input)) {
			return writeJSON(writer, map[string]any{})
		}
		return writePreToolDecision(writer, "deny", "Governor infrastructure is latched; only bounded control-plane repair, status and task handoff are allowed")
	}
	if isExactGovernanceControl(command) {
		return writeJSON(writer, map[string]any{})
	}
	if state.Status != "running" {
		return writePreToolDecision(writer, "deny", "governance is paused: "+valueOr(state.NextAction, state.Status))
	}
	if state.Review.Required {
		return writePreToolDecision(writer, "deny", "Governor review is required before product work: "+valueOr(state.NextAction, "run the review chain"))
	}
	currentAuthority, authorityErr := runtime.authorityHash()
	if authorityErr != nil {
		return writePreToolDecision(writer, "deny", "cannot verify governed authority: "+authorityErr.Error())
	}
	if currentAuthority != state.AuthorityHash {
		_, _ = runtime.ReconcileAuthority()
		return writePreToolDecision(writer, "deny", "governance authority changed; a new Governor review is required")
	}
	packet := state.CurrentWorkPacket
	if packet == nil || packet.Approval == nil || packet.Approval.Status != "approved" {
		return writePreToolDecision(writer, "deny", "product work requires a Governor-approved work packet")
	}
	if packet.Approval.ValidUntilCheckpoint != packet.EvidenceCheckpoint.CheckpointID || packet.EvidenceCheckpoint.Reached {
		return writePreToolDecision(writer, "deny", "work packet approval expired at its evidence checkpoint")
	}
	paths := changedPaths(input)
	for _, path := range paths {
		if !within(runtime.Root, resolvePath(runtime.Root, path)) {
			return writePreToolDecision(writer, "deny", "tool change escapes the Ownward repository: "+path)
		}
		if matchesAnyScope(path, packet.ExcludedScope) {
			return writePreToolDecision(writer, "deny", "tool change enters excluded work-packet scope: "+path)
		}
		if len(packet.AllowedScope) > 0 && !matchesAnyScope(path, packet.AllowedScope) {
			return writePreToolDecision(writer, "deny", "tool change is outside approved work-packet scope: "+path)
		}
	}
	if strings.EqualFold(input.ToolName, "Bash") && commandMentionsExcludedScope(command, packet.ExcludedScope) {
		return writePreToolDecision(writer, "deny", "shell command references excluded work-packet scope")
	}
	return writeJSON(writer, map[string]any{})
}

func (runtime *Runtime) hookPostToolUse(input HookInput, writer io.Writer) error {
	if !runtime.StateExists() || isLowCostRead(input.ToolName, toolCommand(input)) || !toolFailed(input.ToolResponse) {
		return writeJSON(writer, map[string]any{})
	}
	if owned, err := runtime.ensureHookOwner(input); err != nil || !owned {
		return writeJSON(writer, map[string]any{})
	}
	signature := failureFromResponse(input.ToolName, input.ToolResponse)
	if signature == "" {
		return writeJSON(writer, map[string]any{})
	}
	if isGovernorReviewChainAttempt(runtime, input) {
		if err := runtime.latchInfrastructureFailure(signature); err != nil {
			return err
		}
		return writeJSON(writer, map[string]any{"decision": "block", "reason": "Governor infrastructure failed once and is now latched. Product writes remain closed; automatic retries and Stop continuation are disabled for this runtime identity."})
	}
	if strings.TrimSpace(input.ToolUseID) == "" {
		return writeJSON(writer, map[string]any{})
	}
	evidenceHash := sha256Value(bytes.TrimSpace(input.ToolResponse))
	sourceExecution := strings.TrimSpace(input.SessionID) + ":" + strings.TrimSpace(input.TurnID)
	request, err := runtime.RecordFailureEvent(FailureEventInput{
		Signature: signature, SourceKind: "codex_hook", SourceExecution: sourceExecution,
		ToolUseID: input.ToolUseID, EvidenceHash: evidenceHash,
	})
	if err != nil {
		escalationErr := runtime.ensureFailureRecordingReview(firstNonempty(input.ToolUseID, hookInstanceKey(input)))
		reason := "A real tool failure could not be recorded as a verified governance event; product work is blocked for an integrity review: " + err.Error()
		if escalationErr != nil {
			reason += "; the review request also failed: " + escalationErr.Error()
		}
		return writeJSON(writer, map[string]any{"decision": "block", "reason": reason})
	}
	if request != nil {
		return writeJSON(writer, map[string]any{"decision": "block", "reason": "Repeated failure requires Governor review. " + runtime.requestLocation()})
	}
	return writeJSON(writer, map[string]any{})
}

func (runtime *Runtime) hookStop(input HookInput, writer io.Writer) error {
	return writeJSON(writer, map[string]any{})
}

func isGovernorAgentAttempt(runtime *Runtime, input HookInput) bool {
	role, isAgent, err := requestedAgentRole(input)
	return isAgent && err == nil && strings.EqualFold(role, runtime.Config.GovernorAgentName)
}

func isGovernorReviewChainAttempt(runtime *Runtime, input HookInput) bool {
	if isGovernorAgentAttempt(runtime, input) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(input.ToolName), "Bash") {
		return false
	}
	action, ok := governanceControlAction(toolCommand(input))
	return ok && (action == "accept-review" || action == "apply-review")
}

func requestedAgentRole(input HookInput) (string, bool, error) {
	name := strings.ToLower(strings.TrimSpace(input.ToolName))
	if name != "agent" && name != "spawn_agent" {
		return "", false, nil
	}
	var object map[string]any
	if err := json.Unmarshal(input.ToolInput, &object); err != nil {
		return "", true, err
	}
	for _, key := range []string{"agent_type", "role", "name"} {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), true, nil
		}
	}
	// Codex uses the project `default` Agent when no explicit role override is
	// supplied. Keep that standard path usable, while every non-default role
	// must name itself and exist in the capability matrix.
	return "default", true, nil
}

func (runtime *Runtime) agentCapability(role string) (AgentCapability, bool) {
	for _, capability := range runtime.Config.AgentCapabilities {
		if strings.EqualFold(strings.TrimSpace(capability.Role), strings.TrimSpace(role)) {
			return capability, true
		}
	}
	return AgentCapability{}, false
}

func (runtime *Runtime) reviewInstruction(state *State) string {
	return "Autonomous governance requires a read-only Governor review. Read " + runtime.requestLocation() +
		", spawn agent `" + runtime.Config.GovernorAgentName + "`, pass it the request path, wait for its single JSON result, base64-encode that exact JSON and run `.codex/governance/governance-hook.ps1 accept-review --json-base64 <base64>` followed by `apply-review`. Do not rewrite the result or continue product work before both commands succeed."
}

func (runtime *Runtime) interventionInstruction(state *State, sourceTurnID string) string {
	pending := state.PendingIntervention
	if pending == nil {
		return "Autonomous governance is paused without a valid pending intervention; inspect and repair the governance state before product work."
	}
	turnInstruction := "No user turn is being submitted during this resume; wait for the next UserPromptSubmit before resolving it."
	if strings.TrimSpace(sourceTurnID) != "" {
		turnInstruction = "The current source_turn_id is `" + sourceTurnID + "`."
	}
	return "Autonomous governance is awaiting user input for intervention `" + pending.InterventionID + "`: " + pending.MinimumUserInput +
		" Determine whether the current user message answers that exact intervention. If it is only a question or unrelated instruction, answer without changing governance state. If it resolves the intervention, submit JSON with intervention_id, source_turn_id, summary and evidence_refs through `.codex/governance/governance-hook.ps1 resolve-intervention`; use an accurate safe summary and never persist raw credentials or secrets. " + turnInstruction +
		" Then spawn and wait for the Governor using the generated review request. Product work remains blocked until the reviewed resolution is applied."
}

func (runtime *Runtime) handoffInstruction(state *State) string {
	if state.Handoff == nil {
		return "Autonomous governance has no pending handoff."
	}
	return "Governance handoff `" + state.Handoff.HandoffID + "` is pending. Product work is frozen. The source owner may only inspect status, bind the returned target task, or cancel the handoff; the target becomes writable only after its bound one-time token is consumed."
}

func (runtime *Runtime) requestLocation() string {
	return filepath.ToSlash(mustRelative(runtime.Root, runtime.requestPath()))
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeAdditionalContext(writer io.Writer, event, context string) error {
	return writeJSON(writer, map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": context}})
}

func writePreToolDecision(writer io.Writer, decision, reason string) error {
	return writeJSON(writer, map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": decision, "permissionDecisionReason": reason}})
}

func toolCommand(input HookInput) string {
	if len(input.ToolInput) == 0 {
		return ""
	}
	var object map[string]any
	if json.Unmarshal(input.ToolInput, &object) != nil {
		return ""
	}
	for _, key := range []string{"cmd", "command", "input", "patch"} {
		if value, ok := object[key].(string); ok {
			return value
		}
	}
	return ""
}

func isLowCostRead(toolName, command string) bool {
	if !strings.EqualFold(toolName, "Bash") {
		return false
	}
	trimmed := strings.TrimSpace(command)
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n;&|><`") || strings.Contains(trimmed, "$(") {
		return false
	}
	fields := strings.Fields(strings.ToLower(trimmed))
	if len(fields) == 0 {
		return false
	}
	for index := range fields {
		fields[index] = strings.Trim(fields[index], "\"'")
	}
	switch fields[0] {
	case "rg", "rg.exe":
		return !containsCommandFlag(fields[1:], "--pre", "--hostname-bin")
	case "grep", "grep.exe", "get-content", "select-string", "get-childitem", "test-path", "resolve-path":
		return true
	case "git", "git.exe":
		if len(fields) < 2 || !stringIn(fields[1], []string{"status", "diff", "log", "show", "rev-parse", "ls-files"}) {
			return false
		}
		return !containsCommandFlag(fields[2:], "--output", "--ext-diff", "--textconv")
	case "go", "go.exe":
		if len(fields) < 2 {
			return false
		}
		if fields[1] == "version" {
			return true
		}
		return fields[1] == "env" && !containsCommandFlag(fields[2:], "-w", "-u")
	}
	return false
}

func containsCommandFlag(arguments []string, names ...string) bool {
	for _, argument := range arguments {
		for _, name := range names {
			if argument == name || strings.HasPrefix(argument, name+"=") {
				return true
			}
		}
	}
	return false
}

func isExactGovernanceControl(command string) bool {
	action, ok := governanceControlAction(command)
	return ok && stringIn(action, []string{"status", "accept-review", "apply-review", "request-advisory-review", "request-completion-review", "migrate-invalid-stop-review", "resolve-intervention", "record-evidence", "record-repair", "propose-work-packet", "close-work-packet", "finish", "doctor", "prepare-handoff", "bind-handoff", "cancel-handoff", "repair-stage", "repair-apply"})
}

func isExactInfrastructureRecoveryControl(command string) bool {
	action, ok := governanceControlAction(command)
	return ok && stringIn(action, []string{"status", "doctor", "prepare-handoff", "bind-handoff", "cancel-handoff", "repair-stage", "repair-apply"})
}

func isExactPendingHandoffControl(command string) bool {
	action, ok := governanceControlAction(command)
	return ok && stringIn(action, []string{"status", "bind-handoff", "cancel-handoff"})
}

func governanceControlAction(command string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	if strings.ContainsAny(trimmed, "\r\n;&|><`") || strings.Contains(trimmed, "$(") {
		return "", false
	}
	normalized := strings.ToLower(strings.ReplaceAll(trimmed, "\\", "/"))
	fields := strings.Fields(normalized)
	if len(fields) < 2 {
		return "", false
	}
	for index := range fields {
		fields[index] = strings.Trim(fields[index], "\"'")
	}
	scriptIndex := 0
	if fields[0] == "sh" {
		scriptIndex = 1
	}
	if len(fields) <= scriptIndex+1 || !isGovernanceHookScript(fields[scriptIndex]) {
		return "", false
	}
	return strings.Trim(fields[scriptIndex+1], "\"'"), true
}

func isGovernanceHookScript(value string) bool {
	value = strings.TrimPrefix(value, "./")
	return value == ".codex/governance/governance-hook.sh" || value == ".codex/governance/governance-hook.ps1"
}

func stringIn(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func changedPaths(input HookInput) []string {
	if !strings.EqualFold(input.ToolName, "apply_patch") && !strings.EqualFold(input.ToolName, "Edit") && !strings.EqualFold(input.ToolName, "Write") {
		return nil
	}
	command := toolCommand(input)
	var paths []string
	for _, line := range strings.Split(command, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Add File:", "*** Update File:", "*** Delete File:"} {
			if strings.HasPrefix(line, prefix) {
				paths = append(paths, strings.TrimSpace(strings.TrimPrefix(line, prefix)))
			}
		}
	}
	if len(paths) == 0 && len(input.ToolInput) > 0 {
		var object map[string]any
		if json.Unmarshal(input.ToolInput, &object) == nil {
			for _, key := range []string{"path", "file_path"} {
				if path, ok := object[key].(string); ok {
					paths = append(paths, path)
				}
			}
		}
	}
	return normalizeStrings(paths)
}

func matchesAnyScope(path string, scopes []string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	for _, rawScope := range scopes {
		scope := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(rawScope)), "./")
		if scope == "." || scope == "" {
			return true
		}
		if matched, _ := filepath.Match(filepath.FromSlash(scope), filepath.FromSlash(path)); matched {
			return true
		}
		scope = strings.TrimSuffix(scope, "/")
		if path == scope || strings.HasPrefix(path, scope+"/") {
			return true
		}
	}
	return false
}

func commandMentionsExcludedScope(command string, scopes []string) bool {
	normalized := filepath.ToSlash(strings.ToLower(command))
	for _, scope := range scopes {
		scope = strings.TrimPrefix(filepath.ToSlash(strings.ToLower(filepath.Clean(scope))), "./")
		if scope != "" && scope != "." && strings.Contains(normalized, scope) {
			return true
		}
	}
	return false
}

func toolFailed(response json.RawMessage) bool {
	if len(response) == 0 {
		return false
	}
	var object map[string]any
	if json.Unmarshal(response, &object) == nil {
		if failed, ok := object["isError"].(bool); ok && failed {
			return true
		}
		if code, ok := numericValue(object["exit_code"]); ok && code != 0 {
			return true
		}
		if success, ok := object["success"].(bool); ok && !success {
			return true
		}
	}
	lower := bytes.ToLower(response)
	return bytes.Contains(lower, []byte(`"iserror":true`)) || bytes.Contains(lower, []byte(`"exit_code":1`))
}

func numericValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func failureFromResponse(toolName string, response json.RawMessage) string {
	text := strings.TrimSpace(string(response))
	if len(text) > 600 {
		text = text[:600]
	}
	return normalizeFailureSignature(toolName + ":" + text)
}

func valueOr(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return *value
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func hookInstanceKey(input HookInput) string {
	identity := map[string]any{
		"session_id": input.SessionID, "source": input.Source, "turn_id": input.TurnID,
		"transcript_path": input.TranscriptPath, "hook_event_name": input.HookEventName,
	}
	if input.TranscriptPath != "" {
		if info, err := os.Stat(input.TranscriptPath); err == nil {
			identity["transcript_size"] = info.Size()
			identity["transcript_modified"] = info.ModTime().UTC().Format(time.RFC3339Nano)
		}
	}
	hash, err := hashJSON(identity)
	if err != nil {
		return "unknown"
	}
	return strings.TrimPrefix(hash, "sha256:")[:24]
}

func loadHookInput(path string) (HookInput, error) {
	var input HookInput
	file, err := os.Open(path)
	if err != nil {
		return input, err
	}
	defer file.Close()
	err = json.NewDecoder(file).Decode(&input)
	return input, err
}
