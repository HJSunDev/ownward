package governance

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"
)

var handoffPromptPattern = regexp.MustCompile(`\[ownward-governance-handoff\s+id=([^\s\]]+)\s+token=([^\s\]]+)\]`)

// ensureHookOwner identifies the one main task whose lifecycle events may
// create reviews. Ownership never grants or removes permission to do work.
func (runtime *Runtime) ensureHookOwner(input HookInput) (bool, error) {
	if strings.TrimSpace(input.SessionID) == "" {
		return true, nil
	}
	owned := false
	err := runtime.withLock(func() error {
		state, err := runtime.LoadState()
		if err != nil {
			return err
		}
		if state.Owner == nil {
			state.Owner = &OwnerState{
				SessionID:      input.SessionID,
				TranscriptPath: input.TranscriptPath,
				OwnerEpoch:     1,
				AcquiredAt:     time.Now().UTC().Format(time.RFC3339Nano),
			}
			if err := runtime.saveState(state); err != nil {
				return err
			}
			if err := runtime.appendEvent("owner_claimed", state.RunID, "bound advisory governance to its main task", map[string]any{"session_id": input.SessionID, "owner_epoch": 1}); err != nil {
				return err
			}
			owned = true
			return nil
		}
		owned = state.Owner.SessionID == input.SessionID
		return nil
	})
	return owned, err
}

// Handoff transfers lifecycle-review ownership only. Preparing or binding a
// handoff never freezes either task and never changes product permissions.
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
			HandoffID:       newID("handoff"),
			TokenHash:       sha256Value([]byte(token)),
			SourceSessionID: state.Owner.SessionID,
			SourceEpoch:     state.Owner.OwnerEpoch,
			Status:          "prepared",
			CreatedAt:       now.Format(time.RFC3339Nano),
			ExpiresAt:       now.Add(30 * time.Minute).Format(time.RFC3339Nano),
		}
		state.Handoff = handoff
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if err := runtime.appendEvent("handoff_prepared", state.RunID, "prepared advisory governance ownership handoff", map[string]any{"handoff_id": handoff.HandoffID, "source_epoch": handoff.SourceEpoch}); err != nil {
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
		oldSession := ""
		if state.Owner != nil {
			oldSession = state.Owner.SessionID
		}
		state.Owner = &OwnerState{
			SessionID:      input.SessionID,
			TranscriptPath: input.TranscriptPath,
			OwnerEpoch:     handoff.SourceEpoch + 1,
			AcquiredAt:     time.Now().UTC().Format(time.RFC3339Nano),
		}
		state.Handoff = nil
		if err := runtime.saveState(state); err != nil {
			return err
		}
		if err := runtime.appendEvent("handoff_consumed", state.RunID, "transferred advisory governance ownership", map[string]any{"from_session": oldSession, "to_session": input.SessionID, "owner_epoch": state.Owner.OwnerEpoch}); err != nil {
			return err
		}
		consumed = true
		return nil
	})
	return consumed, err
}
