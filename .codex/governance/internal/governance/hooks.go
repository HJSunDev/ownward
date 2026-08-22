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
		return runtime.hookPreCompact(writer)
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
	if !runtime.StateExists() {
		matched := false
		for _, pattern := range runtime.Config.ActivationPromptPatterns {
			expression, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("invalid activation prompt pattern: %w", err)
			}
			if expression.MatchString(input.Prompt) {
				matched = true
				break
			}
		}
		if !matched {
			return writeJSON(writer, map[string]any{})
		}
		if _, err := runtime.Init(); err != nil {
			return err
		}
		if _, err := runtime.Resume("activation:" + hookInstanceKey(input)); err != nil {
			return err
		}
	}
	state, err := runtime.LoadState()
	if err != nil {
		return err
	}
	if state.Status == "complete" {
		return writeAdditionalContext(writer, "UserPromptSubmit", "Ownward autonomous governance is complete. Do not create a second run unless the user explicitly starts a new goal.")
	}
	if state.Review.Required {
		return writeAdditionalContext(writer, "UserPromptSubmit", runtime.reviewInstruction(state))
	}
	if state.PendingIntervention != nil {
		return writeAdditionalContext(writer, "UserPromptSubmit", runtime.interventionInstruction(state, input.TurnID))
	}
	return writeAdditionalContext(writer, "UserPromptSubmit", runtime.activeInstruction(state))
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
	request, err := runtime.Resume("session-start:" + firstNonempty(input.Source, "unknown") + ":" + hookInstanceKey(input))
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

func (runtime *Runtime) hookPreCompact(writer io.Writer) error {
	if !runtime.StateExists() {
		return writeJSON(writer, map[string]any{})
	}
	if _, err := runtime.LoadState(); err != nil {
		return fmt.Errorf("governance state is not safely persisted: %w", err)
	}
	return writeJSON(writer, map[string]any{})
}

func (runtime *Runtime) hookPreToolUse(input HookInput, writer io.Writer) error {
	command := toolCommand(input)
	if isLowCostRead(input.ToolName, command) {
		return writeJSON(writer, map[string]any{})
	}
	if !runtime.StateExists() {
		return writeJSON(writer, map[string]any{})
	}
	state, err := runtime.LoadState()
	if err != nil {
		return writePreToolDecision(writer, "deny", "governance state is invalid: "+err.Error())
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
	signature := failureFromResponse(input.ToolName, input.ToolResponse)
	if signature == "" {
		return writeJSON(writer, map[string]any{})
	}
	request, err := runtime.RecordFailure(signature)
	if err != nil {
		return writeJSON(writer, map[string]any{})
	}
	if request != nil {
		return writeJSON(writer, map[string]any{"decision": "block", "reason": "Repeated failure requires Governor review. " + runtime.requestLocation()})
	}
	return writeJSON(writer, map[string]any{})
}

func (runtime *Runtime) hookStop(input HookInput, writer io.Writer) error {
	if input.StopHookActive || !runtime.StateExists() {
		return writeJSON(writer, map[string]any{})
	}
	state, err := runtime.LoadState()
	if err != nil {
		return writeJSON(writer, map[string]any{"decision": "block", "reason": "Governance state is invalid; repair it before claiming completion: " + err.Error()})
	}
	if state.Status == "complete" || state.Status == "product_decision_required" || state.Status == "external_input_required" {
		return writeJSON(writer, map[string]any{})
	}
	if !state.Review.Required {
		if _, err := runtime.RequestReview("main-agent-stop"); err != nil {
			return err
		}
		state, err = runtime.LoadState()
		if err != nil {
			return err
		}
	}
	return writeJSON(writer, map[string]any{"decision": "block", "reason": runtime.reviewInstruction(state)})
}

func (runtime *Runtime) reviewInstruction(state *State) string {
	return "Autonomous governance requires a read-only Governor review. Read " + runtime.requestLocation() +
		", spawn agent `" + runtime.Config.GovernorAgentName + "`, pass it the request path, wait for its single JSON result, base64-encode that exact JSON and run `.codex/governance/governance-hook.ps1 accept-review --json-base64 <base64>` followed by `apply-review`. Do not rewrite the result or continue product work before both commands succeed."
}

func (runtime *Runtime) activeInstruction(state *State) string {
	return "Autonomous governance is active. Read `.codex/governance/runtime/state.json`; execute only the approved work packet, record independently valid evidence immediately, and request Governor review at the registered natural checkpoint. Current next action: " + valueOr(state.NextAction, "none")
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
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{
		"rg ", "rg --files", "grep ", "get-content ", "select-string ", "get-childitem ", "test-path ", "resolve-path ",
		"git status", "git diff", "git log", "git show", "git rev-parse", "git ls-files", "go env", "go version", "go list",
	} {
		if lower == strings.TrimSpace(prefix) || strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func isExactGovernanceControl(command string) bool {
	trimmed := strings.TrimSpace(command)
	if strings.ContainsAny(trimmed, "\r\n;&|><`") || strings.Contains(trimmed, "$(") {
		return false
	}
	lower := strings.ToLower(trimmed)
	if !strings.Contains(lower, ".codex/governance/governance-hook.sh") && !strings.Contains(lower, ".codex\\governance\\governance-hook.ps1") {
		return false
	}
	for _, action := range []string{" status", " accept-review", " apply-review", " request-review", " resolve-intervention", " record-evidence", " record-failure", " propose-work-packet", " close-work-packet", " finish", " doctor"} {
		if strings.Contains(lower, action) {
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
