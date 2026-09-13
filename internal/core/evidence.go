package core

import (
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/retrieval"
)

// rankEvidence performs late, query-specific passage selection only for an
// asset that already survived the authoritative asset-level retrieval stage.
// It creates no durable child assets, vectors, or corpus-wide index entries.
func rankEvidence(value domain.Information, query string, limit int) []domain.EvidenceReference {
	return selectEvidence(value, query, limit, nil)
}

type evidenceChoice struct {
	units []derived.EvidenceUnit
	terms string
	score float64
}

func selectEvidence(value domain.Information, query string, limit int, choices []evidenceChoice) []domain.EvidenceReference {
	units := derived.EvidenceRanges(value)
	if limit <= 0 {
		return nil
	}
	passages := make([]string, 0, len(units))
	for _, unit := range units {
		passages = append(passages, unit.Content)
	}
	scorer := retrieval.NewPassageTextScorer(query, passages)
	for _, unit := range units {
		choices = append(choices, evidenceChoice{units: []derived.EvidenceUnit{completePassage(value, unit)}, terms: unit.Content})
	}
	for i := range choices {
		choices[i].score = scorer.Score(choices[i].terms)
	}
	sort.SliceStable(choices, func(left, right int) bool {
		if choices[left].score == choices[right].score {
			return choices[left].units[0].StartRune < choices[right].units[0].StartRune
		}
		return choices[left].score > choices[right].score
	})
	result := make([]domain.EvidenceReference, 0, limit)
	for _, choice := range choices {
		if choice.score <= 0 {
			break
		}
		var pending []domain.EvidenceReference
		for _, candidate := range choice.units {
			covered := false
			for _, ref := range result {
				covered = covered || (ref.StartRune <= candidate.StartRune && ref.EndRune >= candidate.EndRune)
			}
			if covered {
				continue
			}
			unit, err := derived.MaterializeEvidenceUnit(value, candidate)
			if err != nil {
				pending = nil
				break
			}
			pending = append(pending, unit.Reference())
		}
		// A semantic unit and its required context are delivered together.
		if len(result)+len(pending) > limit {
			continue
		}
		result = append(result, pending...)
		if len(result) == limit {
			break
		}
	}
	return result
}

// Complete a selected passage at nearby paragraph boundaries. Expansion is
// source-bound and limited to one base passage on either side.
func completePassage(value domain.Information, unit derived.EvidenceUnit) derived.EvidenceUnit {
	for n := 0; n < derived.DefaultEvidenceUnitRunes && unit.StartByte > 0; n++ {
		if strings.HasSuffix(value.Content[:unit.StartByte], "\n\n") || strings.HasSuffix(value.Content[:unit.StartByte], "\r\n\r\n") {
			break
		}
		_, size := utf8.DecodeLastRuneInString(value.Content[:unit.StartByte])
		unit.StartByte -= size
		unit.StartRune--
	}
	for n := 0; n < derived.DefaultEvidenceUnitRunes && unit.EndByte < len(value.Content); n++ {
		if strings.HasSuffix(value.Content[:unit.EndByte], "\n\n") || strings.HasSuffix(value.Content[:unit.EndByte], "\r\n\r\n") {
			break
		}
		_, size := utf8.DecodeRuneInString(value.Content[unit.EndByte:])
		unit.EndByte += size
		unit.EndRune++
	}
	unit.Content = value.Content[unit.StartByte:unit.EndByte]
	return unit
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
