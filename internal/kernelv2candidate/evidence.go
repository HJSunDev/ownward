// Package kernelv2candidate contains only the independently selectable V2
// kernel behavior under evaluation. It is not imported by the current product
// build; the non-formal candidate builder binds it through an explicit Go
// build overlay and seals that fact into the candidate composition.
package kernelv2candidate

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/retrieval"
)

const (
	EvidenceSelectionPolicy = "bounded-source-continuity-diversity/v1"
	PredecessorRunes        = 256
	SuccessorRunes          = 128
)

type scoredEvidenceUnit struct {
	unit  derived.EvidenceUnit
	score float64
}

// RankEvidence preserves the current query ranking, read limit, authoritative
// source binding and ephemeral identity. The only change is that every base
// range carries bounded context from both neighbours. The asymmetric window
// preserves the beginning of the same source turn while also keeping a field
// and its immediately following value together. It creates neither persistent
// ranges nor vectors and keeps the number of scored ranges unchanged.
func RankEvidence(value domain.Information, query string, limit int) []domain.EvidenceReference {
	selected := rankedEvidence(value, query, limit)
	return materializeReferences(value, query, selected)
}

func RankEvidenceWithCues(value domain.Information, query string, limit int, cues []string) []domain.EvidenceReference {
	return materializeReferences(value, query, rankedEvidence(value, query, limit, cues...))
}

// SearchEvidence offers direct reading entry points from both semantic cues
// and the original text, so incomplete metadata cannot hide the raw-text path.
func SearchEvidence(value domain.Information, query string, cues []string) []domain.EvidenceReference {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	refs := RankEvidenceWithCues(value, query, 2, cues)
	for _, raw := range RankEvidence(value, query, 1) {
		duplicate := false
		for _, ref := range refs {
			if ref.ID == raw.ID {
				duplicate = true
			}
		}
		if !duplicate {
			refs = append(refs, raw)
		}
	}
	return refs
}

func ProbeEvidenceWithCues(value domain.Information, query string, cues []string) ([]domain.EvidenceReference, bool) {
	selected := rankedEvidence(value, query, 2, cues...)
	if len(selected) == 0 {
		return nil, false
	}
	return materializeReferences(value, query, selected[:1]), len(selected) > 1
}

// ProbeEvidence proves whether a source has repeated useful depth while
// materializing only the first reference needed by breadth-first scheduling.
// A lone deep source is reranked with the caller's full bound only if the
// selection path actually consumes that depth.
func ProbeEvidence(value domain.Information, query string) ([]domain.EvidenceReference, bool) {
	selected := rankedEvidence(value, query, 2)
	if len(selected) == 0 {
		return nil, false
	}
	return materializeReferences(value, query, selected[:1]), len(selected) > 1
}

func rankedEvidence(value domain.Information, query string, limit int, cues ...string) []scoredEvidenceUnit {
	units := continuityRanges(value)
	if len(units) == 0 || limit <= 0 {
		return nil
	}
	scorer := retrieval.NewQueryTextScorer(query)
	scored := make([]scoredEvidenceUnit, 0, len(units))
	for _, unit := range units {
		if score := scorer.Score(unit.Content); score > 0 {
			scored = append(scored, scoredEvidenceUnit{unit: unit, score: score})
		}
	}
	for _, cue := range queryCues(value.Content, query, cues) {
		startByte := strings.Index(value.Content, cue)
		startRune := utf8.RuneCountInString(value.Content[:startByte])
		endByte, endRune := startByte+len(cue), startRune+utf8.RuneCountInString(cue)
		for n := 0; n < 96 && startByte > 0; n++ {
			_, size := utf8.DecodeLastRuneInString(value.Content[:startByte])
			startByte -= size
			startRune--
		}
		for n := 0; n < 64 && endByte < len(value.Content); n++ {
			_, size := utf8.DecodeRuneInString(value.Content[endByte:])
			endByte += size
			endRune++
		}
		unit := derived.EvidenceUnit{Schema: derived.EvidenceUnitSchema, SourceID: value.ID, SourceRevision: value.Revision,
			StartRune: startRune, EndRune: endRune, StartByte: startByte, EndByte: endByte, Content: value.Content[startByte:endByte]}
		// A source-grounded semantic cue supplies a second relevance signal;
		// lexical-only candidates remain available for facts absent from analysis.
		scored = append(scored, scoredEvidenceUnit{unit: unit, score: scorer.Score(query) + scorer.Score(cue)})
	}
	sort.Slice(scored, func(left, right int) bool {
		if scored[left].score == scored[right].score {
			return scored[left].unit.StartRune < scored[right].unit.StartRune
		}
		return scored[left].score > scored[right].score
	})
	return selectMarginalCoverage(scored, limit)
}

func materializeReferences(value domain.Information, query string, selected []scoredEvidenceUnit) []domain.EvidenceReference {
	result := make([]domain.EvidenceReference, 0, len(selected))
	for _, selected := range selected {
		unit, err := derived.MaterializeEvidenceUnit(value, selected.unit)
		if err == nil {
			reference := unit.Reference()
			reference.Preview = SourcePreview(unit.Content, query, 240)
			result = append(result, reference)
		}
	}
	return result
}

// selectMarginalCoverage keeps the strongest passage first, then discounts
// only source runes already present in selected windows. That prevents several
// nearly identical overlapping windows from consuming the fixed evidence
// limit while a relevant later part of the same authoritative source is still
// absent. Every selected range must still have independently matched the query.
func selectMarginalCoverage(scored []scoredEvidenceUnit, limit int) []scoredEvidenceUnit {
	if len(scored) <= limit {
		return scored
	}
	remaining := append([]scoredEvidenceUnit(nil), scored...)
	selected := make([]scoredEvidenceUnit, 0, limit)
	for len(selected) < limit && len(remaining) > 0 {
		best, bestPriority := 0, -1.0
		for index, candidate := range remaining {
			length := candidate.unit.EndRune - candidate.unit.StartRune
			priority := candidate.score * float64(uncoveredRunes(candidate.unit, selected)) / float64(length)
			if priority > bestPriority || (priority == bestPriority && (candidate.score > remaining[best].score ||
				(candidate.score == remaining[best].score && candidate.unit.StartRune < remaining[best].unit.StartRune))) {
				best, bestPriority = index, priority
			}
		}
		selected = append(selected, remaining[best])
		remaining = append(remaining[:best], remaining[best+1:]...)
	}
	return selected
}

func uncoveredRunes(candidate derived.EvidenceUnit, selected []scoredEvidenceUnit) int {
	type interval struct{ start, end int }
	overlaps := make([]interval, 0, len(selected))
	for _, existing := range selected {
		start := max(candidate.StartRune, existing.unit.StartRune)
		end := min(candidate.EndRune, existing.unit.EndRune)
		if start < end {
			overlaps = append(overlaps, interval{start: start, end: end})
		}
	}
	sort.Slice(overlaps, func(left, right int) bool { return overlaps[left].start < overlaps[right].start })
	covered, currentEnd := 0, candidate.StartRune
	for _, overlap := range overlaps {
		start := max(overlap.start, currentEnd)
		if start < overlap.end {
			covered += overlap.end - start
			currentEnd = overlap.end
		}
	}
	return candidate.EndRune - candidate.StartRune - covered
}

func continuityRanges(value domain.Information) []derived.EvidenceUnit {
	units := derived.EvidenceRanges(value)
	for index := range units {
		startRune := units[index].StartRune
		startByte := units[index].StartByte
		for count := 0; count < PredecessorRunes && startByte > 0; count++ {
			_, size := utf8.DecodeLastRuneInString(value.Content[:startByte])
			if size <= 0 {
				return nil
			}
			startRune--
			startByte -= size
		}
		endRune := units[index].EndRune
		endByte := units[index].EndByte
		for count := 0; count < SuccessorRunes && endByte < len(value.Content); count++ {
			_, size := utf8.DecodeRuneInString(value.Content[endByte:])
			if size <= 0 {
				return nil
			}
			endRune++
			endByte += size
		}
		units[index].StartRune = startRune
		units[index].StartByte = startByte
		units[index].EndRune = endRune
		units[index].EndByte = endByte
		units[index].Content = value.Content[startByte:endByte]
	}
	return units
}
