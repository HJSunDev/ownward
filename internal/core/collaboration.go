package core

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

var ErrSemanticConflict = errors.New("当前语义工作已经接受了不同结果")

type SemanticSubmissionResult struct {
	WorkID       string            `json:"work_id"`
	Organization OrganizationState `json:"organization,omitempty"`
	Error        string            `json:"error,omitempty"`
}

func (s *Service) SemanticWork(_ context.Context, limit int) ([]semantics.Work, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if !s.collaborative || s.derivedStore == nil {
		return nil, errors.New("外部语义协作未启用")
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > 20 {
		return nil, errors.New("语义工作数量必须介于一和二十之间")
	}
	assets := s.store.All()
	sort.Slice(assets, func(left, right int) bool {
		if assets[left].UpdatedAt.Equal(assets[right].UpdatedAt) {
			return assets[left].ID < assets[right].ID
		}
		return assets[left].UpdatedAt.Before(assets[right].UpdatedAt)
	})
	result := make([]semantics.Work, 0, limit)
	for _, asset := range assets {
		record, exists := s.derivedStore.Get(asset.ID)
		if !exists || record.AssetRevision != asset.Revision || record.SemanticWork == nil || record.SemanticResult != nil {
			continue
		}
		result = append(result, *record.SemanticWork)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (s *Service) SemanticWorkFor(_ context.Context, assetIDs []string) ([]semantics.Work, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if !s.collaborative || s.derivedStore == nil {
		return nil, errors.New("外部语义协作未启用")
	}
	if len(assetIDs) == 0 || len(assetIDs) > 20 {
		return nil, errors.New("定向语义工作数量必须介于一和二十之间")
	}
	seen := make(map[string]struct{}, len(assetIDs))
	result := make([]semantics.Work, 0, len(assetIDs))
	for _, rawID := range assetIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, errors.New("定向语义工作包含空资产标识")
		}
		if _, exists := seen[id]; exists {
			return nil, errors.New("定向语义工作包含重复资产标识")
		}
		seen[id] = struct{}{}
		asset, exists := s.store.Get(id)
		if !exists {
			return nil, errors.New("定向语义工作的资产不存在")
		}
		record, exists := s.derivedStore.Get(id)
		if !exists || record.AssetRevision != asset.Revision || record.SemanticWork == nil || record.SemanticResult != nil {
			continue
		}
		result = append(result, *record.SemanticWork)
	}
	return result, nil
}

func (s *Service) SubmitSemantic(_ context.Context, input semantics.Submission) (OrganizationState, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if !s.collaborative || s.derivedStore == nil || s.semantic == nil {
		return OrganizationState{}, errors.New("外部语义协作未启用")
	}
	unlock := s.lockMutation(input.AssetID)
	defer unlock()
	asset, exists := s.store.Get(strings.TrimSpace(input.AssetID))
	if !exists {
		return OrganizationState{}, errors.New("语义工作的资产不存在")
	}
	record, exists := s.derivedStore.GetWithEmbedding(asset.ID)
	if !exists || record.AssetRevision != asset.Revision || record.SemanticWork == nil {
		return OrganizationState{}, errors.New("语义工作不存在或已经过期")
	}
	normalized, err := semantics.NormalizeSubmission(*record.SemanticWork, input, s.now())
	if err != nil {
		return OrganizationState{}, err
	}
	if record.SemanticResult != nil {
		if semantics.SameSubmission(*record.SemanticResult, normalized) {
			return organizationState(record), nil
		}
		return OrganizationState{}, ErrSemanticConflict
	}
	accepted := normalized
	incoming := make([]semantics.Relation, 0, len(normalized.Analysis.Relations))
	outgoing := make([]semantics.Relation, 0, len(normalized.Analysis.Relations))
	explicitTargets := make(map[string]struct{}, len(asset.Relations))
	for _, relation := range asset.Relations {
		explicitTargets[relation.TargetID] = struct{}{}
	}
	for _, relation := range normalized.Analysis.Relations {
		if relation.Direction == "incoming" {
			if _, explicit := explicitTargets[relation.TargetID]; !explicit {
				incoming = append(incoming, relation)
			}
			continue
		}
		relation.Direction = ""
		outgoing = append(outgoing, relation)
	}
	if s.disableRelations {
		incoming = nil
		normalized.Analysis.Relations = nil
	} else {
		normalized.Analysis.Relations = s.finalizeRelations(asset, outgoing)
	}
	record.GeneratedAt = s.now().UTC()
	record.Provider = "semantic:" + normalized.Capability.ID + "/" + normalized.Capability.Version
	record.Analysis = normalized.Analysis
	record.SemanticResult = &accepted
	record.Status = "ready"
	record.Error = ""
	if normalized.Status == semantics.SubmissionUncertain {
		record.Status = "uncertain"
		record.Error = normalized.Uncertainty
	}
	if len(record.Embedding) == 0 {
		record.Status = "pending"
		record.Error = strings.Trim(strings.Join([]string{record.Error, "向量仍待生成"}, "; "), "; ")
	}
	current, exists := s.store.Get(asset.ID)
	if !exists || current.Revision != asset.Revision {
		return OrganizationState{}, errors.New("语义工作已被新的资产版本取代")
	}
	s.graphMu.Lock()
	defer s.graphMu.Unlock()
	previousDependents := s.semantic.Dependents(asset.ID)
	if err := s.derivedStore.Put(record); err != nil {
		return OrganizationState{}, err
	}
	s.semantic.Upsert(record)
	if !s.disableRelations {
		if err := s.applyIncomingRelationsLocked(asset, previousDependents, incoming); err != nil {
			return OrganizationState{}, err
		}
	}
	return organizationState(record), nil
}

func (s *Service) SubmitSemanticBatch(ctx context.Context, inputs []semantics.Submission) ([]SemanticSubmissionResult, error) {
	if len(inputs) == 0 || len(inputs) > 20 {
		return nil, errors.New("批量语义结果数量必须介于一和二十之间")
	}
	results := make([]SemanticSubmissionResult, len(inputs))
	for index, input := range inputs {
		state, err := s.SubmitSemantic(ctx, input)
		results[index] = SemanticSubmissionResult{WorkID: input.WorkID, Organization: state}
		if err != nil {
			results[index].Error = err.Error()
		}
	}
	return results, nil
}

func (s *Service) prepareSemanticWork(ctx context.Context, value domain.Information) OrganizationState {
	if !s.collaborative || s.derivedStore == nil || s.semantic == nil || s.embedder == nil {
		return OrganizationState{Status: "unavailable"}
	}
	vectors, embeddingErr := s.embedder.EmbedDocuments(ctx, []string{value.Content})
	var vector []float32
	if len(vectors) == 1 {
		vector = vectors[0]
	} else if embeddingErr == nil {
		embeddingErr = errors.New("本地向量能力返回数量无效")
	}
	return s.prepareSemanticWorkWithVector(value, vector, embeddingErr)
}

func (s *Service) prepareSemanticWorkBatch(ctx context.Context, values []domain.Information) []OrganizationState {
	states := make([]OrganizationState, len(values))
	if len(values) == 0 {
		return states
	}
	if !s.collaborative || s.derivedStore == nil || s.semantic == nil || s.embedder == nil {
		for index := range states {
			states[index] = OrganizationState{Status: "unavailable"}
		}
		return states
	}
	contents := make([]string, len(values))
	for index, value := range values {
		contents[index] = value.Content
	}
	vectors, embeddingErr := s.embedder.EmbedDocuments(ctx, contents)
	if len(vectors) != len(values) && embeddingErr == nil {
		embeddingErr = errors.New("本地向量能力返回数量无效")
	}
	for index, value := range values {
		var vector []float32
		if embeddingErr == nil {
			vector = vectors[index]
		}
		states[index] = s.prepareSemanticWorkWithVector(value, vector, embeddingErr)
	}
	return states
}

func (s *Service) prepareSemanticWorkWithVector(value domain.Information, vector []float32, embeddingErr error) OrganizationState {
	previousDependents := s.semantic.Dependents(value.ID)
	var vectors [][]float32
	if len(vector) > 0 {
		vectors = [][]float32{vector}
	}
	candidates := s.semanticCandidates(value, vectors)
	var previous *semantics.Analysis
	if current, exists := s.derivedStore.Get(value.ID); exists {
		analysis := current.Analysis
		previous = &analysis
	}
	work, workErr := semantics.NewWork(s.derivedStore.Generation(), value, candidates, previous, s.now())
	if workErr != nil {
		return OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: workErr.Error()}
	}
	record := derived.Record{
		AssetID:        value.ID,
		AssetRevision:  value.Revision,
		GeneratedAt:    s.now().UTC(),
		Provider:       s.embedder.Name(),
		Status:         "pending",
		SemanticWork:   &work,
		EmbeddingSpace: s.embedder.Space().ID,
	}
	if len(vector) > 0 {
		record.Embedding = vector
	} else if embeddingErr == nil {
		embeddingErr = errors.New("本地向量能力返回空向量")
	}
	if embeddingErr != nil {
		record.Error = embeddingErr.Error()
	}
	current, exists := s.store.Get(value.ID)
	if !exists || current.Revision != value.Revision {
		return OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: "信息已被更新版本取代"}
	}
	s.graphMu.Lock()
	defer s.graphMu.Unlock()
	if err := s.derivedStore.Put(record); err != nil {
		return OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: err.Error()}
	}
	s.semantic.Upsert(record)
	if !s.disableRelations {
		if err := s.applyIncomingRelationsLocked(value, previousDependents, nil); err != nil {
			record.Error = strings.Trim(strings.Join([]string{record.Error, err.Error()}, "; "), "; ")
			_ = s.derivedStore.Put(record)
			s.semantic.Upsert(record)
		}
	}
	return OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: record.Error}
}

func (s *Service) semanticCandidates(value domain.Information, vectors [][]float32) []semantics.Candidate {
	lexical := s.index.Search(value.Content, nil, 24)
	result := make([]semantics.Candidate, 0, 12)
	positions := make(map[string]int, 12)
	appendCandidate := func(candidate domain.Information, similarity float64) {
		if candidate.ID == value.ID {
			return
		}
		if position, exists := positions[candidate.ID]; exists {
			if similarity > result[position].Similarity {
				result[position].Similarity = similarity
			}
			return
		}
		if len(result) == 12 {
			return
		}
		positions[candidate.ID] = len(result)
		result = append(result, semantics.Candidate{
			ID: candidate.ID, Revision: candidate.Revision, Content: candidate.Content,
			Contexts: append([]domain.Context(nil), candidate.Contexts...), Similarity: similarity,
		})
	}
	for _, relation := range value.Relations {
		if candidate, exists := s.store.Get(relation.TargetID); exists {
			appendCandidate(candidate, 0)
		}
	}
	for _, hit := range lexical {
		appendCandidate(hit.Information, 0)
		if len(result) >= 6 {
			break
		}
	}
	if len(vectors) == 1 && len(vectors[0]) > 0 {
		for _, hit := range s.semantic.Search(vectors[0], nil, 24) {
			candidate, exists := s.store.Get(hit.AssetID)
			record, recordExists := s.semantic.Get(hit.AssetID)
			if exists && recordExists && record.AssetRevision == candidate.Revision {
				appendCandidate(candidate, hit.Score)
			}
			if len(result) == 12 {
				break
			}
		}
	}
	for _, hit := range lexical {
		appendCandidate(hit.Information, 0)
		if len(result) == 12 {
			break
		}
	}
	return result
}

func organizationState(record derived.Record) OrganizationState {
	return OrganizationState{Status: record.Status, Provider: record.Provider, Error: record.Error}
}

func (s *Service) SemanticStatus() map[string]int {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	counts := map[string]int{"pending": 0, "ready": 0, "uncertain": 0}
	if s.derivedStore == nil {
		return counts
	}
	for _, record := range s.derivedStore.All() {
		if _, exists := counts[record.Status]; exists {
			counts[record.Status]++
		}
	}
	return counts
}

func (s *Service) Organization(id string) (OrganizationState, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	asset, exists := s.store.Get(strings.TrimSpace(id))
	if !exists {
		return OrganizationState{}, errors.New("信息不存在")
	}
	if s.derivedStore == nil {
		return OrganizationState{Status: "unavailable"}, nil
	}
	record, exists := s.derivedStore.Get(asset.ID)
	if !exists || record.AssetRevision != asset.Revision {
		return OrganizationState{Status: "pending"}, nil
	}
	return organizationState(record), nil
}
