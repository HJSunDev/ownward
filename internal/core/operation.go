package core

import (
	"context"
	"errors"
	"hash/fnv"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

func (s *Service) OperationGeneration() uint64 {
	if a, ok := s.authority.(contract.MutationAuthority); ok {
		return a.OperationGeneration()
	}
	return 0
}

func (s *Service) lockOperation(ctx context.Context) func() {
	op, ok := contract.Operation(ctx)
	if !ok {
		return func() {}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(op.System + op.Principal + op.ID))
	mu := &s.operationMu[h.Sum32()%uint32(len(s.operationMu))]
	mu.Lock()
	return mu.Unlock
}

func (s *Service) replayMutation(ctx context.Context) ([]MutationBatchResult, bool, error) {
	op, ok := contract.Operation(ctx)
	if !ok {
		return nil, false, nil
	}
	authority, ok := s.authority.(contract.MutationAuthority)
	if !ok {
		return nil, false, errors.New("当前资产存储不支持断线提交接续")
	}
	r, found, err := authority.MutationReceipt(op)
	if err != nil || !found {
		return nil, found, err
	}
	results := make([]MutationBatchResult, len(r.Results))
	for i, item := range r.Results {
		if item.Error != "" {
			results[i].Error = item.Error
			continue
		}
		value, exists := s.authority.ReadVersion(item.Asset.ID, item.Asset.Revision)
		if !exists {
			results[i].Error = "操作已提交，原结果已更新或遗忘"
			continue
		}
		state, err := s.Organization(value.ID)
		if err != nil {
			state = OrganizationState{Status: "pending"}
		}
		s.index.Upsert(value)
		if s.collaborative && state.Status != "ready" {
			state = s.prepareSemanticWork(ctx, value)
		}
		results[i].Result = &MutationResult{Information: value, Organization: state}
	}
	return results, true, nil
}

func replaySingle(results []MutationBatchResult) (MutationResult, error) {
	if len(results) != 1 {
		return MutationResult{}, errors.New("操作结果类型不一致")
	}
	if results[0].Error != "" {
		return MutationResult{}, errors.New(results[0].Error)
	}
	return *results[0].Result, nil
}

func (s *Service) commitMutation(ctx context.Context, values []domain.Information, expected []uint64, results []MutationBatchResult, positions []int, fallback func() error) error {
	op, ok := contract.Operation(ctx)
	if !ok {
		return contract.Commit(ctx, fallback)
	}
	authority, ok := s.authority.(contract.MutationAuthority)
	if !ok {
		return errors.New("当前资产存储不支持断线提交接续")
	}
	r := contract.MutationReceipt{Operation: op, Results: make([]contract.MutationOutcome, len(results))}
	for i, item := range results {
		r.Results[i].Error = item.Error
	}
	for i, v := range values {
		r.Results[positions[i]].Asset = contract.AssetVersion{ID: v.ID, Revision: v.Revision}
	}
	return contract.Commit(ctx, func() error { return authority.CommitMutation(r, values, expected) })
}
