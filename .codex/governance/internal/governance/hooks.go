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

var (
	failureTimestampPattern  = regexp.MustCompile(`(?i)\b\d{4}-\d{2}-\d{2}[t ][0-9:.+-]+z?\b`)
	failureUUIDPattern       = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	failureHashPattern       = regexp.MustCompile(`(?i)\b[0-9a-f]{32,}\b`)
	failureDurationPattern   = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?\s*(?:ns|us|µs|ms|milliseconds?|s|sec(?:onds?)?|m|min(?:utes?)?|h|hours?)\b`)
	failureLongNumberPattern = regexp.MustCompile(`\b\d{4,}\b`)
	failureWhitespacePattern = regexp.MustCompile(`\s+`)
)

// HandleHook is deliberately fail-open for governance. A broken advisor must
// never prevent the main Agent from using Codex or modifying the product.
func (runtime *Runtime) HandleHook(event string, reader io.Reader, writer io.Writer) error {
	var input HookInput
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		runtime.recordHookDiagnostic(event, err)
		return writeJSON(writer, map[string]any{})
	}
	var err error
	switch event {
	case "user-prompt-submit":
		err = runtime.hookUserPrompt(input, writer)
	case "session-start":
		err = runtime.hookSessionStart(input, writer)
	case "pre-compact":
		err = runtime.hookPreCompact(input, writer)
	case "post-compact":
		// Compatibility for configurations loaded before compact recovery moved
		// to SessionStart(source=compact), the only compact boundary whose
		// additionalContext is delivered to the resumed model.
		err = writeJSON(writer, map[string]any{})
	case "post-tool-use":
		err = runtime.hookPostToolUse(input, writer)
	case "pre-tool-use", "stop":
		// Compatibility for already-loaded old Hook definitions. These events
		// are not registered by the advisory runtime and can never deny work.
		err = writeJSON(writer, map[string]any{})
	default:
		err = fmt.Errorf("unknown governance hook event %q", event)
	}
	if err != nil {
		runtime.recordHookDiagnostic(event, err)
		return writeJSON(writer, map[string]any{})
	}
	return nil
}

func (runtime *Runtime) hookUserPrompt(input HookInput, writer io.Writer) error {
	activation := runtime.matchesActivationPrompt(input.Prompt)
	if !runtime.StateExists() {
		if !activation {
			return writeJSON(writer, map[string]any{})
		}
		if _, err := runtime.Init(); err != nil {
			return err
		}
		if _, err := runtime.ensureHookOwner(input); err != nil {
			return err
		}
		sourceID := activationSourceID(input.Prompt)
		if _, err := runtime.rememberActivationSource(sourceID); err != nil {
			return err
		}
		request, err := runtime.RequestFixedReview("activation", sourceID)
		if err != nil {
			return err
		}
		if request == nil {
			return writeJSON(writer, map[string]any{})
		}
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		return writeAdditionalContext(writer, "UserPromptSubmit", runtime.reviewInstruction(state))
	}

	consumed, err := runtime.consumeHandoff(input)
	if err != nil {
		return err
	}
	if !activation && !consumed {
		// Ordinary user messages are never governance events, even when a
		// review or intervention exists in persisted state.
		return writeJSON(writer, map[string]any{})
	}
	owned, err := runtime.ensureHookOwner(input)
	if err != nil || !owned {
		return writeJSON(writer, map[string]any{})
	}
	state, err := runtime.LoadState()
	if err != nil || state.Status == "complete" {
		return writeJSON(writer, map[string]any{})
	}
	sourceID := activationSourceID(input.Prompt)
	triggerType := "activation"
	if consumed {
		triggerType = "session-start"
		sourceID = "handoff:" + firstNonempty(input.SessionID, sourceID)
	} else {
		hadActivationIdentity := state.ActivationSourceID != nil
		changed, err := runtime.rememberActivationSource(sourceID)
		if err != nil || !changed || !hadActivationIdentity {
			return writeJSON(writer, map[string]any{})
		}
	}
	request, err := runtime.RequestFixedReview(triggerType, sourceID)
	if err != nil || request == nil {
		return writeJSON(writer, map[string]any{})
	}
	state, err = runtime.LoadState()
	if err != nil {
		return err
	}
	return writeAdditionalContext(writer, "UserPromptSubmit", runtime.reviewInstruction(state))
}

func (runtime *Runtime) rememberActivationSource(sourceID string) (bool, error) {
	if strings.TrimSpace(sourceID) == "" {
		return false, errors.New("activation source identity is empty")
	}
	changed := false
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.ActivationSourceID != nil && *state.ActivationSourceID == sourceID {
			return nil
		}
		state.ActivationSourceID = stringPointer(sourceID)
		changed = true
		return runtime.saveState(state)
	})
	return changed, err
}

func (runtime *Runtime) matchesActivationPrompt(prompt string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return line == runtime.Config.ActivationMarker
	}
	return false
}

func activationSourceID(prompt string) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(prompt, "\r\n", "\n"))
	return strings.TrimPrefix(sha256Value([]byte(normalized)), "sha256:")
}

func (runtime *Runtime) hookSessionStart(input HookInput, writer io.Writer) error {
	if !runtime.StateExists() {
		return writeJSON(writer, map[string]any{})
	}
	source := strings.ToLower(strings.TrimSpace(input.Source))
	if !stringIn(source, []string{"startup", "resume", "clear", "compact"}) {
		return writeJSON(writer, map[string]any{})
	}
	state, err := runtime.LoadState()
	if err != nil || state.Status == "complete" {
		return writeJSON(writer, map[string]any{})
	}
	owned, err := runtime.ensureHookOwner(input)
	if err != nil || !owned {
		return writeJSON(writer, map[string]any{})
	}
	request, err := runtime.RequestLifecycleReview("session-start", source+":"+strings.TrimSpace(input.SessionID))
	if err != nil || request == nil {
		return writeJSON(writer, map[string]any{})
	}
	state, err = runtime.LoadState()
	if err != nil {
		return err
	}
	return writeAdditionalContext(writer, "SessionStart", runtime.reviewInstruction(state))
}

func (runtime *Runtime) hookPreCompact(input HookInput, writer io.Writer) error {
	if runtime.StateExists() {
		if _, err := runtime.LoadState(); err != nil {
			runtime.recordHookDiagnostic("pre-compact-state", err)
		}
	}
	return writeJSON(writer, map[string]any{})
}

func (runtime *Runtime) hookPostToolUse(input HookInput, writer io.Writer) error {
	if !runtime.StateExists() || !toolFailed(input.ToolResponse) {
		return writeJSON(writer, map[string]any{})
	}
	owned, err := runtime.ensureHookOwner(input)
	if err != nil || !owned {
		return writeJSON(writer, map[string]any{})
	}
	signature := failureFromResponse(input.ToolName, input.ToolResponse)
	if signature == "" {
		return writeJSON(writer, map[string]any{})
	}
	if isGovernorReviewChainAttempt(runtime, input) {
		_ = runtime.MarkReviewMissed(signature)
		return writeJSON(writer, map[string]any{})
	}
	if strings.TrimSpace(input.ToolUseID) == "" {
		return writeJSON(writer, map[string]any{})
	}
	evidenceHash := sha256Value(bytes.TrimSpace(input.ToolResponse))
	sourceExecution := strings.TrimSpace(input.SessionID) + ":" + strings.TrimSpace(input.TurnID)
	request, recordErr := runtime.RecordFailureEvent(FailureEventInput{
		Signature: signature, SourceKind: "codex_hook", SourceExecution: sourceExecution,
		ToolUseID: input.ToolUseID, EvidenceHash: evidenceHash,
	})
	if recordErr != nil {
		request, _ = runtime.RequestAdvisoryReview("failure-recording-integrity:"+input.ToolUseID, "a verified tool failure could not be recorded: "+recordErr.Error())
	}
	if request == nil {
		return writeJSON(writer, map[string]any{})
	}
	state, err := runtime.LoadState()
	if err != nil {
		return writeJSON(writer, map[string]any{})
	}
	return writeAdditionalContext(writer, "PostToolUse", runtime.reviewInstruction(state))
}

func isGovernorAgentAttempt(runtime *Runtime, input HookInput) bool {
	role, isAgent, err := requestedAgentRole(input)
	return isAgent && err == nil && strings.EqualFold(role, runtime.Config.GovernorAgentName)
}

func isGovernorReviewChainAttempt(runtime *Runtime, input HookInput) bool {
	if isGovernorAgentAttempt(runtime, input) {
		return true
	}
	if !stringIn(strings.ToLower(strings.TrimSpace(input.ToolName)), []string{"bash", "exec_command"}) {
		return false
	}
	action, ok := governanceControlAction(toolCommand(input))
	return ok && stringIn(action, []string{"accept-review", "record-review-response", "apply-review"})
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
	return "default", true, nil
}

func (runtime *Runtime) reviewInstruction(state *State) string {
	if state.Review.Status == "feedback_ready" && state.Review.ReviewID != nil {
		return "Governor advisory feedback is ready at `" + runtime.reviewLocation() + "`. Read and explicitly respond through `.codex/governance/governance-hook.ps1 record-review-response`; adopt, decline, or acknowledge it with evidence and a next validation point, then continue under the main Agent's own decision."
	}
	return "An advisory governance review is ready at `" + runtime.requestLocation() + "`. Reuse the single Governor for this main task (spawn `" + runtime.Config.GovernorAgentName + "` only if none is active), pass it the request path, wait for one JSON result, and persist that exact result with `.codex/governance/governance-hook.ps1 accept-review --json-base64 <base64>`. Then read the feedback and explicitly answer it with `record-review-response`. Governor feedback is mandatory input to the main Agent's reasoning, never permission or a tool gate; product work remains under the main Agent's control if Governor is unavailable."
}

func (runtime *Runtime) requestLocation() string {
	return filepath.ToSlash(mustRelative(runtime.Root, runtime.requestPath()))
}

func (runtime *Runtime) reviewLocation() string {
	return filepath.ToSlash(mustRelative(runtime.Root, runtime.reviewPath()))
}

func (runtime *Runtime) recordHookDiagnostic(event string, hookErr error) {
	if hookErr == nil || !runtime.StateExists() {
		return
	}
	_ = runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		state.LastDiagnostic = &RuntimeDiagnostic{Source: "hook:" + event, Summary: sha256Value([]byte(hookErr.Error())), OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}
		return runtime.saveState(state)
	})
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeAdditionalContext(writer io.Writer, event, context string) error {
	return writeJSON(writer, map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": context}})
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
	var value any
	if json.Unmarshal(response, &value) == nil {
		value = stableFailureValue(value)
		if encoded, err := json.Marshal(value); err == nil {
			return normalizeFailureSignature(strings.ToLower(strings.TrimSpace(toolName)) + ":class:" + sha256Value(encoded))
		}
	}
	stable := normalizeFailureText(string(bytes.TrimSpace(response)))
	return normalizeFailureSignature(strings.ToLower(strings.TrimSpace(toolName)) + ":class:" + sha256Value([]byte(stable)))
}

func stableFailureValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		stable := make(map[string]any, len(typed))
		for key, item := range typed {
			if volatileFailureKey(key) {
				continue
			}
			stable[key] = stableFailureValue(item)
		}
		return stable
	case []any:
		stable := make([]any, len(typed))
		for index, item := range typed {
			stable[index] = stableFailureValue(item)
		}
		return stable
	case string:
		return normalizeFailureText(typed)
	default:
		return value
	}
}

func volatileFailureKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(key)))
	switch normalized {
	case "callid", "requestid", "sessionid", "turnid", "tooluseid", "chunkid",
		"timestamp", "occurredat", "startedat", "completedat", "finishedat",
		"elapsed", "elapsedms", "duration", "durationms", "latency", "latencyms", "walltimeseconds":
		return true
	default:
		return false
	}
}

func normalizeFailureText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = failureTimestampPattern.ReplaceAllString(value, "<timestamp>")
	value = failureUUIDPattern.ReplaceAllString(value, "<uuid>")
	value = failureHashPattern.ReplaceAllString(value, "<hash>")
	value = failureDurationPattern.ReplaceAllString(value, "<duration>")
	value = failureLongNumberPattern.ReplaceAllString(value, "<number>")
	return failureWhitespacePattern.ReplaceAllString(value, " ")
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
