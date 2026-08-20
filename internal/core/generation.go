package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/retrieval"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func (s *Service) rebuildCollaborative(ctx context.Context) (map[string]int, error) {
	if s.embedder == nil {
		return nil, errors.New("向量能力边界不存在")
	}
	for attempt := 0; attempt < 3; attempt++ {
		s.stateMu.RLock()
		assets := s.store.All()
		currentGeneration := s.derivedStore.Generation()
		currentRecords := s.derivedStore.All()
		s.stateMu.RUnlock()
		assetDigest, err := informationSnapshotDigest(assets)
		if err != nil {
			return nil, err
		}
		stateDigest, err := recordSnapshotDigest(currentRecords)
		if err != nil {
			return nil, err
		}
		generation, err := derived.NewGenerationID(s.now())
		if err != nil {
			return nil, err
		}
		next, nextIndex, counts, err := s.buildCollaborativeGeneration(ctx, generation, assets, currentRecords)
		if err != nil {
			return nil, err
		}
		committed := false
		func() {
			s.stateMu.Lock()
			defer s.stateMu.Unlock()
			latestAssetDigest, digestErr := informationSnapshotDigest(s.store.All())
			latestStateDigest, stateErr := recordSnapshotDigest(s.derivedStore.All())
			if digestErr != nil || stateErr != nil || latestAssetDigest != assetDigest || latestStateDigest != stateDigest || s.derivedStore.Generation() != currentGeneration {
				return
			}
			s.graphMu.Lock()
			defer s.graphMu.Unlock()
			if err = s.derivedStore.CommitGeneration(next, derived.GenerationMetadata{
				AssetCount: len(assets), AssetSnapshot: assetDigest, EmbeddingSpace: s.embedder.Space().ID,
			}); err != nil {
				return
			}
			s.semantic = nextIndex
			committed = true
		}()
		if committed {
			return counts, nil
		}
		_ = next.Discard()
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("资产或组织状态持续变化，未切换派生世代")
}

func (s *Service) buildCollaborativeGeneration(ctx context.Context, generation string, assets []domain.Information, current []derived.Record) (*derived.Store, *derived.Index, map[string]int, error) {
	next, err := derived.CreateGeneration(s.derivedStore.Root(), generation)
	if err != nil {
		return nil, nil, nil, err
	}
	fail := func(err error) (*derived.Store, *derived.Index, map[string]int, error) {
		_ = next.Discard()
		return nil, nil, nil, err
	}
	currentByID := make(map[string]derived.Record, len(current))
	for _, record := range current {
		currentByID[record.AssetID] = record
	}
	records := make([]derived.Record, len(assets))
	allowUnavailable := len(current) == 0
	for start := 0; start < len(assets); start += 32 {
		end := min(start+32, len(assets))
		contents := make([]string, end-start)
		for index := start; index < end; index++ {
			contents[index-start] = assets[index].Content
		}
		vectors, embedErr := s.embedder.EmbedDocuments(ctx, contents)
		if embedErr != nil || len(vectors) != len(contents) {
			if embedErr == nil {
				embedErr = errors.New("本地向量能力返回数量无效")
			}
			if !allowUnavailable {
				return fail(embedErr)
			}
			vectors = make([][]float32, len(contents))
		}
		for offset, vector := range vectors {
			asset := assets[start+offset]
			if len(vector) > 0 && len(vector) != s.embedder.Space().Dimensions {
				return fail(errors.New("本地向量能力返回维度无效"))
			}
			record := derived.Record{
				AssetID: asset.ID, AssetRevision: asset.Revision, GeneratedAt: s.now().UTC(),
				Provider: s.embedder.Name(), Status: "pending", EmbeddingSpace: s.embedder.Space().ID,
				Embedding: append([]float32(nil), vector...),
			}
			if len(vector) == 0 && embedErr != nil {
				record.Error = embedErr.Error()
			}
			if previous, exists := currentByID[asset.ID]; exists && previous.AssetRevision == asset.Revision {
				record.Analysis = previous.Analysis
				record.SemanticWork = previous.SemanticWork
				record.SemanticResult = previous.SemanticResult
				if previous.SemanticResult != nil {
					record.Provider = previous.Provider
					record.Status = "ready"
					record.Error = ""
					if previous.SemanticResult.Status == semantics.SubmissionUncertain {
						record.Status = "uncertain"
						record.Error = previous.SemanticResult.Uncertainty
					}
				}
			}
			if s.disableRelations {
				record.Analysis.Relations = nil
			}
			records[start+offset] = record
		}
	}
	stagedIndex := derived.NewIndex(cloneRecordsWithEmbeddings(records))
	lexical := retrieval.NewLexical(assets)
	assetsByID := make(map[string]domain.Information, len(assets))
	for _, asset := range assets {
		assetsByID[asset.ID] = asset
	}
	for index, asset := range assets {
		if records[index].SemanticResult == nil {
			candidates := generationCandidates(asset, records[index].Embedding, lexical, stagedIndex, assetsByID)
			var previous *semantics.Analysis
			if currentRecord, exists := currentByID[asset.ID]; exists && currentRecord.AssetRevision == asset.Revision {
				analysis := currentRecord.Analysis
				previous = &analysis
			}
			work, workErr := semantics.NewWork(generation, asset, candidates, previous, s.now())
			if workErr != nil {
				return fail(workErr)
			}
			records[index].SemanticWork = &work
			records[index].SemanticResult = nil
			records[index].Status = "pending"
			records[index].Provider = s.embedder.Name()
			records[index].Error = ""
		}
		if err := next.Put(records[index]); err != nil {
			return fail(err)
		}
	}
	counts := map[string]int{"ready": 0, "uncertain": 0, "pending": 0, "unchanged": 0}
	for _, record := range records {
		counts[record.Status]++
	}
	return next, derived.NewIndex(records), counts, nil
}

func generationCandidates(value domain.Information, vector []float32, lexical *retrieval.Lexical, semanticIndex *derived.Index, assets map[string]domain.Information) []semantics.Candidate {
	lexicalHits := lexical.Search(value.Content, nil, 24)
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
		result = append(result, semantics.Candidate{ID: candidate.ID, Revision: candidate.Revision, Content: candidate.Content, Contexts: append([]domain.Context(nil), candidate.Contexts...), Similarity: similarity})
	}
	for _, relation := range value.Relations {
		if candidate, exists := assets[relation.TargetID]; exists {
			appendCandidate(candidate, 0)
		}
	}
	for _, hit := range lexicalHits {
		appendCandidate(hit.Information, 0)
		if len(result) >= 6 {
			break
		}
	}
	for _, hit := range semanticIndex.Search(vector, nil, 24) {
		if candidate, exists := assets[hit.AssetID]; exists {
			appendCandidate(candidate, hit.Score)
		}
		if len(result) == 12 {
			break
		}
	}
	for _, hit := range lexicalHits {
		appendCandidate(hit.Information, 0)
		if len(result) == 12 {
			break
		}
	}
	return result
}

func informationSnapshotDigest(values []domain.Information) (string, error) {
	cloned := append([]domain.Information(nil), values...)
	sort.Slice(cloned, func(left, right int) bool { return cloned[left].ID < cloned[right].ID })
	return jsonDigest(cloned)
}

func recordSnapshotDigest(values []derived.Record) (string, error) {
	cloned := append([]derived.Record(nil), values...)
	sort.Slice(cloned, func(left, right int) bool { return cloned[left].AssetID < cloned[right].AssetID })
	return jsonDigest(cloned)
}

func jsonDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneRecordsWithEmbeddings(values []derived.Record) []derived.Record {
	cloned := make([]derived.Record, len(values))
	for index, record := range values {
		cloned[index] = record
		cloned[index].Embedding = append([]float32(nil), record.Embedding...)
	}
	return cloned
}
