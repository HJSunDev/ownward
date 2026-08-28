//go:build ownward_migration

package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
)

// BuildIsolatedDerivedGeneration runs the current kernel's real organization,
// representation and indexing implementation against an immutable authority
// snapshot. It writes only a new, invisible derived generation and never
// consults the active derived generation.
func (s *Service) BuildIsolatedDerivedGeneration(ctx context.Context, generation string, snapshot []domain.Information) (*derived.Store, map[string]int, error) {
	if s == nil || !s.collaborative || s.derivedStore == nil || s.embedder == nil {
		return nil, nil, errors.New("当前内核不支持 collaborative 派生候选构建")
	}
	values := append([]domain.Information(nil), snapshot...)
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	for index, value := range values {
		if value.ID == "" || value.Revision == 0 || (index > 0 && values[index-1].ID == value.ID) {
			return nil, nil, errors.New("派生候选资产快照无效")
		}
	}
	next, _, counts, err := s.buildCollaborativeGeneration(ctx, generation, values, nil)
	return next, counts, err
}

// BuildIsolatedCollaborativeGeneration is the candidate-binary boundary used
// by the offline lifecycle. Unlike constructing a Service on the active state,
// it neither opens nor reads the active generation.
func BuildIsolatedCollaborativeGeneration(ctx context.Context, embedder contract.VectorCapability, root, generation string, snapshot []domain.Information) (*derived.Store, error) {
	if embedder == nil {
		return nil, errors.New("派生候选缺少向量能力")
	}
	values := append([]domain.Information(nil), snapshot...)
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	authority, err := newCandidateSnapshotAuthority(values)
	if err != nil {
		return nil, err
	}
	anchor, err := derived.CandidateRoot(root)
	if err != nil {
		return nil, err
	}
	service := &Service{authority: authority, derivedStore: anchor, embedder: embedder, collaborative: true, now: time.Now}
	next, _, _, err := service.buildCollaborativeGeneration(ctx, generation, values, nil)
	return next, err
}

// CatchUpIsolatedCollaborativeGeneration applies an accepted ChangeScope to a
// candidate store without taking ownership of the shared vector capability.
func CatchUpIsolatedCollaborativeGeneration(ctx context.Context, embedder contract.VectorCapability, store *derived.Store, snapshot []domain.Information, scope contract.ChangeScope) error {
	authority, err := newCandidateSnapshotAuthority(snapshot)
	if err != nil {
		return err
	}
	service, err := NewCollaborativeWithAuthority(authority, store, embedder)
	if err != nil {
		return err
	}
	return service.ApplyAcceptedChanges(ctx, scope)
}

// readOnlyCandidateAuthority makes the lifecycle's lack of authority write
// ownership mechanical even though the stable AssetAuthority contract also
// contains product mutation operations.
type readOnlyCandidateAuthority struct {
	values map[string]domain.Information
}

func newCandidateSnapshotAuthority(values []domain.Information) (readOnlyCandidateAuthority, error) {
	result := readOnlyCandidateAuthority{values: make(map[string]domain.Information, len(values))}
	for _, value := range values {
		if value.ID == "" || value.Revision == 0 {
			return readOnlyCandidateAuthority{}, errors.New("派生候选资产快照无效")
		}
		if _, exists := result.values[value.ID]; exists {
			return readOnlyCandidateAuthority{}, errors.New("派生候选资产快照包含重复身份")
		}
		result.values[value.ID] = value
	}
	return result, nil
}

func (readOnlyCandidateAuthority) CreateAsset(domain.Information) (contract.ChangeScope, error) {
	return contract.ChangeScope{}, errors.New("派生候选没有资产写权")
}

func (readOnlyCandidateAuthority) CreateAssets([]domain.Information) (contract.ChangeScope, error) {
	return contract.ChangeScope{}, errors.New("派生候选没有资产写权")
}

func (readOnlyCandidateAuthority) UpdateAsset(domain.Information, uint64) (contract.ChangeScope, error) {
	return contract.ChangeScope{}, errors.New("派生候选没有资产写权")
}

func (readOnlyCandidateAuthority) Sync() error {
	return errors.New("派生候选没有资产维护权")
}

func (readOnlyCandidateAuthority) Compact() error {
	return errors.New("派生候选没有资产维护权")
}

func (readOnlyCandidateAuthority) Backup(string) error {
	return errors.New("派生候选没有资产备份权")
}

func (authority readOnlyCandidateAuthority) ReadCurrent(id string) (domain.Information, bool) {
	value, exists := authority.values[id]
	return value, exists
}

func (authority readOnlyCandidateAuthority) ReadVersion(id string, revision uint64) (domain.Information, bool) {
	value, exists := authority.ReadCurrent(id)
	return value, exists && value.Revision == revision
}

func (authority readOnlyCandidateAuthority) ListCurrent() []domain.Information {
	result := make([]domain.Information, 0, len(authority.values))
	for _, value := range authority.values {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

// ApplyAcceptedChanges incrementally catches an isolated generation up using
// only the exact versions already accepted by the authority. It does not own
// or repeat the authority write and therefore cannot create a second asset
// truth. Callers bind s to the isolated generation before using this method.
func (s *Service) ApplyAcceptedChanges(ctx context.Context, scope contract.ChangeScope) error {
	if s == nil || s.derivedStore == nil || s.semantic == nil {
		return errors.New("派生候选尚未打开")
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	versions := append([]contract.AssetVersion(nil), scope.Assets...)
	sort.Slice(versions, func(left, right int) bool { return versions[left].ID < versions[right].ID })
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	for _, version := range versions {
		value, exists := s.authority.ReadVersion(version.ID, version.Revision)
		if !exists {
			return fmt.Errorf("权威资产版本已漂移: %s@%d", version.ID, version.Revision)
		}
		current, exists := s.derivedStore.Get(value.ID)
		if exists && current.AssetRevision == value.Revision {
			continue
		}
		dependents := appendUniqueIDs(s.semantic.Dependents(value.ID), s.semantic.PendingDependents(value.ID)...)
		s.index.Upsert(value)
		var state OrganizationState
		if s.collaborative {
			state = s.prepareSemanticWork(ctx, value)
		} else {
			state = s.organize(ctx, value)
		}
		if state.Status == "unavailable" {
			return fmt.Errorf("派生候选无法处理 %s@%d", value.ID, value.Revision)
		}
		if pending := s.refreshDependents(ctx, dependents); pending > 0 {
			// Pending is an existing safe degradation state, not a failed catch-up.
		}
		s.reindexDerived(dependents)
	}
	return nil
}
