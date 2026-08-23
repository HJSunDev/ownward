package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type RepairManifest struct {
	SchemaVersion     int               `json:"schema_version"`
	RepairID          string            `json:"repair_id"`
	RuntimeIdentity   string            `json:"runtime_identity"`
	ProtectedIdentity string            `json:"protected_identity"`
	BaseHashes        map[string]string `json:"base_hashes"`
	CreatedAt         string            `json:"created_at"`
	Status            string            `json:"status"`
}

func repairAllowed(relative string) bool {
	value := filepath.ToSlash(filepath.Clean(relative))
	if value == ".codex/config.toml" || value == ".codex/hooks.json" || value == ".codex/governance/governance-hook.ps1" || value == ".codex/governance/governance-hook.sh" || value == ".codex/governance/go.mod" || value == ".codex/governance/go.sum" {
		return true
	}
	return strings.HasPrefix(value, ".codex/agents/") && strings.HasSuffix(value, ".toml") ||
		strings.HasPrefix(value, ".codex/governance/cmd/") && strings.HasSuffix(value, ".go") ||
		strings.HasPrefix(value, ".codex/governance/internal/") && strings.HasSuffix(value, ".go")
}

func (runtime *Runtime) protectedIdentity() (string, error) {
	paths := append(append([]string{}, runtime.Config.AuthorityPaths...), runtime.Config.CompletionDefinitionPaths...)
	paths = append(paths, ".codex/governance/config.json", runtime.Config.StateSchemaPath, runtime.Config.ReviewRequestSchemaPath, runtime.Config.ReviewSchemaPath)
	return hashPaths(runtime.Root, normalizeStrings(paths))
}

func (runtime *Runtime) StageRepair() (*RepairManifest, string, error) {
	state, err := runtime.LoadState()
	if err != nil {
		return nil, "", err
	}
	if state.InfrastructureFailure == nil {
		return nil, "", errors.New("repair staging requires a latched governance infrastructure failure")
	}
	identity, err := runtime.runtimeIdentity()
	if err != nil {
		return nil, "", err
	}
	if identity != state.InfrastructureFailure.RuntimeIdentity {
		return nil, "", errors.New("runtime identity already changed; start a new task instead of staging another repair")
	}
	protected, err := runtime.protectedIdentity()
	if err != nil {
		return nil, "", err
	}
	repairID := newID("repair")
	root := filepath.Join(runtime.RuntimeDir, "repairs", repairID)
	filesRoot := filepath.Join(root, "files")
	base := map[string]string{}
	err = filepath.WalkDir(runtime.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == runtime.RuntimeDir || strings.HasPrefix(filepath.Clean(path), filepath.Clean(runtime.RuntimeDir)+string(filepath.Separator)) || entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		relative := filepath.ToSlash(mustRelative(runtime.Root, path))
		if !repairAllowed(relative) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		base[relative] = sha256Value(data)
		target := filepath.Join(filesRoot, filepath.FromSlash(relative))
		return atomicWrite(target, data)
	})
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, "", err
	}
	manifest := &RepairManifest{SchemaVersion: schemaVersion, RepairID: repairID, RuntimeIdentity: identity, ProtectedIdentity: protected, BaseHashes: base, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Status: "staged"}
	if err := atomicWriteJSON(filepath.Join(root, "manifest.json"), manifest); err != nil {
		_ = os.RemoveAll(root)
		return nil, "", err
	}
	return manifest, filepath.ToSlash(mustRelative(runtime.Root, root)), nil
}

func (runtime *Runtime) ApplyRepair(repairID string) error {
	root := filepath.Join(runtime.RuntimeDir, "repairs", filepath.Base(repairID))
	if !within(filepath.Join(runtime.RuntimeDir, "repairs"), root) {
		return errors.New("repair identity escapes the repair runtime")
	}
	var manifest RepairManifest
	if err := decodeStrictFile(filepath.Join(root, "manifest.json"), &manifest); err != nil {
		return err
	}
	if manifest.RepairID != repairID || manifest.Status != "staged" {
		return errors.New("repair manifest is not an applicable staged repair")
	}
	state, err := runtime.LoadState()
	if err != nil {
		return err
	}
	if state.InfrastructureFailure == nil || state.InfrastructureFailure.RuntimeIdentity != manifest.RuntimeIdentity {
		return errors.New("repair is not bound to the current latched runtime failure")
	}
	protected, err := runtime.protectedIdentity()
	if err != nil || protected != manifest.ProtectedIdentity {
		return errors.New("protected product or governance contracts changed after repair staging")
	}
	filesRoot := filepath.Join(root, "files")
	candidates := map[string][]byte{}
	err = filepath.WalkDir(filesRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(filesRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !repairAllowed(relative) {
			return fmt.Errorf("repair contains an out-of-scope path: %s", relative)
		}
		if _, exists := manifest.BaseHashes[relative]; !exists {
			return fmt.Errorf("repair adds an unreviewed control-plane path: %s", relative)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		candidates[relative] = data
		return nil
	})
	if err != nil {
		return err
	}
	if len(candidates) != len(manifest.BaseHashes) {
		return errors.New("repair removed a governed control-plane file")
	}
	changed := []string{}
	for relative, baseHash := range manifest.BaseHashes {
		current, err := os.ReadFile(resolvePath(runtime.Root, relative))
		if err != nil || sha256Value(current) != baseHash {
			return fmt.Errorf("control-plane source changed after repair staging: %s", relative)
		}
		if sha256Value(candidates[relative]) != baseHash {
			changed = append(changed, relative)
		}
	}
	if len(changed) == 0 {
		return errors.New("repair contains no control-plane change")
	}
	if err := validateStagedRepair(filesRoot, candidates); err != nil {
		return err
	}
	sort.Strings(changed)
	backups := map[string][]byte{}
	for _, relative := range changed {
		backups[relative], _ = os.ReadFile(resolvePath(runtime.Root, relative))
	}
	for _, relative := range changed {
		if err := atomicWrite(resolvePath(runtime.Root, relative), candidates[relative]); err != nil {
			for _, applied := range changed {
				if applied == relative {
					break
				}
				_ = atomicWrite(resolvePath(runtime.Root, applied), backups[applied])
			}
			return fmt.Errorf("apply repair %s: %w", relative, err)
		}
	}
	manifest.Status = "applied"
	if err := atomicWriteJSON(filepath.Join(root, "manifest.json"), &manifest); err != nil {
		return err
	}
	return runtime.appendEvent("control_plane_repair_applied", state.RunID, "applied a bounded control-plane repair; current task remains closed", map[string]any{"repair_id": repairID, "paths": changed})
}

func validateStagedRepair(filesRoot string, candidates map[string][]byte) error {
	if data, exists := candidates[".codex/hooks.json"]; exists {
		var hooks map[string]any
		if err := json.Unmarshal(data, &hooks); err != nil {
			return fmt.Errorf("staged hooks.json is invalid: %w", err)
		}
	}
	if project, exists := candidates[".codex/config.toml"]; exists {
		if governor, governorExists := candidates[".codex/agents/governor.toml"]; governorExists {
			if err := validateGovernorConfiguration(project, governor); err != nil {
				return fmt.Errorf("staged Governor isolation is invalid: %w", err)
			}
		}
	}
	moduleRoot := filepath.Join(filesRoot, ".codex", "governance")
	command := exec.Command("go", "test", "./...", "-run", `^Test(StrictJSONRejectsUnknownFields|GovernedRunAllowsLiveProcessWithoutTotalDeadline|GovernedRunStopsStaleProcess|ReviewResultConditionalValidation|StateLockIgnoresStaleSentinelAndRejectsLiveContention)$`)
	command.Dir = moduleRoot
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("staged governance implementation failed its tests: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (runtime *Runtime) isRepairStagingChange(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	root := filepath.Join(runtime.RuntimeDir, "repairs")
	for _, path := range paths {
		resolved := resolvePath(runtime.Root, path)
		if !within(root, resolved) || !strings.Contains(filepath.ToSlash(resolved), "/files/.codex/") {
			return false
		}
	}
	return true
}
