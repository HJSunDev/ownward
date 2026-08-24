package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
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
		// Already-loaded configurations may still invoke this event. Real Codex
		// payloads do not expose a reliable command exit status, so generic tool
		// results are deliberately ignored instead of guessed.
		err = writeJSON(writer, map[string]any{})
	case "subagent-start":
		err = runtime.hookSubagentStart(input, writer)
	case "subagent-stop":
		err = runtime.hookSubagentStop(input, writer)
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

// FastHookRelevant performs only payload-local classification. It is called
// before opening the governance runtime, so ordinary messages and unrelated
// subagents do not read state, authority files, or migration data.
func FastHookRelevant(event string, data []byte) bool {
	return FastHookRelevantAt(event, data, "")
}

// FastHookRelevantAt decides whether a Hook needs the full governance
// runtime. It may read only the tiny governance config and check whether the
// current state file exists; it never loads state, authority, evidence, or
// migrations. This keeps ordinary Codex work off the governance hot path.
func FastHookRelevantAt(event string, data []byte, start string) bool {
	switch event {
	case "user-prompt-submit":
		var input HookInput
		if json.Unmarshal(data, &input) != nil {
			return false
		}
		if handoffPromptPattern.MatchString(input.Prompt) {
			return FastStateExists(start)
		}
		candidate := firstPromptLine(input.Prompt)
		if !strings.Contains(candidate, "governance:enable") {
			return false
		}
		config, ok := loadFastHookConfig(start)
		return ok && candidate == config.ActivationMarker
	case "subagent-start", "subagent-stop":
		var input HookInput
		return json.Unmarshal(data, &input) == nil && strings.EqualFold(strings.TrimSpace(input.AgentType), "governor") && FastStateOwnerMatches(start, input.SessionID)
	case "post-tool-use", "pre-tool-use", "stop", "pre-compact", "post-compact":
		return false
	case "session-start":
		var input HookInput
		if json.Unmarshal(data, &input) != nil || !stringIn(strings.ToLower(strings.TrimSpace(input.Source)), []string{"startup", "resume", "clear", "compact"}) {
			return false
		}
		return FastStateOwnerMatches(start, input.SessionID)
	default:
		return false
	}
}

func firstPromptLine(prompt string) string {
	for _, line := range strings.Split(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
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
	return firstPromptLine(prompt) == runtime.Config.ActivationMarker
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

func (runtime *Runtime) hookSubagentStart(input HookInput, writer io.Writer) error {
	if !runtime.StateExists() || !strings.EqualFold(strings.TrimSpace(input.AgentType), runtime.Config.GovernorAgentName) || strings.TrimSpace(input.AgentID) == "" {
		return writeJSON(writer, map[string]any{})
	}
	_ = runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil || state.Owner == nil || strings.TrimSpace(input.SessionID) == "" || state.Owner.SessionID != input.SessionID || state.Review.Status != "requested" {
			return err
		}
		if state.Review.GovernorAgentID == nil {
			state.Review.GovernorAgentID = stringPointer(input.AgentID)
			return runtime.saveState(state)
		}
		return nil
	})
	return writeJSON(writer, map[string]any{})
}

func (runtime *Runtime) hookSubagentStop(input HookInput, writer io.Writer) error {
	if !runtime.StateExists() || !strings.EqualFold(strings.TrimSpace(input.AgentType), runtime.Config.GovernorAgentName) || strings.TrimSpace(input.AgentID) == "" || input.LastAssistantMessage == nil {
		return writeJSON(writer, map[string]any{})
	}
	state, err := runtime.LoadState()
	if err != nil || state.Owner == nil || strings.TrimSpace(input.SessionID) == "" || state.Owner.SessionID != input.SessionID || state.Review.Status != "requested" || state.Review.GovernorAgentID == nil || *state.Review.GovernorAgentID != input.AgentID {
		return writeJSON(writer, map[string]any{})
	}
	var result ReviewResult
	if err := decodeStrict(strings.NewReader(*input.LastAssistantMessage), &result); err != nil {
		_ = runtime.MarkReviewMissed("Governor returned invalid JSON")
		return writeJSON(writer, map[string]any{})
	}
	if _, err := runtime.AcceptReview(result); err != nil {
		_ = runtime.MarkReviewMissed("Governor feedback failed deterministic validation")
	}
	return writeJSON(writer, map[string]any{})
}

func (runtime *Runtime) reviewInstruction(state *State) string {
	if oneOf(state.Review.Status, "feedback_ready", "superseded") && state.Review.ReviewID != nil {
		return "Governor advisory feedback is ready at `" + runtime.reviewLocation() + "`. Read and explicitly respond through `.codex/governance/governance-hook.ps1 record-review-response`; adopt, decline, or acknowledge it with evidence and a next validation point, then continue under the main Agent's own decision."
	}
	return "An advisory governance review is ready at `" + runtime.requestLocation() + "`. Start or reuse the single `" + runtime.Config.GovernorAgentName + "` for this main task. When starting it, use `agent_type=\"" + runtime.Config.GovernorAgentName + "\"` with `fork_turns=\"none\"` and pass only that request path; do not fall back to a default agent. Do not wait: continue the current bounded work. The native SubagentStop channel validates and stores its single JSON result automatically. At the next related evidence checkpoint, high-cost expansion, stage end, or completion claim, read and explicitly answer available feedback with `record-review-response`; if no result is available, record it missed and continue. Governor feedback must enter the main Agent's reasoning but never grants permission or controls tools."
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

func stringIn(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
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
