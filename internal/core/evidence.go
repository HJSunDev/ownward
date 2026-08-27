package core

import (
	"errors"
	"sort"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/retrieval"
)

// rankEvidence performs late, query-specific passage selection only for an
// asset that already survived the authoritative asset-level retrieval stage.
// It creates no durable child assets, vectors, or corpus-wide index entries.
func rankEvidence(value domain.Information, query string, limit int) []domain.EvidenceReference {
	units := derived.EvidenceRanges(value)
	if len(units) == 0 || limit <= 0 {
		return nil
	}
	type scoredUnit struct {
		unit  derived.EvidenceUnit
		score float64
	}
	scorer := retrieval.NewQueryTextScorer(query)
	scored := make([]scoredUnit, 0, len(units))
	for _, unit := range units {
		if score := scorer.Score(unit.Content); score > 0 {
			scored = append(scored, scoredUnit{unit: unit, score: score})
		}
	}
	sort.Slice(scored, func(left, right int) bool {
		if scored[left].score == scored[right].score {
			return scored[left].unit.StartRune < scored[right].unit.StartRune
		}
		return scored[left].score > scored[right].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	result := make([]domain.EvidenceReference, 0, len(scored))
	for _, selected := range scored {
		unit, err := derived.MaterializeEvidenceUnit(value, selected.unit)
		if err != nil {
			continue
		}
		result = append(result, unit.Reference())
	}
	return result
}

func readEvidence(store interface {
	ReadCurrent(string) (domain.Information, bool)
}, id string) (domain.Evidence, error) {
	unit, err := derived.ParseEvidenceUnitID(id)
	if err != nil {
		return domain.Evidence{}, errors.New("证据引用不存在或已经过期")
	}
	value, exists := store.ReadCurrent(unit.SourceID)
	if !exists || value.Revision != unit.SourceRevision {
		return domain.Evidence{}, errors.New("证据来源不存在或已经过期")
	}
	return derived.ResolveEvidence(value, unit)
}
