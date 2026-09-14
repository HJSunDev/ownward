package core

import (
	"errors"
	"math"
	"sort"
	"strconv"
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
		// Compare retrieval scope as well as term coverage. A whole-source
		// organization must not dominate every focused passage just by collecting
		// unrelated query words across a much larger span. Context remains intact.
		width := 0
		for _, unit := range choices[i].units {
			width += unit.EndRune - unit.StartRune
		}
		normalizer := math.Sqrt(math.Max(1, float64(width)/float64(derived.DefaultEvidenceUnitRunes)))
		choices[i].score = scorer.Score(choices[i].terms) / normalizer
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
	resolved, err := derived.ResolveEvidence(value, unit)
	if err != nil {
		return domain.Evidence{}, err
	}
	return withSourcePrelude(value, resolved), nil
}

// queryEvidenceSummary exposes source-bound reading clues for the references
// already selected by retrieval. It never exceeds the prior summary's width.
func fragmentEvidenceSummary(asset domain.Information, query, summary string, refs []domain.EvidenceReference) string {
	limit := utf8.RuneCountInString(summary)
	if limit > derived.DefaultEvidenceUnitRunes {
		limit = derived.DefaultEvidenceUnitRunes
	}
	if len(refs) == 0 || limit < len(refs)*24 {
		return summary
	}
	width := (limit - 5*len(refs)) / len(refs)
	content := []rune(asset.Content)
	ranges := derived.EvidenceRanges(asset)
	passages := make([]string, 0, len(ranges))
	for _, unit := range ranges {
		passages = append(passages, unit.Content)
	}
	scorer := retrieval.NewPassageTextScorer(query, passages)
	var lines []string
	for n, ref := range refs {
		if ref.SourceID != asset.ID || ref.SourceRevision != asset.Revision || ref.StartRune < 0 || ref.EndRune > len(content) || ref.StartRune >= ref.EndRune {
			return summary
		}
		span := content[ref.StartRune:ref.EndRune]
		count := width
		if len(span) < count {
			count = len(span)
		}
		best, bestScore := 0, -1.0
		for start := 0; ; start += 16 {
			if start+count > len(span) {
				start = len(span) - count
			}
			score := scorer.Score(string(span[start : start+count]))
			if score > bestScore {
				best, bestScore = start, score
			}
			if start+count == len(span) {
				break
			}
		}
		lines = append(lines, "["+strconv.Itoa(n+1)+"] "+string(span[best:best+count]))
	}
	return strings.Join(lines, "\n")
}

// Deliver the same authoritative source's first existing text unit separately
// from the hit. Selection is positional, with no content interpretation.
func withSourcePrelude(source domain.Information, evidence domain.Evidence) domain.Evidence {
	if evidence.SourcePrelude != "" || evidence.StartRune == 0 {
		return evidence
	}
	units := derived.EvidenceRanges(source)
	if len(units) == 0 {
		return evidence
	}
	end := units[0].EndRune
	if end > evidence.StartRune {
		end = evidence.StartRune
	}
	evidence.SourcePrelude = string([]rune(source.Content)[:end])
	evidence.SourcePreludeStartRune, evidence.SourcePreludeEndRune = 0, end
	return evidence
}

// Prefer complete original sentence spans over several clipped fragments.
// This is lexical placement, not a statement of what the source means.
func queryEvidenceSummary(asset domain.Information, query, summary string, refs []domain.EvidenceReference) string {
	limit := utf8.RuneCountInString(summary)
	if limit > derived.DefaultEvidenceUnitRunes {
		limit = derived.DefaultEvidenceUnitRunes
	}
	content := []rune(asset.Content)
	var passages []string
	for _, unit := range derived.EvidenceRanges(asset) {
		passages = append(passages, unit.Content)
	}
	scorer := retrieval.NewPassageTextScorer(query, passages)
	type clue struct {
		ref, start, end int
		text            string
		score           float64
	}
	var choices []clue
	for n, ref := range refs {
		if ref.SourceID != asset.ID || ref.SourceRevision != asset.Revision || ref.StartRune < 0 || ref.EndRune > len(content) || ref.StartRune >= ref.EndRune {
			return summary
		}
		start := ref.StartRune
		completeStart := start == 0
		if start > 0 {
			previous := content[start-1]
			completeStart = previous == '\n' || previous == '\r' || previous == '.' || previous == '!' || previous == '?' || previous == ';' || previous == '。' || previous == '！' || previous == '？' || previous == '；'
		}
		for at := start; at < ref.EndRune; at++ {
			c := content[at]
			boundary := c == '\n' || c == '\r' || c == '。' || c == '！' || c == '？' || c == '；' || c == '!' || c == '?' || c == ';'
			if c == '.' && (at+1 == ref.EndRune || content[at+1] == ' ' || content[at+1] == '\n' || content[at+1] == '"') {
				boundary = true
			}
			if !boundary {
				continue
			}
			text := strings.TrimSpace(string(content[start : at+1]))
			count := utf8.RuneCountInString(text)
			if completeStart && count > 0 && count+5 <= limit {
				score := scorer.Score(text)
				if score > 0 {
					choices = append(choices, clue{n, start, at + 1, text, score})
				}
			}
			start = at + 1
			completeStart = true
		}
	}
	sort.SliceStable(choices, func(i, j int) bool { return choices[i].score > choices[j].score })
	used := map[int]bool{}
	var chosen []clue
	remaining := limit
	for _, item := range choices {
		if used[item.ref] {
			continue
		}
		overlap := false
		for _, prior := range chosen {
			if item.start < prior.end && prior.start < item.end {
				overlap = true
				break
			}
		}
		if overlap {
			continue
		}
		line := "[" + strconv.Itoa(item.ref+1) + "] " + item.text
		cost := utf8.RuneCountInString(line)
		if len(chosen) > 0 {
			cost++
		}
		if cost > remaining {
			continue
		}
		chosen = append(chosen, item)
		used[item.ref] = true
		remaining -= cost
	}
	if len(chosen) == 0 {
		return fragmentEvidenceSummary(asset, query, summary, refs)
	}
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].ref < chosen[j].ref })
	var lines []string
	for _, item := range chosen {
		lines = append(lines, "["+strconv.Itoa(item.ref+1)+"] "+item.text)
	}
	return strings.Join(lines, "\n")
}
