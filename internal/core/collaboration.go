package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

var ErrSemanticConflict = errors.New("当前语义工作已经接受了不同结果")

const semanticWorkRequiredAction = "ownward_semantic_work"

// A byte bound is deliberately used instead of a tokenizer-specific estimate:
// byte length is a deterministic upper bound for byte-fallback tokenization and
// keeps every request comfortably below the 512-token production embedding
// window. Long assets remain authoritative and are represented after semantic
// submission rather than being truncated for the embedding transport.
const semanticEmbeddingChunkBytes = 320

type SemanticSubmissionResult = contract.SemanticSubmissionResult

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
	assets := s.authority.ListCurrent()
	sort.Slice(assets, func(left, right int) bool {
		if assets[left].UpdatedAt.Equal(assets[right].UpdatedAt) {
			return assets[left].ID < assets[right].ID
		}
		return assets[left].UpdatedAt.Before(assets[right].UpdatedAt)
	})
	result := make([]semantics.Work, 0, limit)
	for _, asset := range assets {
		record, exists := s.derivedStore.Get(asset.ID)
		if !exists || record.AssetRevision != asset.Revision || !record.HasPendingSemanticWork() {
			continue
		}
		work, err := s.resolveSemanticWork(record)
		if err != nil {
			return nil, err
		}
		result = append(result, work)
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
		asset, exists := s.authority.ReadCurrent(id)
		if !exists {
			return nil, errors.New("定向语义工作的资产不存在")
		}
		record, exists := s.derivedStore.Get(id)
		if !exists || record.AssetRevision != asset.Revision || !record.HasPendingSemanticWork() {
			continue
		}
		work, err := s.resolveSemanticWork(record)
		if err != nil {
			return nil, err
		}
		result = append(result, work)
	}
	return result, nil
}

func (s *Service) resolveSemanticWork(record derived.Record) (semantics.Work, error) {
	if record.SemanticWorkReference == nil {
		return semantics.Work{}, errors.New("语义工作引用不存在")
	}
	asset, exists := s.authority.ReadCurrent(record.SemanticWorkReference.AssetID)
	if !exists {
		return semantics.Work{}, errors.New("语义工作的权威资产不存在")
	}
	candidates := make([]domain.Information, 0, len(record.SemanticWorkReference.Candidates))
	for _, reference := range record.SemanticWorkReference.Candidates {
		candidate, exists := s.authority.ReadCurrent(reference.ID)
		if !exists {
			return semantics.Work{}, errors.New("语义工作的候选资产不存在")
		}
		candidates = append(candidates, candidate)
	}
	return semantics.ResolveWork(*record.SemanticWorkReference, asset, candidates)
}

func (s *Service) SubmitSemantic(ctx context.Context, input semantics.Submission) (OrganizationState, error) {
	recoveries := s.prepareSemanticVectorRecoveries(ctx, []semantics.Submission{input})
	return s.submitSemanticWithRecovery(input, recoveries[0])
}

type semanticVectorRecovery struct {
	vector []float32
	err    error
}

func (s *Service) submitSemanticWithRecovery(input semantics.Submission, recovery semanticVectorRecovery) (OrganizationState, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if !s.collaborative || s.derivedStore == nil || s.semantic == nil {
		return OrganizationState{}, errors.New("外部语义协作未启用")
	}
	unlock := s.lockMutation(input.AssetID)
	defer unlock()
	asset, exists := s.authority.ReadCurrent(strings.TrimSpace(input.AssetID))
	if !exists {
		return OrganizationState{}, errors.New("语义工作的资产不存在")
	}
	record, exists := s.derivedStore.GetWithEmbedding(asset.ID)
	if !exists || record.AssetRevision != asset.Revision || record.SemanticWorkReference == nil {
		return OrganizationState{}, errors.New("语义工作不存在或已经过期")
	}
	normalized, err := semantics.NormalizeSubmissionReference(*record.SemanticWorkReference, asset, input, s.now())
	if err != nil {
		return OrganizationState{}, err
	}
	if record.SemanticReceipt != nil {
		if !record.SemanticReceipt.Matches(normalized) {
			return OrganizationState{}, ErrSemanticConflict
		}
		if len(record.Embedding) > 0 || len(recovery.vector) == 0 {
			return organizationState(record), nil
		}
		record.Embedding = append([]float32(nil), recovery.vector...)
		record.EmbeddingSpace = s.embedder.Space().ID
		record.Status = "ready"
		record.Error = ""
		if record.SemanticReceipt.Status == semantics.SubmissionUncertain {
			record.Status = "uncertain"
			record.Error = record.SemanticReceipt.Uncertainty
		}
		s.graphMu.Lock()
		defer s.graphMu.Unlock()
		if err := s.derivedStore.Put(record); err != nil {
			return OrganizationState{}, err
		}
		s.semantic.Upsert(record)
		return organizationState(record), nil
	}
	accepted := normalized
	receipt, err := semantics.NewSubmissionReceipt(accepted)
	if err != nil {
		return OrganizationState{}, err
	}
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
	normalized.Analysis.Relations = s.finalizeRelations(asset, outgoing)
	record.GeneratedAt = s.now().UTC()
	record.Provider = "semantic:" + normalized.Capability.ID + "/" + normalized.Capability.Version
	record.Analysis = normalized.Analysis
	record.SemanticReceipt = &receipt
	if len(record.Embedding) == 0 && len(recovery.vector) > 0 {
		record.Embedding = append([]float32(nil), recovery.vector...)
		record.EmbeddingSpace = s.embedder.Space().ID
	}
	record.Status = "ready"
	record.Error = ""
	if normalized.Status == semantics.SubmissionUncertain {
		record.Status = "uncertain"
		record.Error = normalized.Uncertainty
	}
	if len(record.Embedding) == 0 {
		record.Status = "pending"
		reason := "向量仍待生成"
		if recovery.err != nil {
			reason = "语义检索表示生成失败: " + recovery.err.Error()
		}
		record.Error = strings.Trim(strings.Join([]string{record.Error, reason}, "; "), "; ")
	}
	current, exists := s.authority.ReadCurrent(asset.ID)
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
	if err := s.applyIncomingRelationsLocked(asset, previousDependents, incoming); err != nil {
		return OrganizationState{}, err
	}
	return organizationState(record), nil
}

func (s *Service) SubmitSemanticBatch(ctx context.Context, inputs []semantics.Submission) ([]SemanticSubmissionResult, error) {
	if len(inputs) == 0 || len(inputs) > 20 {
		return nil, errors.New("批量语义结果数量必须介于一和二十之间")
	}
	recoveries := s.prepareSemanticVectorRecoveries(ctx, inputs)
	results := make([]SemanticSubmissionResult, len(inputs))
	for index, input := range inputs {
		state, err := s.submitSemanticWithRecovery(input, recoveries[index])
		results[index] = SemanticSubmissionResult{WorkID: input.WorkID, Organization: state}
		if err != nil {
			results[index].Error = err.Error()
		}
	}
	return results, nil
}

func (s *Service) prepareSemanticVectorRecoveries(ctx context.Context, inputs []semantics.Submission) []semanticVectorRecovery {
	result := make([]semanticVectorRecovery, len(inputs))
	if s.embedder == nil || s.derivedStore == nil {
		return result
	}
	chunks := make([]string, 0)
	owners := make([]int, 0)
	expected := make([]int, len(inputs))
	s.stateMu.RLock()
	for index, input := range inputs {
		asset, exists := s.authority.ReadCurrent(strings.TrimSpace(input.AssetID))
		if !exists {
			continue
		}
		record, exists := s.derivedStore.GetWithEmbedding(asset.ID)
		if !exists || record.AssetRevision != asset.Revision || record.SemanticWorkReference == nil || len(record.Embedding) > 0 {
			continue
		}
		normalized, err := semantics.NormalizeSubmissionReference(*record.SemanticWorkReference, asset, input, s.now())
		if err != nil {
			result[index].err = err
			continue
		}
		if record.SemanticReceipt != nil && !record.SemanticReceipt.Matches(normalized) {
			result[index].err = ErrSemanticConflict
			continue
		}
		parts := semanticEmbeddingChunks(normalized.Analysis)
		expected[index] = len(parts)
		for _, part := range parts {
			chunks = append(chunks, part)
			owners = append(owners, index)
		}
	}
	s.stateMu.RUnlock()
	if len(chunks) == 0 {
		return result
	}
	collected, failures := s.embedBoundedDocumentGroups(ctx, chunks, owners, len(inputs))
	for index, failure := range failures {
		if failure != nil && result[index].err == nil {
			result[index].err = failure
		}
	}
	for index := range result {
		if result[index].err != nil || expected[index] == 0 {
			continue
		}
		if len(collected[index]) != expected[index] {
			result[index].err = errors.New("语义检索表示分块结果不完整")
			continue
		}
		result[index].vector, result[index].err = aggregateSemanticVectors(collected[index])
	}
	return result
}

func (s *Service) embedBoundedDocumentGroups(ctx context.Context, values []string, owners []int, ownerCount int) ([][][]float32, []error) {
	collected := make([][][]float32, ownerCount)
	failures := make([]error, ownerCount)
	if len(values) != len(owners) {
		for index := range failures {
			failures[index] = errors.New("语义检索表示输入身份不完整")
		}
		return collected, failures
	}
	for offset := 0; offset < len(values); {
		end := boundedEmbeddingBatchEnd(values, offset)
		vectors, err := s.embedder.EmbedDocuments(ctx, values[offset:end])
		if err != nil || len(vectors) != end-offset {
			if err == nil {
				err = errors.New("本地向量能力返回数量无效")
			}
			for _, owner := range owners[offset:end] {
				if owner >= 0 && owner < len(failures) && failures[owner] == nil {
					failures[owner] = err
				}
			}
			offset = end
			continue
		}
		for index, vector := range vectors {
			owner := owners[offset+index]
			if owner < 0 || owner >= len(collected) {
				continue
			}
			collected[owner] = append(collected[owner], vector)
		}
		offset = end
	}
	return collected, failures
}

func boundedEmbeddingBatchEnd(values []string, start int) int {
	end, total := start, 0
	for end < len(values) && end-start < 32 {
		size := len([]byte(values[end]))
		if end > start && total+size > semanticEmbeddingChunkBytes {
			break
		}
		total += size
		end++
	}
	return end
}

func semanticEmbeddingChunks(analysis semantics.Analysis) []string {
	lines := []string{"summary: " + analysis.Summary}
	for _, topic := range analysis.Topics {
		lines = append(lines, "topic: "+topic)
	}
	for _, cue := range analysis.Cues {
		lines = append(lines, "cue "+cue.Kind+": "+cue.Text)
	}
	for _, inferred := range analysis.Contexts {
		lines = append(lines, "context "+inferred.Key+"="+inferred.Value+": "+inferred.Evidence)
	}
	for _, relation := range analysis.Relations {
		lines = append(lines, "relation "+relation.Direction+" "+relation.Type+" "+relation.TargetID+": "+relation.Evidence)
	}
	chunks := make([]string, 0, len(lines))
	current := ""
	flush := func() {
		if strings.TrimSpace(current) != "" {
			chunks = append(chunks, current)
			current = ""
		}
	}
	for _, line := range lines {
		for _, part := range splitUTF8ByBytes(strings.TrimSpace(line), semanticEmbeddingChunkBytes) {
			if current == "" {
				current = part
				continue
			}
			if len([]byte(current))+1+len([]byte(part)) <= semanticEmbeddingChunkBytes {
				current += "\n" + part
				continue
			}
			flush()
			current = part
		}
	}
	flush()
	return chunks
}

func splitUTF8ByBytes(value string, maximum int) []string {
	if value == "" {
		return nil
	}
	parts := make([]string, 0, len([]byte(value))/maximum+1)
	start, size := 0, 0
	for index, current := range value {
		width := len(string(current))
		if size > 0 && size+width > maximum {
			parts = append(parts, value[start:index])
			start, size = index, 0
		}
		size += width
	}
	parts = append(parts, value[start:])
	return parts
}

func aggregateSemanticVectors(vectors [][]float32) ([]float32, error) {
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil, errors.New("语义检索表示没有向量")
	}
	result := make([]float32, len(vectors[0]))
	for _, vector := range vectors {
		if len(vector) != len(result) {
			return nil, errors.New("语义检索表示向量维度不一致")
		}
		for index, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, errors.New("语义检索表示包含非有限向量")
			}
			result[index] += value
		}
	}
	norm := float64(0)
	for _, value := range result {
		norm += float64(value * value)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return nil, fmt.Errorf("语义检索表示聚合为零向量")
	}
	for index := range result {
		result[index] = float32(float64(result[index]) / norm)
	}
	return result, nil
}

func (s *Service) prepareSemanticWork(ctx context.Context, value domain.Information) OrganizationState {
	if !s.collaborative || s.derivedStore == nil || s.semantic == nil || s.embedder == nil {
		return OrganizationState{Status: "unavailable"}
	}
	if len([]byte(value.Content)) > semanticEmbeddingChunkBytes {
		return s.prepareSemanticWorkWithVector(value, nil, errors.New("长信息等待语义结果生成检索表示"))
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
	vectors := make([][]float32, len(values))
	embeddingErrors := make([]error, len(values))
	contents := make([]string, 0, len(values))
	positions := make([]int, 0, len(values))
	for index, value := range values {
		if len([]byte(value.Content)) > semanticEmbeddingChunkBytes {
			embeddingErrors[index] = errors.New("长信息等待语义结果生成检索表示")
			continue
		}
		contents = append(contents, value.Content)
		positions = append(positions, index)
	}
	for offset := 0; offset < len(contents); {
		end := boundedEmbeddingBatchEnd(contents, offset)
		generated, embeddingErr := s.embedder.EmbedDocuments(ctx, contents[offset:end])
		if len(generated) != end-offset && embeddingErr == nil {
			embeddingErr = errors.New("本地向量能力返回数量无效")
		}
		for batchIndex, position := range positions[offset:end] {
			if embeddingErr != nil {
				embeddingErrors[position] = embeddingErr
				continue
			}
			vectors[position] = generated[batchIndex]
		}
		offset = end
	}
	staged := derived.NewIndex(nil)
	records := make([]derived.Record, 0, len(values))
	positionsByRecord := make([]int, 0, len(values))
	for index, value := range values {
		record, recordErr := s.newPendingSemanticRecord(value, vectors[index], embeddingErrors[index], s.semantic, staged)
		if recordErr != nil {
			states[index] = OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: recordErr.Error()}
			continue
		}
		current, exists := s.authority.ReadCurrent(value.ID)
		if !exists || current.Revision != value.Revision {
			states[index] = OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: "信息已被更新版本取代"}
			continue
		}
		records = append(records, record)
		positionsByRecord = append(positionsByRecord, index)
		staged.Upsert(record)
		states[index] = organizationState(record)
	}
	if len(records) == 0 {
		return states
	}
	s.graphMu.Lock()
	defer s.graphMu.Unlock()
	if err := s.derivedStore.PutBatch(records); err != nil {
		for _, position := range positionsByRecord {
			states[position] = OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: err.Error()}
		}
		return states
	}
	for _, record := range records {
		s.semantic.Upsert(record)
	}
	return states
}

func (s *Service) prepareSemanticWorkWithVector(value domain.Information, vector []float32, embeddingErr error) OrganizationState {
	previousDependents := s.semantic.Dependents(value.ID)
	record, err := s.newPendingSemanticRecord(value, vector, embeddingErr, s.semantic)
	if err != nil {
		return OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: err.Error()}
	}
	current, exists := s.authority.ReadCurrent(value.ID)
	if !exists || current.Revision != value.Revision {
		return OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: "信息已被更新版本取代"}
	}
	s.graphMu.Lock()
	defer s.graphMu.Unlock()
	if err := s.derivedStore.Put(record); err != nil {
		return OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: err.Error()}
	}
	s.semantic.Upsert(record)
	if err := s.applyIncomingRelationsLocked(value, previousDependents, nil); err != nil {
		record.Error = strings.Trim(strings.Join([]string{record.Error, err.Error()}, "; "), "; ")
		_ = s.derivedStore.Put(record)
		s.semantic.Upsert(record)
	}
	return OrganizationState{
		Status:         "pending",
		Provider:       "external-semantic-capability",
		Error:          record.Error,
		RequiredAction: semanticWorkRequiredAction,
	}
}

func (s *Service) newPendingSemanticRecord(value domain.Information, vector []float32, embeddingErr error, indexes ...*derived.Index) (derived.Record, error) {
	var vectors [][]float32
	if len(vector) > 0 {
		vectors = [][]float32{vector}
	}
	candidates := s.semanticCandidates(value, vectors, indexes...)
	var previous *semantics.Analysis
	if current, exists := s.derivedStore.Get(value.ID); exists {
		analysis := current.Analysis
		previous = &analysis
	}
	work, err := semantics.NewWork(s.derivedStore.Generation(), value, candidates, previous, s.now())
	if err != nil {
		return derived.Record{}, err
	}
	workReference, err := semantics.ReferenceWork(work)
	if err != nil {
		return derived.Record{}, err
	}
	record := derived.Record{
		AssetID: value.ID, AssetRevision: value.Revision, GeneratedAt: s.now().UTC(), Provider: s.embedder.Name(),
		Status: "pending", SemanticWorkReference: &workReference, EmbeddingSpace: s.embedder.Space().ID,
	}
	if len(vector) > 0 {
		record.Embedding = vector
	} else if embeddingErr == nil {
		embeddingErr = errors.New("本地向量能力返回空向量")
	}
	if embeddingErr != nil {
		record.Error = embeddingErr.Error()
	}
	return record, nil
}

func (s *Service) semanticCandidates(value domain.Information, vectors [][]float32, indexes ...*derived.Index) []semantics.Candidate {
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
		if candidate, exists := s.authority.ReadCurrent(relation.TargetID); exists {
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
		for _, hit := range mergedSemanticHits(vectors[0], indexes, 24) {
			candidate, exists := s.authority.ReadCurrent(hit.AssetID)
			record, recordExists := semanticRecordFromIndexes(hit.AssetID, indexes)
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

func semanticRecordFromIndexes(assetID string, indexes []*derived.Index) (derived.Record, bool) {
	for _, index := range indexes {
		if index == nil {
			continue
		}
		if record, exists := index.Get(assetID); exists {
			return record, true
		}
	}
	return derived.Record{}, false
}

func mergedSemanticHits(vector []float32, indexes []*derived.Index, limit int) []derived.SemanticHit {
	byID := make(map[string]derived.SemanticHit)
	for _, index := range indexes {
		if index == nil {
			continue
		}
		for _, hit := range index.Search(vector, nil, limit) {
			if current, exists := byID[hit.AssetID]; !exists || hit.Score > current.Score {
				byID[hit.AssetID] = hit
			}
		}
	}
	result := make([]derived.SemanticHit, 0, len(byID))
	for _, hit := range byID {
		result = append(result, hit)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Score == result[right].Score {
			return result[left].AssetID < result[right].AssetID
		}
		return result[left].Score > result[right].Score
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func organizationState(record derived.Record) OrganizationState {
	state := OrganizationState{Status: record.Status, Provider: record.Provider, Error: record.Error}
	if record.Status == "pending" && record.HasPendingSemanticWork() {
		state.RequiredAction = semanticWorkRequiredAction
	}
	return state
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
	asset, exists := s.authority.ReadCurrent(strings.TrimSpace(id))
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
