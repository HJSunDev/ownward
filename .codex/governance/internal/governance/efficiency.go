package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const pendingReviewLimit = 8

func (runtime *Runtime) authorityReferences() ([]AuthorityReference, error) {
	paths := normalizeStrings(append(append([]string{}, runtime.Config.AuthorityPaths...), runtime.Config.CompletionDefinitionPaths...))
	refs := make([]AuthorityReference, 0, len(paths))
	for _, path := range paths {
		hash, err := fileHash(resolvePath(runtime.Root, path))
		if err != nil {
			return nil, err
		}
		refs = append(refs, AuthorityReference{Path: filepath.ToSlash(path), Hash: hash})
	}
	return refs, nil
}

func (runtime *Runtime) progressDelta(state *State) ProgressDelta {
	delta := ProgressDelta{
		CriticalConditionID: "none", CriticalConditionStatus: "met", NewEvidenceIDs: []string{},
		ReusedEvidenceIDs: []string{}, InvalidatedEvidenceIDs: []string{}, CheckpointOutcome: "not_applicable",
		NextInvestment: valueOr(state.NextAction, "restore the most valuable next validation point"), NetProgress: "initial",
	}
	for _, condition := range state.CompletionConditions {
		if condition.Status != "met" {
			delta.CriticalConditionID = condition.ConditionID
			delta.CriticalConditionStatus = condition.Status
			break
		}
	}
	if state.CurrentFocus != nil {
		delta.ExpectedCheckpointID = stringPointer(state.CurrentFocus.EvidenceCheckpoint.CheckpointID)
		if state.CurrentFocus.EvidenceCheckpoint.Reached {
			delta.CheckpointOutcome = "passed"
		} else if len(state.CurrentFocus.FailureEvents) > 0 {
			delta.CheckpointOutcome = "failed"
		} else {
			delta.CheckpointOutcome = "pending"
		}
	}
	current := runtime.evidenceReferencesForCondition(state, delta.CriticalConditionID)
	if state.ReviewBaseline == nil {
		for _, ref := range current {
			delta.NewEvidenceIDs = append(delta.NewEvidenceIDs, ref.EvidenceID)
		}
		return delta
	}
	delta.BaselineID = stringPointer(state.ReviewBaseline.BaselineID)
	baseline := map[string]string{}
	for _, ref := range state.ReviewBaseline.EvidenceRefs {
		baseline[ref.EvidenceID] = ref.Hash
	}
	for _, ref := range current {
		if baseline[ref.EvidenceID] == ref.Hash {
			delta.ReusedEvidenceIDs = append(delta.ReusedEvidenceIDs, ref.EvidenceID)
		} else {
			delta.NewEvidenceIDs = append(delta.NewEvidenceIDs, ref.EvidenceID)
		}
		delete(baseline, ref.EvidenceID)
	}
	for evidenceID := range baseline {
		delta.InvalidatedEvidenceIDs = append(delta.InvalidatedEvidenceIDs, evidenceID)
	}
	for _, invalidated := range state.InvalidatedEvidence {
		delta.InvalidatedEvidenceIDs = append(delta.InvalidatedEvidenceIDs, invalidated.EvidenceID)
	}
	delta.NewEvidenceIDs = normalizeStrings(delta.NewEvidenceIDs)
	delta.ReusedEvidenceIDs = normalizeStrings(delta.ReusedEvidenceIDs)
	delta.InvalidatedEvidenceIDs = normalizeStrings(delta.InvalidatedEvidenceIDs)
	baselineFocusID, baselineExecutionID := valueOr(state.ReviewBaseline.FocusID, ""), valueOr(state.ReviewBaseline.FocusExecutionID, "")
	currentFocusID, currentExecutionID := "", ""
	if state.CurrentFocus != nil {
		currentFocusID, currentExecutionID = state.CurrentFocus.FocusID, state.CurrentFocus.ExecutionID
	}
	delta.ExecutionIdentityChanged = baselineFocusID != currentFocusID || baselineExecutionID != currentExecutionID
	advanced, regressed := completionConditionMovement(state.ReviewBaseline.Conditions, state.CompletionConditions)
	if len(delta.InvalidatedEvidenceIDs) > 0 || regressed {
		delta.NetProgress = "regressed"
	} else if len(delta.NewEvidenceIDs) > 0 || advanced || (delta.CheckpointOutcome == "passed" && state.ReviewBaseline.CheckpointOutcome != "passed") {
		delta.NetProgress = "advanced"
	} else {
		delta.NetProgress = "zero"
	}
	return delta
}

func completionConditionMovement(baseline, current []CompletionCondition) (advanced, regressed bool) {
	previous := map[string]int{}
	for _, condition := range baseline {
		previous[condition.ConditionID] = conditionRank(condition.Status)
	}
	for _, condition := range current {
		before, exists := previous[condition.ConditionID]
		if !exists {
			continue
		}
		after := conditionRank(condition.Status)
		if after > before {
			advanced = true
		} else if after < before {
			regressed = true
		}
	}
	return advanced, regressed
}

func conditionRank(status string) int {
	switch status {
	case "met":
		return 3
	case "in_progress":
		return 2
	case "evidence_insufficient":
		return 1
	default:
		return 0
	}
}

func (runtime *Runtime) baselineFromState(state *State, reviewID string) *ReviewBaseline {
	baseline := &ReviewBaseline{
		BaselineID: newID("baseline"), ReviewID: reviewID, AuthorityHash: state.AuthorityHash,
		Conditions:   append([]CompletionCondition(nil), state.CompletionConditions...),
		EvidenceRefs: runtime.evidenceReferencesForCondition(state, criticalConditionID(state)), CheckpointOutcome: "not_applicable",
		EstablishedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if state.CurrentFocus != nil {
		baseline.FocusID = stringPointer(state.CurrentFocus.FocusID)
		baseline.FocusSnapshotHash = stringPointer(state.CurrentFocus.SnapshotHash)
		baseline.FocusExecutionID = stringPointer(state.CurrentFocus.ExecutionID)
		baseline.CheckpointID = stringPointer(state.CurrentFocus.EvidenceCheckpoint.CheckpointID)
		if state.CurrentFocus.EvidenceCheckpoint.Reached {
			baseline.CheckpointOutcome = "passed"
		} else if len(state.CurrentFocus.FailureEvents) > 0 {
			baseline.CheckpointOutcome = "failed"
		} else {
			baseline.CheckpointOutcome = "pending"
		}
	}
	state.InvalidatedEvidence = []EvidenceInvalidation{}
	return baseline
}

func criticalConditionID(state *State) string {
	if state == nil {
		return "none"
	}
	for _, condition := range state.CompletionConditions {
		if condition.Status != "met" {
			return condition.ConditionID
		}
	}
	return "none"
}

func (runtime *Runtime) evidenceReferencesForCondition(state *State, conditionID string) []EvidenceReference {
	if state == nil || conditionID == "none" {
		return []EvidenceReference{}
	}
	relevant := map[string]struct{}{}
	for _, condition := range state.CompletionConditions {
		if condition.ConditionID == conditionID {
			for _, evidenceID := range condition.EvidenceIDs {
				relevant[evidenceID] = struct{}{}
			}
			break
		}
	}
	if state.CurrentFocus != nil && state.CurrentFocus.ConditionID == conditionID {
		for _, evidenceID := range state.CurrentFocus.ExpectedEvidence {
			relevant[evidenceID] = struct{}{}
		}
	}
	refs := []EvidenceReference{}
	for _, result := range state.ReusableResults {
		_, selected := relevant[result.ResultID]
		if !selected {
			for _, scope := range result.Scope {
				if scope == conditionID {
					selected = true
					break
				}
			}
		}
		if !selected {
			continue
		}
		path := resolvePath(runtime.Root, result.EvidencePath)
		hash, err := fileHash(path)
		if err == nil && hash == result.InputHash && runtime.allowedEvidencePath(path) {
			refs = append(refs, EvidenceReference{EvidenceID: result.ResultID, Path: result.EvidencePath, Hash: hash})
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].EvidenceID+"\x00"+refs[i].Path+"\x00"+refs[i].Hash < refs[j].EvidenceID+"\x00"+refs[j].Path+"\x00"+refs[j].Hash
	})
	return refs
}

func mergePendingReview(state *State, trigger ReviewTrigger) error {
	if state == nil {
		return errors.New("governance state is required")
	}
	pending := state.Review.Pending
	if pending == nil {
		pending = &PendingReview{TriggerTypes: []string{}, SourceIDs: []string{}}
	}
	pending.TriggerTypes = appendBoundedUnique(pending.TriggerTypes, trigger.Type, pendingReviewLimit)
	pending.SourceIDs = appendBoundedUnique(pending.SourceIDs, trigger.SourceID, pendingReviewLimit)
	pending.Reason = trigger.Reason
	hash, err := hashJSON(map[string]any{"trigger_types": pending.TriggerTypes, "source_ids": pending.SourceIDs})
	if err != nil {
		return err
	}
	pending.FactsHash = hash
	state.Review.Pending = pending
	return nil
}

func appendBoundedUnique(values []string, value string, limit int) []string {
	value = strings.TrimSpace(value)
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	values = append(values, value)
	if len(values) > limit {
		values = values[len(values)-limit:]
	}
	return values
}

func pendingReviewTrigger(pending PendingReview) (ReviewTrigger, error) {
	return newReviewTrigger("event", "pending-events", pending.FactsHash, pending.Reason)
}

func (runtime *Runtime) verifyReviewIntegrity(request *ReviewRequest, state *State) error {
	if request == nil || state == nil || state.Review.ReviewID == nil || state.Review.TriggerInstanceID == nil || state.Review.ReviewSnapshotHash == nil {
		return errors.New("review request is not bound to current governance state")
	}
	if request.ReviewID != *state.Review.ReviewID || request.TriggerInstanceID != *state.Review.TriggerInstanceID || request.ReviewSnapshotHash != *state.Review.ReviewSnapshotHash {
		return errors.New("review request identity does not match governance state")
	}
	copy := *request
	copy.ReviewSnapshotHash = ""
	hash, err := hashJSON(&copy)
	if err != nil {
		return err
	}
	if hash != request.ReviewSnapshotHash {
		return errors.New("review request snapshot hash is invalid")
	}
	return nil
}

func (runtime *Runtime) reviewStillApplicable(request *ReviewRequest, state *State) bool {
	if request == nil || state == nil {
		return false
	}
	currentAuthority, err := runtime.authorityReferences()
	if err != nil || !authorityRefsEqual(request.AuthorityRefs, currentAuthority) {
		return false
	}
	if (request.CurrentFocus == nil) != (state.CurrentFocus == nil) {
		return false
	}
	if request.CurrentFocus != nil && (request.CurrentFocus.FocusID != state.CurrentFocus.FocusID || request.CurrentFocus.SnapshotHash != state.CurrentFocus.SnapshotHash || request.CurrentFocus.ExecutionID != state.CurrentFocus.ExecutionID) {
		return false
	}
	requestedRelevant := []EvidenceReference{}
	requestIDs := map[string]struct{}{}
	for _, evidenceID := range append(append([]string{}, request.ProgressDelta.NewEvidenceIDs...), request.ProgressDelta.ReusedEvidenceIDs...) {
		requestIDs[evidenceID] = struct{}{}
	}
	for _, ref := range request.EvidenceRefs {
		if _, exists := requestIDs[ref.EvidenceID]; exists {
			requestedRelevant = append(requestedRelevant, ref)
		}
	}
	requestEvidence, _ := hashJSON(requestedRelevant)
	currentEvidence, _ := hashJSON(runtime.evidenceReferencesForCondition(state, request.ProgressDelta.CriticalConditionID))
	if requestEvidence != currentEvidence {
		return false
	}
	requestCheckpoint, _ := hashJSON(request.RecentCheckpoint)
	currentCheckpoint, _ := hashJSON(recentCheckpointForState(state))
	if requestCheckpoint != currentCheckpoint {
		return false
	}
	return true
}

func authorityRefsEqual(left, right []AuthorityReference) bool {
	leftHash, _ := hashJSON(left)
	rightHash, _ := hashJSON(right)
	return leftHash == rightHash
}

func (runtime *Runtime) validateReviewResultForRequest(result *ReviewResult, request *ReviewRequest) error {
	if result == nil || request == nil {
		return errors.New("Governor feedback and request are required")
	}
	if err := ensureReferencedEvidence(request, append(append([]string(nil), result.ValidatedEvidenceIDs...), result.PreservedResultIDs...)); err != nil {
		return err
	}
	authorities := map[string]string{}
	for _, ref := range request.AuthorityRefs {
		authorities[filepath.ToSlash(ref.Path)] = ref.Hash
	}
	for _, claim := range result.AuthorityClaims {
		path := filepath.ToSlash(strings.TrimSpace(claim.SourcePath))
		if authorities[path] == "" || authorities[path] != claim.SourceHash {
			return fmt.Errorf("authority claim source %q is not bound to the review request", claim.SourcePath)
		}
		resolved := resolvePath(runtime.Root, path)
		currentHash, err := fileHash(resolved)
		if err != nil || currentHash != claim.SourceHash {
			return fmt.Errorf("authority claim source %q is no longer available at its reviewed identity", claim.SourcePath)
		}
		if err := validateStableLocator(resolved, claim.StableLocator); err != nil {
			return fmt.Errorf("authority claim source %q: %w", claim.SourcePath, err)
		}
	}
	return nil
}

func validateStableLocator(path, locator string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	locator = strings.TrimSpace(locator)
	switch {
	case strings.HasPrefix(locator, "heading:"):
		expected := strings.TrimSpace(strings.TrimPrefix(locator, "heading:"))
		if expected == "" {
			return errors.New("heading locator is empty")
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") && strings.TrimSpace(strings.TrimLeft(trimmed, "#")) == expected {
				return nil
			}
		}
		return fmt.Errorf("heading locator %q does not exist", expected)
	case strings.HasPrefix(locator, "anchor:"):
		expected := strings.TrimSpace(strings.TrimPrefix(locator, "anchor:"))
		if expected == "" {
			return errors.New("anchor locator is empty")
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			if strings.TrimSpace(line) == expected {
				return nil
			}
		}
		return fmt.Errorf("anchor locator %q does not exist as an exact line", expected)
	case strings.HasPrefix(locator, "json-pointer:"):
		pointer := strings.TrimSpace(strings.TrimPrefix(locator, "json-pointer:"))
		if pointer == "" {
			return errors.New("JSON pointer locator is empty")
		}
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			return errors.New("JSON pointer locator requires a JSON authority source")
		}
		if !jsonPointerExists(document, pointer) {
			return fmt.Errorf("JSON pointer locator %q does not exist", pointer)
		}
		return nil
	default:
		return errors.New("stable_locator must use heading:, anchor:, or json-pointer:")
	}
}

func jsonPointerExists(document any, pointer string) bool {
	if pointer == "" {
		return true
	}
	if !strings.HasPrefix(pointer, "/") {
		return false
	}
	current := document
	for _, encoded := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		key := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch typed := current.(type) {
		case map[string]any:
			value, exists := typed[key]
			if !exists {
				return false
			}
			current = value
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(typed) {
				return false
			}
			current = typed[index]
		default:
			return false
		}
	}
	return true
}

func sortedEvidenceIDs(refs []EvidenceReference) []string {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.EvidenceID)
	}
	sort.Strings(ids)
	return ids
}
