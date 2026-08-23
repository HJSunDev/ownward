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
	if err := validateReusableAssets(runtime.Root); err != nil {
		return nil, err
	}
	checks = append(checks, "project and reusable JSON contracts are closed, parseable, and identical")

	if err := validateHookConfiguration(runtime.Root); err != nil {
		return nil, err
	}
	if err := validateHookWrappers(runtime.Root); err != nil {
		return nil, err
	}
	checks = append(checks, "Hooks use natural advisory boundaries, register no tool or Stop gate, and their launchers fail open")

	projectConfig, err := os.ReadFile(filepath.Join(runtime.Root, ".codex", "config.toml"))
	if err != nil {
		return nil, err
	}
	governorConfig, err := os.ReadFile(filepath.Join(runtime.Root, ".codex", "agents", runtime.Config.GovernorAgentName+".toml"))
	if err != nil {
		return nil, err
	}
	if err := validateGovernorConfiguration(projectConfig, governorConfig); err != nil {
		return nil, err
	}
	checks = append(checks, "Governor is read-only, advisory, and isolated from the stateful product MCP")

	if err := runtime.runAdvisoryFixture(); err != nil {
		return nil, err
	}
	checks = append(checks, "activation, ordinary messages, duplicate triggers, fixed current-state files, explicit response, compact recovery, and fail-open behavior passed an isolated runtime fixture")

	return &DoctorReport{Status: "passed", Checks: checks}, nil
}

func validateHookWrappers(root string) error {
	powershellData, err := os.ReadFile(filepath.Join(root, ".codex", "governance", "governance-hook.ps1"))
	if err != nil {
		return err
	}
	powershell := string(powershellData)
	for _, required := range []string{`$isHook`, `Exit-GovernanceFailure`, `[Console]::Out.WriteLine("{}")`, `if ($LASTEXITCODE -ne 0) { Exit-GovernanceFailure $LASTEXITCODE }`} {
		if !strings.Contains(powershell, required) {
			return errors.New("PowerShell Hook launcher does not guarantee fail-open startup")
		}
	}

	shellData, err := os.ReadFile(filepath.Join(root, ".codex", "governance", "governance-hook.sh"))
	if err != nil {
		return err
	}
	shell := string(shellData)
	for _, required := range []string{`is_hook=false`, `fail_governance()`, `printf '{}\n'`, `go build -trimpath -o "$binary" ./cmd/governance-cli) || fail_governance $?`} {
		if !strings.Contains(shell, required) {
			return errors.New("POSIX Hook launcher does not guarantee fail-open startup")
		}
	}
	return nil
}

func validateHookConfiguration(root string) error {
	data, err := os.ReadFile(filepath.Join(root, ".codex", "hooks.json"))
	if err != nil {
		return err
	}
	var document struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("invalid hooks.json: %w", err)
	}
	for _, required := range []string{"SessionStart", "UserPromptSubmit", "PreCompact", "PostToolUse"} {
		if _, exists := document.Hooks[required]; !exists {
			return fmt.Errorf("hooks.json is missing %s", required)
		}
	}
	for _, forbidden := range []string{"PreToolUse", "Stop", "SubagentStart", "SubagentStop"} {
		if _, exists := document.Hooks[forbidden]; exists {
			return fmt.Errorf("governance must not register %s", forbidden)
		}
	}
	return nil
}

func validateReusableAssets(root string) error {
	pairs := [][2]string{
		{".codex/governance/state.schema.json", ".agents/skills/autonomous-development-governance/assets/governance-runtime/state.schema.json"},
		{".codex/governance/review-request.schema.json", ".agents/skills/autonomous-development-governance/assets/governance-runtime/review-request.schema.json"},
		{".codex/governance/review.schema.json", ".agents/skills/autonomous-development-governance/assets/governance-runtime/review.schema.json"},
	}
	for _, pair := range pairs {
		left, err := os.ReadFile(resolvePath(root, pair[0]))
		if err != nil {
			return err
		}
		right, err := os.ReadFile(resolvePath(root, pair[1]))
		if err != nil {
			return err
		}
		if !bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right)) {
			return fmt.Errorf("reusable schema differs from project contract: %s", pair[0])
		}
	}
	return nil
}

func (runtime *Runtime) runAdvisoryFixture() error {
	directory, err := os.MkdirTemp("", "ownward-governance-doctor-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	fixture := &Runtime{Root: runtime.Root, ConfigPath: runtime.ConfigPath, Config: runtime.Config, RuntimeDir: directory}

	var ordinary bytes.Buffer
	if err := fixture.HandleHook("user-prompt-submit", bytes.NewBufferString(`{"session_id":"doctor-main","turn_id":"ordinary-1","prompt":"ordinary question"}`), &ordinary); err != nil {
		return err
	}
	if fixture.StateExists() || strings.TrimSpace(ordinary.String()) != "{}" {
		return errors.New("ordinary prompt activated or disturbed governance")
	}

	activation := map[string]any{"session_id": "doctor-main", "turn_id": "activation-1", "prompt": runtime.Config.ActivationMarker + "\nfixture goal"}
	activationJSON, _ := json.Marshal(activation)
	var first bytes.Buffer
	if err := fixture.HandleHook("user-prompt-submit", bytes.NewReader(activationJSON), &first); err != nil {
		return err
	}
	state, err := fixture.LoadState()
	if err != nil || state.Review.Status != "requested" || state.Owner == nil || state.Owner.SessionID != "doctor-main" {
		return errors.New("stable activation marker did not create one owned advisory review")
	}
	firstReviewID := valueOr(state.Review.ReviewID, "")
	firstGeneration := state.Review.FixedReviewGeneration

	var duplicate bytes.Buffer
	if err := fixture.HandleHook("user-prompt-submit", bytes.NewReader(activationJSON), &duplicate); err != nil {
		return err
	}
	state, err = fixture.LoadState()
	if err != nil || valueOr(state.Review.ReviewID, "") != firstReviewID || state.Review.FixedReviewGeneration != firstGeneration {
		return errors.New("duplicate activation created another review or generation")
	}

	before, _ := os.ReadFile(fixture.statePath())
	var normal bytes.Buffer
	if err := fixture.HandleHook("user-prompt-submit", bytes.NewBufferString(`{"session_id":"doctor-main","turn_id":"ordinary-2","prompt":"do another task"}`), &normal); err != nil {
		return err
	}
	after, _ := os.ReadFile(fixture.statePath())
	if !bytes.Equal(before, after) || strings.TrimSpace(normal.String()) != "{}" {
		return errors.New("ordinary prompt changed state or injected governance context")
	}

	request, err := fixture.loadRequest()
	if err != nil {
		return err
	}
	result := ReviewResult{
		ReviewID: request.ReviewID, TriggerInstanceID: request.TriggerInstanceID, ReviewSnapshotHash: request.ReviewSnapshotHash,
		Recommendation:     "continue",
		MacroAssessment:    MacroAssessment{OverallProgress: "fixture", EvidenceSupport: "repository snapshot", Completed: []string{}, Unmet: []string{"fixture gap"}},
		HighestPriorityGap: stringPointer("fixture gap"),
		PathAssessment:     PathAssessment{Necessary: true, Efficient: true, Optimal: true, Problems: []string{}, BetterPlan: []string{}},
		PreservedResultIDs: []string{}, SuggestedInvalidations: []string{}, ValidatedEvidenceIDs: []string{},
		Reason: "the fixture path is valid",
	}
	if _, err := fixture.AcceptReview(result); err != nil {
		return fmt.Errorf("accept advisory feedback: %w", err)
	}
	state, err = fixture.RecordReviewResponse(ReviewResponseInput{ReviewID: request.ReviewID, Disposition: "acknowledge", Reason: "reviewed against fixture facts", NextValidationPoint: "fixture next point"})
	if err != nil || state.Review.Status != "responded" || state.Review.Response == nil {
		return errors.New("main Agent response was not persisted")
	}

	compactInput := map[string]any{"session_id": "doctor-main", "source": "compact", "hook_event_name": "SessionStart"}
	compactJSON, _ := json.Marshal(compactInput)
	var compact bytes.Buffer
	if err := fixture.HandleHook("session-start", bytes.NewReader(compactJSON), &compact); err != nil {
		return err
	}
	state, err = fixture.LoadState()
	if err != nil || state.Review.Status != "requested" || state.Review.FixedReviewGeneration != firstGeneration+1 || !strings.Contains(compact.String(), "additionalContext") {
		return errors.New("compact recovery did not create and deliver one fresh advisory review")
	}
	compactReviewID := valueOr(state.Review.ReviewID, "")
	var compactReplay bytes.Buffer
	if err := fixture.HandleHook("session-start", bytes.NewReader(compactJSON), &compactReplay); err != nil {
		return err
	}
	state, err = fixture.LoadState()
	if err != nil || valueOr(state.Review.ReviewID, "") != compactReviewID || state.Review.FixedReviewGeneration != firstGeneration+1 {
		return errors.New("replayed compact boundary created duplicate governance work")
	}

	if err := fixture.MarkReviewMissed("fixture Governor unavailable"); err != nil {
		return err
	}
	state, err = fixture.LoadState()
	if err != nil || state.Review.Status != "missed" || state.Status != "active" {
		return errors.New("Governor failure did not remain advisory and fail open")
	}
	var compatibility bytes.Buffer
	if err := fixture.HandleHook("pre-tool-use", bytes.NewBufferString(`{"tool_name":"apply_patch"}`), &compatibility); err != nil || strings.TrimSpace(compatibility.String()) != "{}" {
		return errors.New("legacy pre-tool compatibility path can still deny work")
	}
	if _, err := os.Stat(filepath.Join(directory, "events.jsonl")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("isolated runtime created a legacy event stream")
	}
	if _, err := os.Stat(filepath.Join(directory, "reviews")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("isolated runtime created a historical review directory")
	}
	return nil
}

func validateGovernorConfiguration(projectConfigData, governorData []byte) error {
	text := string(governorData)
	if !strings.Contains(text, `sandbox_mode = "read-only"`) {
		return errors.New("Governor must use the read-only sandbox")
	}
	if strings.Contains(strings.ToLower(text), "hooks = false") {
		return errors.New("Governor must not disable project hooks")
	}
	if !strings.Contains(text, "建议") && !strings.Contains(strings.ToLower(text), "advisory") {
		return errors.New("Governor instructions do not define an advisory relationship")
	}
	if strings.Contains(text, "批准令牌") || strings.Contains(text, "不得继续产品") || strings.Contains(text, "产品修改保持阻止") {
		return errors.New("Governor instructions retain execution-control semantics")
	}
	project := tomlSection(string(projectConfigData), "mcp_servers.ownward")
	if project == "" {
		return nil
	}
	governor := tomlSection(text, "mcp_servers.ownward")
	if governor == "" {
		return errors.New("Governor must explicitly isolate the stateful Ownward MCP")
	}
	for _, key := range []string{"command", "args", "cwd"} {
		if tomlAssignment(governor, key) == "" {
			return fmt.Errorf("Governor Ownward MCP isolation lacks standalone %s transport", key)
		}
	}
	if tomlAssignment(governor, "enabled") != "false" || tomlAssignment(governor, "required") != "false" {
		return errors.New("Governor Ownward MCP must be explicitly disabled and optional")
	}
	return nil
}

func tomlSection(data, sectionName string) string {
	lines := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	header := "[" + sectionName + "]"
	start := -1
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == header {
			start = index + 1
			continue
		}
		if start >= 0 && strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			return strings.Join(lines[start:index], "\n")
		}
	}
	if start >= 0 {
		return strings.Join(lines[start:], "\n")
	}
	return ""
}

func tomlAssignment(section, key string) string {
	for _, line := range strings.Split(section, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			return strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		}
	}
	return ""
}
