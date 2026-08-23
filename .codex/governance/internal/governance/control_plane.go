package governance

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var handoffPromptPattern = regexp.MustCompile(`\[ownward-governance-handoff\s+id=([^\s\]]+)\s+token=([^\s\]]+)\]`)

func (runtime *Runtime) ensureHookOwner(input HookInput) (bool, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		// Codex hooks always provide session_id. Empty IDs are accepted only by
		// direct deterministic fixtures that do not represent competing tasks.
		return true, nil
	}
	owned := false
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.Owner == nil {
			state.Owner = &OwnerState{SessionID: input.SessionID, TranscriptPath: input.TranscriptPath, OwnerEpoch: 1, AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := runtime.saveState(state); err != nil {
				return err
			}
			if err := runtime.appendEvent("owner_claimed", state.RunID, "bound the governance run to its first active task", map[string]any{"session_id": input.SessionID, "owner_epoch": 1}); err != nil {
				return err
			}
			owned = true
			return nil
		}
		// A prepared handoff freezes product work, not the source owner's
		// ability to bind or cancel that handoff. The hook layer applies the
		// narrower command allowlist while the handoff is pending.
		owned = state.Owner.SessionID == input.SessionID
		return nil
	})
	return owned, err
}

func (runtime *Runtime) PrepareHandoff(sourceSessionID string) (*HandoffTicket, error) {
	if strings.TrimSpace(sourceSessionID) == "" {
		return nil, errors.New("handoff requires the current owner session_id")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	var ticket *HandoffTicket
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.Owner == nil || state.Owner.SessionID != sourceSessionID {
			return errors.New("only the active governance owner can prepare a handoff")
		}
		if state.Handoff != nil {
			return errors.New("a governance handoff is already pending")
		}
		now := time.Now().UTC()
		handoff := &HandoffState{
			HandoffID: newID("handoff"), TokenHash: sha256Value([]byte(token)), SourceSessionID: state.Owner.SessionID,
			SourceEpoch: state.Owner.OwnerEpoch, Status: "prepared", CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(30 * time.Minute).Format(time.RFC3339Nano),
		}
		state.Handoff = handoff
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if err := runtime.appendEvent("handoff_prepared", state.RunID, "froze the active task for one-time handoff", map[string]any{"handoff_id": handoff.HandoffID, "source_epoch": handoff.SourceEpoch}); err != nil {
			return err
		}
		ticket = &HandoffTicket{HandoffID: handoff.HandoffID, Token: token, ExpiresAt: handoff.ExpiresAt}
		return nil
	})
	return ticket, err
}

func (runtime *Runtime) BindHandoff(handoffID, targetThreadID string) error {
	if strings.TrimSpace(targetThreadID) == "" {
		return errors.New("handoff requires the returned target thread id")
	}
	return runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.Handoff == nil || state.Handoff.HandoffID != handoffID || state.Handoff.Status != "prepared" {
			return errors.New("handoff is not prepared or its identity does not match")
		}
		state.Handoff.TargetThreadID = targetThreadID
		state.Handoff.Status = "bound"
		return runtime.saveState(state)
	})
}

func (runtime *Runtime) CancelHandoff(handoffID string) error {
	return runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.Handoff == nil || state.Handoff.HandoffID != handoffID {
			return errors.New("handoff identity does not match")
		}
		state.Handoff = nil
		return runtime.saveState(state)
	})
}

func (runtime *Runtime) consumeHandoff(input HookInput) (bool, error) {
	match := handoffPromptPattern.FindStringSubmatch(input.Prompt)
	if len(match) != 3 || strings.TrimSpace(input.SessionID) == "" {
		return false, nil
	}
	consumed := false
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		handoff := state.Handoff
		if handoff == nil || handoff.Status != "bound" || handoff.HandoffID != match[1] || handoff.TokenHash != sha256Value([]byte(match[2])) || handoff.TargetThreadID != input.SessionID {
			return errors.New("governance handoff token is invalid, not bound, or addressed to another task")
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, handoff.ExpiresAt)
		if err != nil || time.Now().UTC().After(expiresAt) {
			return errors.New("governance handoff token has expired")
		}
		oldSession := state.Owner.SessionID
		state.Owner = &OwnerState{SessionID: input.SessionID, TranscriptPath: input.TranscriptPath, OwnerEpoch: handoff.SourceEpoch + 1, AcquiredAt: time.Now().UTC().Format(time.RFC3339Nano)}
		state.Handoff = nil
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if err := runtime.appendEvent("handoff_consumed", state.RunID, "atomically transferred governance ownership", map[string]any{"from_session": oldSession, "to_session": input.SessionID, "owner_epoch": state.Owner.OwnerEpoch, "target_thread_id": handoff.TargetThreadID}); err != nil {
			return err
		}
		consumed = true
		return nil
	})
	return consumed, err
}

func (runtime *Runtime) runtimeIdentity() (string, error) {
	paths := []string{runtime.ConfigPath, filepath.Join(runtime.Root, ".codex", "hooks.json"), filepath.Join(runtime.Root, ".codex", "config.toml"), filepath.Join(runtime.Root, ".codex", "agents", runtime.Config.GovernorAgentName+".toml")}
	governanceRoot := filepath.Join(runtime.Root, ".codex", "governance")
	err := filepath.WalkDir(governanceRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == "runtime" || name == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".go") || entry.Name() == "go.mod" || entry.Name() == "go.sum" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	values := make([]string, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		values = append(values, filepath.ToSlash(path)+"\x00"+string(data))
	}
	return sha256Value([]byte(strings.Join(values, "\x00"))), nil
}

func (runtime *Runtime) latchInfrastructureFailure(signature string) error {
	identity, err := runtime.runtimeIdentity()
	if err != nil {
		return err
	}
	return runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if !state.Review.Required || state.Review.ReviewID == nil || state.Review.TriggerInstanceID == nil {
			return nil
		}
		if state.InfrastructureFailure != nil && state.InfrastructureFailure.RuntimeIdentity == identity && state.InfrastructureFailure.Signature == signature {
			return nil
		}
		if state.Owner == nil {
			return errors.New("cannot latch infrastructure failure without an active task owner")
		}
		state.InfrastructureFailure = &InfrastructureFailure{ReviewID: *state.Review.ReviewID, TriggerInstance: *state.Review.TriggerInstanceID, Signature: normalizeFailureSignature(signature), RuntimeIdentity: identity, OwnerSessionID: state.Owner.SessionID, OwnerEpoch: state.Owner.OwnerEpoch, Status: "latched", FirstObservedAt: time.Now().UTC().Format(time.RFC3339Nano), RecoveryAction: "repair the governance control plane through the bounded repair channel, then hand off to a new task with the new runtime identity"}
		if err := runtime.saveState(state); err != nil {
			return err
		}
		return runtime.appendEvent("infrastructure_failure_latched", state.RunID, "stopped automatic Governor retries for the unchanged runtime", map[string]any{"signature": signature, "runtime_identity": identity})
	})
}

func (runtime *Runtime) reconcileInfrastructureFailure(input HookInput) (bool, error) {
	state, err := runtime.LoadState()
	if err != nil || state.InfrastructureFailure == nil {
		return false, err
	}
	identity, err := runtime.runtimeIdentity()
	if err != nil {
		return false, err
	}
	if identity == state.InfrastructureFailure.RuntimeIdentity {
		return true, nil
	}
	if state.Owner == nil || state.Owner.SessionID == state.InfrastructureFailure.OwnerSessionID || state.Owner.OwnerEpoch <= state.InfrastructureFailure.OwnerEpoch || input.SessionID != state.Owner.SessionID {
		return true, nil
	}
	err = runtime.withLock(func() error {
		current, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if current.InfrastructureFailure == nil || current.InfrastructureFailure.RuntimeIdentity == identity {
			return nil
		}
		old := current.InfrastructureFailure.RuntimeIdentity
		current.InfrastructureFailure = nil
		if err := runtime.saveState(current); err != nil {
			return err
		}
		return runtime.appendEvent("infrastructure_failure_resolved", current.RunID, "observed a new governance runtime identity; a fresh review may run", map[string]any{"old_runtime_identity": old, "new_runtime_identity": identity})
	})
	return false, err
}

func (runtime *Runtime) infrastructureInstruction(state *State) string {
	return fmt.Sprintf("Governor infrastructure failed for review %s and this runtime identity is latched. Product modifications remain closed. Do not retry the Governor or let Stop loop. Preserve state, use the bounded governance repair channel, then continue in a new task after the runtime identity changes.", state.InfrastructureFailure.ReviewID)
}
