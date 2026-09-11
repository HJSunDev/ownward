package core

import (
	"context"
	"errors"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/retrieval"
	"github.com/HJSunDev/ownward/internal/semantics"
	"sort"
	"strings"
)

// Invalid generations are already hidden by the read-time input checks. The
// existing semantic-work pickup also repairs their work references, including
// after a restart, so no background agent or user maintenance action is needed.
func (s *Service) refreshInvalidOrganizationWork(ctx context.Context, asset domain.Information) error {
	unlock := s.lockMutation(asset.ID)
	defer unlock()
	s.graphMu.Lock()
	defer s.graphMu.Unlock()
	record, ok := s.derivedStore.GetWithEmbedding(asset.ID)
	if !ok {
		return nil
	}
	invalid := !s.organizationCurrent(record) || s.hasStaleRelation(record)
	if record.HasPendingSemanticWork() {
		_, err := s.resolveSemanticWork(record)
		invalid = invalid || err != nil
	}
	if !invalid {
		return nil
	}
	current, ok := s.authority.ReadCurrent(asset.ID)
	if !ok {
		return nil
	}
	next, err := s.newPendingSemanticRecord(current, record.Embedding, nil, s.semantic)
	if err != nil {
		return err
	}
	return contract.Commit(ctx, func() error {
		if err := s.derivedStore.Put(next); err != nil {
			return err
		}
		s.semantic.Upsert(next)
		return nil
	})
}

func sourceReference(asset domain.Information, selector domain.TextSelector) (domain.EvidenceReference, error) {
	if selector.Exact == "" {
		selector.Exact = asset.Content
	}
	start, end, err := selector.Resolve(asset.Content)
	if err != nil {
		return domain.EvidenceReference{}, err
	}
	runes := []rune(asset.Content)
	unit, err := derived.MaterializeEvidenceUnit(asset, derived.EvidenceUnit{Schema: derived.EvidenceUnitSchema, SourceID: asset.ID, SourceRevision: asset.Revision, StartRune: start, EndRune: end, StartByte: len(string(runes[:start])), EndByte: len(string(runes[:end]))})
	return unit.Reference(), err
}

// A match elsewhere in a multi-topic source does not make every connection
// equally relevant. Transfer the strong seed score only in proportion to the
// query supported by the connection itself; keep the ordinary graph candidate
// floor when literal matching cannot establish relevance (including paraphrases).
func (s *Service) groundedRelationWeight(edge derived.Edge, seed string, scorer retrieval.QueryTextScorer) float64 {
	// A generic association is an exploration lead, not evidence that the
	// neighboring source answers the query. Keep it at the existing graph
	// candidate weight; typed evidence can justify stronger propagation.
	if edge.Grounded.Type == "related_to" {
		return 0.3
	}
	asset, ok := s.authority.ReadCurrent(seed)
	if !ok {
		return 0
	}
	endpoint := edge.Grounded.Source
	if endpoint.AssetID != seed {
		endpoint = edge.Grounded.Target
	}
	passage := endpoint.Selector.Exact
	if passage == "" {
		passage = asset.Content
	}
	// Mentions identify an object, while its unit states what happened. Judge
	// query relevance against the complete evidence unit, not just its name.
	whole := 0.0
	for _, paragraph := range strings.Split(asset.Content, "\n\n") {
		whole = max(whole, scorer.Score(paragraph))
	}
	if record, exists := s.semantic.Get(seed); exists && record.Analysis.Organization != nil {
		for _, unit := range record.Analysis.Organization.Units {
			text := unit.Selector.Exact
			if text == "" {
				text = asset.Content
			}
			for _, context := range unit.Context {
				text += " " + context.Exact
			}
			whole = max(whole, scorer.Score(text))
			if unit.ID == endpoint.UnitID {
				passage = text
			}
		}
	}
	if whole == 0 {
		return 0.3
	}
	support := scorer.Score(passage + " " + edge.Grounded.Meaning)
	return max(0.3, 0.96*min(1, support/whole))
}

func (s *Service) relationEvidence(edge derived.Edge) (*contract.RelationEvidence, bool) {
	if edge.Grounded == nil {
		return nil, true
	}
	owner, ok := s.semantic.Get(edge.OwnerID)
	if !ok || !s.organizationCurrent(owner) {
		return nil, false
	}
	link := edge.Grounded
	result := &contract.RelationEvidence{ID: link.ID, Origin: "derived_interpretation", Type: link.Type, Meaning: link.Meaning}
	endpoints := append([]semantics.GraphEndpoint{link.Source, link.Target}, link.Conditions...)
	for n, endpoint := range endpoints {
		asset, exists := s.authority.ReadCurrent(endpoint.AssetID)
		if !exists || asset.Revision != endpoint.Revision {
			return nil, false
		}
		ref, err := sourceReference(asset, endpoint.Selector)
		if err != nil {
			return nil, false
		}
		if n == 0 {
			result.Source = ref
		} else if n == 1 {
			result.Target = ref
		} else {
			result.Conditions = append(result.Conditions, ref)
		}
		if endpoint.UnitID != "" {
			record, exists := s.semantic.Get(endpoint.AssetID)
			if !exists || !s.organizationCurrent(record) || record.Analysis.Organization == nil || record.Analysis.Organization.Snapshot != endpoint.Snapshot {
				return nil, false
			}
			found := false
			for _, unit := range record.Analysis.Organization.Units {
				if unit.ID != endpoint.UnitID {
					continue
				}
				found = true
				selectors := append([]domain.TextSelector{unit.Selector}, unit.Context...)
				for _, selector := range selectors {
					if selector == endpoint.Selector {
						continue
					}
					contextRef, err := sourceReference(asset, selector)
					if err != nil {
						return nil, false
					}
					result.Context = append(result.Context, contextRef)
				}
			}
			if !found {
				return nil, false
			}
		}
	}
	return result, true
}

func (s *Service) organizedEvidence(asset domain.Information, query string, limit int) []domain.EvidenceReference {
	if s.semantic == nil {
		return nil
	}
	record, ok := s.semantic.Get(asset.ID)
	if !ok || record.AssetRevision != asset.Revision || record.Analysis.Organization == nil || !s.organizationCurrent(record) {
		return nil
	}
	type hit struct {
		unit  semantics.SemanticUnit
		score float64
	}
	var hits []hit
	scorer := retrieval.NewQueryTextScorer(query)
	for _, unit := range record.Analysis.Organization.Units {
		terms := []string{unit.Statement, unit.Selector.Exact}
		for _, mention := range unit.Mentions {
			terms = append(terms, mention.Name, mention.Role)
		}
		if score := scorer.Score(strings.Join(terms, " ")); score > 0 {
			hits = append(hits, hit{unit, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	var result []domain.EvidenceReference
	for _, selected := range hits {
		selectors := append([]domain.TextSelector{selected.unit.Selector}, selected.unit.Context...)
		// Keep the complete context bundle or leave it to ordinary raw reading.
		if len(result)+len(selectors) > limit {
			continue
		}
		for _, selector := range selectors {
			ref, err := sourceReference(asset, selector)
			if err != nil {
				return nil
			}
			result = append(result, ref)
		}
		if len(result) == limit {
			break
		}
	}
	return result
}

// Validate the work handed to the caller, not a freshly enlarged candidate set.
// Recovery and publication must hash exactly the same normalized submission.
func (s *Service) normalizeSemanticSubmission(record derived.Record, asset domain.Information, input semantics.Submission) (semantics.Submission, error) {
	work, err := s.resolveSemanticWork(record)
	if err != nil {
		return semantics.Submission{}, err
	}
	if len(input.InputAssets) > 660 {
		return semantics.Submission{}, errors.New("语义调用的输入引用超过有界批量范围")
	}
	var supplemental []semantics.Candidate
	supplementalIndex := map[string]int{}
	for _, ref := range input.InputAssets {
		current, exists := s.authority.ReadCurrent(ref.ID)
		if !exists || ref.Revision == 0 || current.Revision != ref.Revision {
			return semantics.Submission{}, errors.New("语义调用的实际输入已变化")
		}
		if ref.OrganizationSnapshot != "" {
			derived, exists := s.semantic.Get(ref.ID)
			if !exists || derived.Analysis.Organization == nil || derived.Analysis.Organization.Snapshot != ref.OrganizationSnapshot || !s.organizationCurrent(derived) {
				return semantics.Submission{}, errors.New("语义调用的实际组织输入已变化")
			}
		}
		if ref.ID != asset.ID {
			candidate := semantics.Candidate{ID: ref.ID, Revision: ref.Revision, Content: current.Content}
			if ref.OrganizationSnapshot != "" {
				record, _ := s.semantic.Get(ref.ID)
				candidate.Organization = semantics.OrganizationInventory(record.Analysis.Organization)
			}
			provided := false
			for _, original := range work.Candidates {
				if original.ID == ref.ID && (original.Organization != nil || candidate.Organization == nil) {
					provided = true
					break
				}
			}
			if !provided {
				if n, ok := supplementalIndex[candidate.ID]; ok {
					if candidate.Organization != nil {
						supplemental[n] = candidate
					}
				} else {
					supplementalIndex[candidate.ID] = len(supplemental)
					supplemental = append(supplemental, candidate)
				}
			}
		}
	}
	if record.SemanticReceipt == nil {
		snapshot := ""
		if record.Analysis.Organization != nil {
			snapshot = record.Analysis.Organization.Snapshot
		}
		if snapshot != work.TargetSnapshot {
			return semantics.Submission{}, errors.New("目标组织已被替换，请接续当前语义工作")
		}
	}
	for _, dependency := range record.InputAssets {
		value, ok := s.authority.ReadCurrent(dependency.ID)
		if !ok || value.Revision != dependency.Revision {
			return semantics.Submission{}, errors.New("语义工作的输入已变化，请接续当前语义工作")
		}
		if dependency.OrganizationSnapshot != "" {
			current, ok := s.semantic.Get(dependency.ID)
			if !ok || current.Analysis.Organization == nil || current.Analysis.Organization.Snapshot != dependency.OrganizationSnapshot {
				return semantics.Submission{}, errors.New("语义工作使用的组织快照已变化")
			}
		}
	}
	return semantics.NormalizeSubmission(work, input, s.now(), supplemental...)
}

func (s *Service) organizationCurrent(record derived.Record) bool {
	for _, dependency := range derived.Inputs(record) {
		asset, ok := s.authority.ReadCurrent(dependency.ID)
		if !ok || (dependency.Revision != 0 && dependency.Revision != asset.Revision) {
			return false
		}
		if dependency.OrganizationSnapshot != "" {
			other, ok := s.semantic.Get(dependency.ID)
			if !ok || other.Analysis.Organization == nil || other.Analysis.Organization.Snapshot != dependency.OrganizationSnapshot {
				return false
			}
		}
	}
	return true
}
