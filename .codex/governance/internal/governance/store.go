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
		if err := runtime.migrateFailureEvents(); err != nil {
			return nil, fmt.Errorf("migrate governance failure events: %w", err)
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
	if config.GovernorAgentName == "" || config.GovernedToolMatcher == "" || len(config.ActivationPromptPatterns) == 0 {
		return errors.New("governance config requires governor_agent_name, governed_tool_matcher and activation_prompt_patterns")
	}
	if len(config.AgentCapabilities) == 0 {
		return errors.New("governance config requires an explicit agent capability matrix")
	}
	roles := map[string]struct{}{}
	for _, capability := range config.AgentCapabilities {
		if strings.TrimSpace(capability.Role) == "" || !oneOf(capability.ProductMCP, "disabled", "shared-client") {
			return errors.New("agent capabilities require a role and product_mcp disabled or shared-client")
		}
		if _, exists := roles[capability.Role]; exists {
			return fmt.Errorf("duplicate agent capability role %q", capability.Role)
		}
		roles[capability.Role] = struct{}{}
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
	if state.CurrentWorkPacket != nil {
		for index := range state.CurrentWorkPacket.FailureEvents {
			if state.CurrentWorkPacket.FailureEvents[index].Trust == "legacy_unverified" && state.CurrentWorkPacket.FailureEvents[index].KnownEvidenceIDs == nil {
				state.CurrentWorkPacket.FailureEvents[index].KnownEvidenceIDs = []string{}
			}
		}
	}
	if err := validateState(&state); err != nil {
		return nil, fmt.Errorf("invalid governance state: %w", err)
	}
	return &state, nil
}

func (runtime *Runtime) migrateFailureEvents() error {
	return runtime.withLock(func() error {
		var state State
		if err := decodeStrictFile(runtime.statePath(), &state); err != nil {
			return err
		}
		markerPath := filepath.Join(runtime.RuntimeDir, "failure-event-migration.json")
		marker := map[string]any{}
		if data, err := os.ReadFile(markerPath); err == nil {
			if err := json.Unmarshal(data, &marker); err != nil {
				return err
			}
		}
		packet := state.CurrentWorkPacket
		needsMigration := packet != nil && (packet.FailureEvents == nil || packet.FailureRepairs == nil || len(packet.FailureSignatures) > 0)
		if marker["status"] == "complete" && !needsMigration {
			return validateState(&state)
		}
		if marker["status"] == "complete" && needsMigration {
			marker = map[string]any{}
		}
		if !needsMigration && len(marker) == 0 {
			return validateState(&state)
		}
		migrationID := "failure-events-v1"
		migrationReviewTrigger := "fixed:legacy-state-migration:" + migrationID
		oldReviewRequired := state.Review.Required && (state.Review.Trigger == nil || *state.Review.Trigger != migrationReviewTrigger)
		if len(marker) == 0 {
			marker = map[string]any{"schema": "ownward.governance-migration/v1", "migration_id": migrationID, "status": "prepared", "replace_review": oldReviewRequired}
			if err := atomicWriteJSON(markerPath, marker); err != nil {
				return err
			}
		}
		if packet != nil && needsMigration {
			if packet.FailureEvents == nil {
				packet.FailureEvents = []FailureEvent{}
			}
			if packet.FailureRepairs == nil {
				packet.FailureRepairs = []FailureRepair{}
			}
			for _, raw := range packet.FailureSignatures {
				signature := normalizeFailureSignature(raw)
				identity, _ := hashJSON(map[string]string{"packet_id": packet.PacketID, "signature": signature, "trust": "legacy_unverified"})
				evidence, _ := hashJSON(map[string]string{"legacy_signature": signature})
				packet.FailureEvents = append(packet.FailureEvents, FailureEvent{
					EventID: "legacy_" + strings.TrimPrefix(identity, "sha256:")[:24], Signature: signature,
					WorkPacketID: packet.PacketID, SourceKind: "legacy", SourceExecution: "legacy:" + packet.PacketID,
					ToolUseID: "legacy-unverified", RepairGeneration: 0, EvidenceHash: evidence,
					KnownEvidenceIDs: []string{}, Trust: "legacy_unverified", OccurredAt: packet.StartedAt,
				})
			}
			packet.FailureSignatures = []string{}
		}
		if oldReviewRequired {
			if data, err := os.ReadFile(runtime.requestPath()); err == nil {
				directory := filepath.Join(runtime.reviewsDir(), "superseded")
				if err := os.MkdirAll(directory, 0o755); err != nil {
					return err
				}
				name := "legacy-review-request.json"
				if state.Review.ReviewID != nil {
					name = *state.Review.ReviewID + ".json"
				}
				if err := atomicWrite(filepath.Join(directory, name), data); err != nil {
					return err
				}
			}
			state.Review = ReviewState{FixedReviewGeneration: state.Review.FixedReviewGeneration}
		}
		if err := runtime.saveState(&state); err != nil {
			return err
		}
		if !runtime.migrationEventExists(migrationID) {
			if err := runtime.appendEvent("failure_event_migration", state.RunID, "migrated legacy failure signatures as non-counting audit facts", map[string]any{"migration_id": migrationID, "legacy_events": len(packet.FailureEvents)}); err != nil {
				return err
			}
		}
		if replace, _ := marker["replace_review"].(bool); replace && !state.Review.Required {
			trigger, err := newReviewTrigger("fixed", "legacy-state-migration", migrationID, "migrate legacy failure events")
			if err != nil {
				return err
			}
			if _, err := runtime.requestReviewLocked(&state, trigger, false); err != nil {
				return err
			}
		}
		marker["status"] = "complete"
		return atomicWriteJSON(markerPath, marker)
	})
}

func (runtime *Runtime) migrationEventExists(migrationID string) bool {
	lines, err := readJSONLines(runtime.eventsPath())
	if err != nil {
		return false
	}
	for _, line := range lines {
		var event Event
		if json.Unmarshal(line, &event) == nil && event.Kind == "failure_event_migration" && event.Fields["migration_id"] == migrationID {
			return true
		}
	}
	return false
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
