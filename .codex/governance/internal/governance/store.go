package governance

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Runtime struct {
	Root       string
	ConfigPath string
	Config     Config
	RuntimeDir string
}

func Open(start string) (*Runtime, error) {
	root, err := findRoot(start)
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(root, ".codex", "governance", "config.json")
	var config Config
	if err := decodeStrictFile(configPath, &config); err != nil {
		return nil, fmt.Errorf("load governance config: %w", err)
	}
	if err := validateConfig(root, config); err != nil {
		return nil, err
	}
	runtime := &Runtime{Root: root, ConfigPath: configPath, Config: config, RuntimeDir: resolvePath(root, config.RuntimeDirectory)}
	if runtime.StateExists() {
		if err := runtime.migrateLegacyStateIfNeeded(); err != nil {
			return nil, fmt.Errorf("migrate governance state: %w", err)
		}
	}
	return runtime, nil
}

func findRoot(start string) (string, error) {
	if strings.TrimSpace(start) == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(abs, ".codex", "governance", "config.json")
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", errors.New("cannot locate .codex/governance/config.json")
		}
		abs = parent
	}
}

func validateConfig(root string, config Config) error {
	if config.SchemaVersion != schemaVersion {
		return fmt.Errorf("unsupported governance config schema_version %d", config.SchemaVersion)
	}
	if config.RuntimeDirectory == "" || len(config.AuthorityPaths) == 0 || len(config.CompletionDefinitionPaths) == 0 {
		return errors.New("governance config requires runtime_directory, authority_paths and completion_definition_paths")
	}
	if !within(root, resolvePath(root, config.RuntimeDirectory)) {
		return errors.New("governance runtime_directory must remain inside the repository")
	}
	if strings.TrimSpace(config.GovernorAgentName) == "" || strings.TrimSpace(config.ActivationMarker) == "" {
		return errors.New("governance config requires governor_agent_name and activation_marker")
	}
	if config.ActivationMarker != strings.TrimSpace(config.ActivationMarker) || strings.ContainsAny(config.ActivationMarker, "\r\n") {
		return errors.New("activation_marker must be one exact, trimmed line")
	}
	all := append(append([]string{}, config.AuthorityPaths...), config.CompletionDefinitionPaths...)
	all = append(all, config.StateSchemaPath, config.ReviewRequestSchemaPath, config.ReviewSchemaPath)
	for _, path := range all {
		if path == "" {
			return errors.New("governance config contains an empty required path")
		}
		resolved := resolvePath(root, path)
		if !within(root, resolved) {
			return fmt.Errorf("configured path escapes repository: %s", path)
		}
		if info, err := os.Stat(resolved); err != nil || info.IsDir() {
			if err == nil {
				err = errors.New("path is a directory")
			}
			return fmt.Errorf("configured file %s is unavailable: %w", path, err)
		}
	}
	for _, constraint := range config.ExplicitResourceConstraints {
		if constraint.ConstraintID == "" || constraint.Source == "" || constraint.Measure == "" {
			return errors.New("explicit resource constraints require constraint_id, source and measure")
		}
	}
	return nil
}

func resolvePath(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(root, filepath.FromSlash(path)))
}

func within(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func decodeStrictFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return decodeStrict(file, target)
}

func decodeStrict(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func (runtime *Runtime) statePath() string { return filepath.Join(runtime.RuntimeDir, "state.json") }
func (runtime *Runtime) requestPath() string {
	return filepath.Join(runtime.RuntimeDir, "review-request.json")
}
func (runtime *Runtime) eventsPath() string { return filepath.Join(runtime.RuntimeDir, "events.jsonl") }
func (runtime *Runtime) reviewsDir() string { return filepath.Join(runtime.RuntimeDir, "reviews") }

func (runtime *Runtime) StateExists() bool {
	info, err := os.Stat(runtime.statePath())
	return err == nil && !info.IsDir()
}

func (runtime *Runtime) LoadState() (*State, error) {
	var state State
	if err := decodeStrictFile(runtime.statePath(), &state); err != nil {
		return nil, err
	}
	if err := validateState(&state); err != nil {
		return nil, fmt.Errorf("invalid governance state: %w", err)
	}
	return &state, nil
}

type legacyReviewStateV1 struct {
	FixedReviewGeneration int `json:"fixed_review_generation"`
}

type legacyFailureEventV1 struct {
	EventID            string   `json:"event_id"`
	Signature          string   `json:"signature"`
	WorkPacketID       string   `json:"work_packet_id"`
	SourceKind         string   `json:"source_kind"`
	SourceExecution    string   `json:"source_execution"`
	ToolUseID          string   `json:"tool_use_id"`
	RepairGeneration   int      `json:"repair_generation"`
	EvidenceHash       string   `json:"evidence_hash"`
	KnownEvidenceIDs   []string `json:"known_evidence_ids"`
	RepositoryIdentity string   `json:"repository_identity"`
	CandidateIdentity  string   `json:"candidate_identity"`
	ConfigIdentity     string   `json:"config_identity"`
	RuntimeIdentity    string   `json:"runtime_identity"`
	Trust              string   `json:"trust"`
	OccurredAt         string   `json:"occurred_at"`
}

type legacyFailureRepairV1 struct {
	RepairID           string   `json:"repair_id"`
	Signature          string   `json:"signature"`
	PreviousEventID    string   `json:"previous_event_id"`
	WorkPacketID       string   `json:"work_packet_id"`
	RepairGeneration   int      `json:"repair_generation"`
	RepositoryIdentity string   `json:"repository_identity"`
	CandidateIdentity  string   `json:"candidate_identity"`
	ConfigIdentity     string   `json:"config_identity"`
	RuntimeIdentity    string   `json:"runtime_identity"`
	EvidenceIDs        []string `json:"evidence_ids"`
	RecordedAt         string   `json:"recorded_at"`
}

type legacyWorkPacketV1 struct {
	PacketID            string                  `json:"packet_id"`
	ConditionID         string                  `json:"condition_id"`
	Objective           string                  `json:"objective"`
	Value               string                  `json:"value"`
	AllowedScope        []string                `json:"allowed_scope"`
	ExcludedScope       []string                `json:"excluded_scope"`
	ExpectedEvidence    []string                `json:"expected_evidence"`
	EvidenceCheckpoint  EvidenceCheckpoint      `json:"evidence_checkpoint"`
	StartedAt           string                  `json:"started_at"`
	LastEvidenceAt      *string                 `json:"last_evidence_at"`
	Checkpoint          *string                 `json:"checkpoint"`
	FailureEvents       []legacyFailureEventV1  `json:"failure_events"`
	FailureRepairs      []legacyFailureRepairV1 `json:"failure_repairs"`
	LegacyFailureLabels []string                `json:"failure_signatures"`
}

type legacyStateV1 struct {
	SchemaVersion               int                   `json:"schema_version"`
	RunID                       string                `json:"run_id"`
	Status                      string                `json:"status"`
	AuthorityHash               string                `json:"authority_hash"`
	CompletionConditions        []CompletionCondition `json:"completion_conditions"`
	CurrentWorkPacket           *legacyWorkPacketV1   `json:"current_work_packet"`
	PendingIntervention         *PendingIntervention  `json:"pending_intervention"`
	ExplicitResourceConstraints []ResourceConstraint  `json:"explicit_resource_constraints"`
	ReusableResults             []ReusableResult      `json:"reusable_results"`
	Review                      legacyReviewStateV1   `json:"review"`
	Owner                       *OwnerState           `json:"owner"`
	Handoff                     *HandoffState         `json:"handoff"`
}

func (runtime *Runtime) migrateLegacyStateIfNeeded() error {
	return runtime.withLock(func() error {
		raw, err := os.ReadFile(runtime.statePath())
		if err != nil {
			return err
		}
		var header struct {
			SchemaVersion int `json:"schema_version"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return err
		}
		if header.SchemaVersion == schemaVersion {
			var state State
			if err := json.Unmarshal(raw, &state); err != nil {
				return err
			}
			if state.CurrentFocus != nil && state.NextAction != nil && !state.CurrentFocus.EvidenceCheckpoint.Reached && strings.TrimSpace(*state.NextAction) == strings.TrimSpace(state.CurrentFocus.EvidenceCheckpoint.Description) {
				previous := *state.NextAction
				state.NextAction = stringPointer(state.CurrentFocus.Objective)
				if err := runtime.saveState(&state); err != nil {
					return err
				}
				if err := runtime.appendEvent("advisory_v2_next_action_repaired", state.RunID, "replaced a premature checkpoint outcome with the active objective", map[string]any{"previous_next_action": previous, "focus_id": state.CurrentFocus.FocusID}); err != nil {
					return err
				}
			}
			return runtime.finalizeAdvisoryV2Migration(&state)
		}
		if header.SchemaVersion != 1 {
			return fmt.Errorf("unsupported governance state schema_version %d", header.SchemaVersion)
		}
		var legacy legacyStateV1
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return err
		}
		archiveDir := filepath.Join(runtime.RuntimeDir, "migrations", "advisory-v2")
		if err := os.MkdirAll(archiveDir, 0o755); err != nil {
			return err
		}
		if err := writeOnce(filepath.Join(archiveDir, "state.v1.json"), raw); err != nil {
			return err
		}
		if request, readErr := os.ReadFile(runtime.requestPath()); readErr == nil {
			if err := writeOnce(filepath.Join(archiveDir, "review-request.v1.json"), request); err != nil {
				return err
			}
		}

		status := "active"
		if legacy.Status == "complete" {
			status = "complete"
		} else if legacy.PendingIntervention != nil {
			status = "awaiting_user"
		}
		next := "continue from the migrated execution snapshot and its next validation point"
		if status == "awaiting_user" {
			next = "handle the persisted user decision or external input, then continue from the saved execution snapshot"
		}
		state := &State{
			SchemaVersion:               schemaVersion,
			RunID:                       legacy.RunID,
			Status:                      status,
			AuthorityHash:               legacy.AuthorityHash,
			CompletionConditions:        legacy.CompletionConditions,
			CurrentFocus:                migrateLegacyWorkPacket(legacy.CurrentWorkPacket),
			PendingIntervention:         legacy.PendingIntervention,
			ExplicitResourceConstraints: legacy.ExplicitResourceConstraints,
			ReusableResults:             legacy.ReusableResults,
			NextAction:                  &next,
			Review: ReviewState{
				Status:                "idle",
				FixedReviewGeneration: legacy.Review.FixedReviewGeneration,
			},
			Owner:   legacy.Owner,
			Handoff: legacy.Handoff,
		}
		if state.RunID == "" {
			state.RunID = newID("run")
		}
		if state.ReusableResults == nil {
			state.ReusableResults = []ReusableResult{}
		}
		if err := runtime.saveState(state); err != nil {
			return err
		}
		return runtime.finalizeAdvisoryV2Migration(state)
	})
}

func (runtime *Runtime) finalizeAdvisoryV2Migration(state *State) error {
	if err := runtime.archiveInactiveLegacyArtifacts(); err != nil {
		return err
	}
	archiveDir := filepath.Join(runtime.RuntimeDir, "migrations", "advisory-v2")
	if _, err := os.Stat(filepath.Join(archiveDir, "state.v1.json")); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	marker := filepath.Join(archiveDir, "migration.json")
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	exists, err := runtime.eventKindExists("advisory_v2_migrated", state.RunID)
	if err != nil {
		return err
	}
	if !exists {
		if err := runtime.appendEvent("advisory_v2_migrated", state.RunID, "migrated approval-based governance state to an advisory execution snapshot", map[string]any{"migration": "advisory-v2", "preserved_focus": state.CurrentFocus != nil}); err != nil {
			return err
		}
	}
	return atomicWriteJSON(marker, map[string]any{"schema_version": schemaVersion, "migration": "advisory-v2", "status": "complete"})
}

func (runtime *Runtime) eventKindExists(kind, runID string) (bool, error) {
	file, err := os.Open(runtime.eventsPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event Event
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Kind == kind && event.RunID == runID {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func migrateLegacyWorkPacket(packet *legacyWorkPacketV1) *ExecutionSnapshot {
	if packet == nil {
		return nil
	}
	involvedScope := make([]string, 0, len(packet.AllowedScope)+len(packet.ExcludedScope))
	for _, item := range normalizeStrings(packet.AllowedScope) {
		involvedScope = append(involvedScope, "原执行范围说明（仅作上下文）："+item)
	}
	for _, item := range normalizeStrings(packet.ExcludedScope) {
		involvedScope = append(involvedScope, "原边界说明（仅作上下文）："+item)
	}
	focus := &ExecutionSnapshot{
		FocusID:            packet.PacketID,
		ConditionID:        packet.ConditionID,
		Objective:          packet.Objective,
		Value:              packet.Value,
		InvolvedScope:      involvedScope,
		ExpectedEvidence:   normalizeStrings(packet.ExpectedEvidence),
		EvidenceCheckpoint: packet.EvidenceCheckpoint,
		StartedAt:          packet.StartedAt,
		LastEvidenceAt:     packet.LastEvidenceAt,
		Checkpoint:         packet.Checkpoint,
		FailureEvents:      []FailureEvent{},
		FailureRepairs:     []FailureRepair{},
	}
	if focus.StartedAt == "" {
		focus.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	for _, event := range packet.FailureEvents {
		focus.FailureEvents = append(focus.FailureEvents, FailureEvent{
			EventID: event.EventID, Signature: event.Signature, FocusID: focus.FocusID,
			SourceKind: event.SourceKind, SourceExecution: event.SourceExecution, ToolUseID: event.ToolUseID,
			RepairGeneration: event.RepairGeneration, EvidenceHash: event.EvidenceHash,
			KnownEvidenceIDs: event.KnownEvidenceIDs, RepositoryIdentity: event.RepositoryIdentity,
			CandidateIdentity: event.CandidateIdentity, ConfigIdentity: event.ConfigIdentity,
			RuntimeIdentity: event.RuntimeIdentity, Trust: event.Trust, OccurredAt: event.OccurredAt,
		})
	}
	for index, signature := range normalizeStrings(packet.LegacyFailureLabels) {
		normalized := normalizeFailureSignature(signature)
		if normalized == "" {
			continue
		}
		identity, _ := hashJSON(map[string]any{"focus_id": focus.FocusID, "signature": normalized, "index": index})
		occurredAt := focus.StartedAt
		focus.FailureEvents = append(focus.FailureEvents, FailureEvent{
			EventID: "legacy_failure_" + strings.TrimPrefix(identity, "sha256:")[:24], Signature: normalized, FocusID: focus.FocusID,
			SourceKind: "legacy_state", SourceExecution: "advisory-v1-migration", ToolUseID: fmt.Sprintf("legacy-signature-%d", index+1),
			RepairGeneration: 0, EvidenceHash: sha256Value([]byte(normalized)), KnownEvidenceIDs: []string{},
			Trust: "legacy_unverified", OccurredAt: occurredAt,
		})
	}
	for _, repair := range packet.FailureRepairs {
		focus.FailureRepairs = append(focus.FailureRepairs, FailureRepair{
			RepairID: repair.RepairID, Signature: repair.Signature, PreviousEventID: repair.PreviousEventID,
			FocusID: focus.FocusID, RepairGeneration: repair.RepairGeneration,
			RepositoryIdentity: repair.RepositoryIdentity, CandidateIdentity: repair.CandidateIdentity,
			ConfigIdentity: repair.ConfigIdentity, RuntimeIdentity: repair.RuntimeIdentity,
			EvidenceIDs: repair.EvidenceIDs, RecordedAt: repair.RecordedAt,
		})
	}
	payload := ExecutionSnapshotInput{
		FocusID: focus.FocusID, ConditionID: focus.ConditionID, Objective: focus.Objective,
		Value: focus.Value, InvolvedScope: focus.InvolvedScope, ExpectedEvidence: focus.ExpectedEvidence,
		CheckpointID:          focus.EvidenceCheckpoint.CheckpointID,
		CheckpointDescription: focus.EvidenceCheckpoint.Description,
	}
	focus.SnapshotHash, _ = hashJSON(payload)
	return focus
}

func (runtime *Runtime) archiveInactiveLegacyArtifacts() error {
	archiveDir := filepath.Join(runtime.RuntimeDir, "migrations", "advisory-v2")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return err
	}
	if raw, err := os.ReadFile(runtime.requestPath()); err == nil {
		var header struct {
			SchemaVersion int `json:"schema_version"`
		}
		if json.Unmarshal(raw, &header) == nil && header.SchemaVersion == 1 {
			if err := writeOnce(filepath.Join(archiveDir, "review-request.v1.json"), raw); err != nil {
				return err
			}
			if err := os.Remove(runtime.requestPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	for _, name := range []string{"failure-event-migration.json", "invalid-main-agent-stop-v1.json"} {
		path := filepath.Join(runtime.RuntimeDir, name)
		if raw, err := os.ReadFile(path); err == nil {
			if err := writeOnce(filepath.Join(archiveDir, name), raw); err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func writeOnce(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWrite(path, data)
}

func (runtime *Runtime) saveState(state *State) error {
	if err := validateState(state); err != nil {
		return err
	}
	return atomicWriteJSON(runtime.statePath(), state)
}

func (runtime *Runtime) withLock(action func() error) error {
	if err := os.MkdirAll(runtime.RuntimeDir, 0o755); err != nil {
		return err
	}
	lockPath := filepath.Join(runtime.RuntimeDir, ".lock")
	lock, err := acquireStateLock(lockPath, 2*time.Second)
	if err != nil {
		return err
	}
	defer lock.release()
	return action()
}

func atomicWriteJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWrite(path, data)
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".governance-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tempPath, path); err != nil {
		return err
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func (runtime *Runtime) appendEvent(kind, runID, summary string, fields map[string]any) error {
	if err := os.MkdirAll(runtime.RuntimeDir, 0o755); err != nil {
		return err
	}
	event := Event{SchemaVersion: schemaVersion, EventID: newID("event"), OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), Kind: kind, RunID: runID, Summary: summary, Fields: fields}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(runtime.eventsPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func newID(prefix string) string {
	seed := fmt.Sprintf("%s:%d:%d", prefix, time.Now().UnixNano(), os.Getpid())
	hash := sha256.Sum256([]byte(seed))
	return prefix + "_" + hex.EncodeToString(hash[:12])
}

func sha256Value(data []byte) string {
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func hashJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return sha256Value(data), nil
}

func (runtime *Runtime) authorityHash() (string, error) {
	paths := append(append([]string{}, runtime.Config.AuthorityPaths...), runtime.Config.CompletionDefinitionPaths...)
	return hashPaths(runtime.Root, paths)
}

func hashPaths(root string, paths []string) (string, error) {
	hash := sha256.New()
	for _, path := range paths {
		resolved := resolvePath(root, path)
		data, err := os.ReadFile(resolved)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func (runtime *Runtime) repositorySnapshot() (RepositorySnapshot, error) {
	head, err := runGit(runtime.Root, "rev-parse", "HEAD")
	if err != nil {
		return RepositorySnapshot{}, err
	}
	status, err := runGitBytes(runtime.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return RepositorySnapshot{}, err
	}
	diff, err := runGitBytes(runtime.Root, "diff", "--binary", "HEAD", "--")
	if err != nil {
		return RepositorySnapshot{}, err
	}
	untracked, err := runGitBytes(runtime.Root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return RepositorySnapshot{}, err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(strings.TrimSpace(head)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(status)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(diff)
	for _, relative := range bytes.Split(untracked, []byte{0}) {
		if len(relative) == 0 {
			continue
		}
		path := resolvePath(runtime.Root, filepath.FromSlash(string(relative)))
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return RepositorySnapshot{}, readErr
		}
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(relative)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
	}
	return RepositorySnapshot{Root: filepath.Clean(runtime.Root), HeadCommit: strings.TrimSpace(head), WorkingTreeHash: "sha256:" + hex.EncodeToString(hash.Sum(nil))}, nil
}

func runGit(root string, args ...string) (string, error) {
	data, err := runGitBytes(root, args...)
	return string(data), err
}

func runGitBytes(root string, args ...string) ([]byte, error) {
	command := exec.Command("git", args...)
	command.Dir = root
	data, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return data, nil
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func normalizeStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func readJSONLines(path string) ([]json.RawMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []json.RawMessage
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			return nil, errors.New("events.jsonl contains invalid JSON")
		}
		lines = append(lines, append(json.RawMessage(nil), line...))
	}
	return lines, scanner.Err()
}
