package kernelv2candidate

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/retrieval"
)

// SourcePreview selects a bounded, contiguous excerpt of the authoritative
// content. It never presents a model's inference as a quotation from this source.
// Query terms choose the window, not the facts or the decision to read further.
func SourcePreview(content, query string, limit int, cues ...string) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	if limit > 2 {
		if grounded := queryCues(content, query, cues); len(grounded) > 0 {
			cue := grounded[0]
			start := strings.Index(content, cue)
			// Return source bytes around the chosen cue, never a semantic rewrite.
			prefix := []rune(content[:start])
			suffix := []rune(content[start+len(cue):])
			window := string(prefix[max(0, len(prefix)-48):]) + cue + string(suffix[:min(32, len(suffix))])
			return "…" + SourcePreview(window, query, limit-2) + "…"
		}
	}
	if limit < 3 {
		return string(runes[:limit])
	}
	width := limit - 2 // reserve truncation markers, without exceeding the bound
	terms := make(map[string]float64)
	previewTokens([]rune(query), func(term string, _, _ int) {
		terms[term] = math.Log1p(float64(len([]rune(term))))
	})
	type hit struct {
		start, end int
		term       string
	}
	hits := make([]hit, 0)
	previewTokens(runes, func(term string, start, end int) {
		if _, ok := terms[term]; ok && end-start <= width {
			hits = append(hits, hit{start, end, term})
		}
	})
	bestStart, left, right := 0, 0, 0
	bestScore, score := -1.0, 0.0
	counts := make(map[string]int)
	for _, anchor := range hits {
		start := min(max(0, anchor.start-width/4), len(runes)-width)
		end := start + width
		for right < len(hits) && hits[right].end <= end {
			term := hits[right].term
			if counts[term] == 0 {
				score += terms[term]
			}
			counts[term]++
			right++
		}
		for left < right && hits[left].start < start {
			term := hits[left].term
			counts[term]--
			if counts[term] == 0 {
				score -= terms[term]
			}
			left++
		}
		if score > bestScore+1e-9 {
			bestStart, bestScore = start, score
		}
	}
	end := bestStart + width
	// Avoid partial words where possible; all displayed text remains contiguous.
	if bestStart > 0 && unicode.IsLetter(runes[bestStart-1]) {
		for bestStart < end && unicode.IsLetter(runes[bestStart]) && !unicode.Is(unicode.Han, runes[bestStart]) {
			bestStart++
		}
	}
	if end < len(runes) && unicode.IsLetter(runes[end]) {
		for end > bestStart && unicode.IsLetter(runes[end-1]) && !unicode.Is(unicode.Han, runes[end-1]) {
			end--
		}
	}
	if end <= bestStart {
		bestStart, end = 0, width
	}
	text := string(runes[bestStart:end])
	if bestStart > 0 {
		text = "…" + text
	}
	if end < len(runes) {
		text += "…"
	}
	return text
}

// Semantic analysis chooses useful cues; only exact, query-relevant quotes
// from this asset can guide its public source excerpts. No role or topic gets
// a hard-coded preference, and an ungrounded/cross-source cue is ignored.
func queryCues(content, query string, cues []string) []string {
	scorer := retrieval.NewQueryTextScorer(query)
	seen := make(map[string]bool)
	result := make([]string, 0, len(cues))
	for _, cue := range cues {
		cue = strings.TrimSpace(cue)
		// Stored metadata can be a shortened quote with a display ellipsis.
		// Anchor its literal prefix only when it identifies one source location;
		// evidence is still read from that original location, not from metadata.
		if !strings.Contains(content, cue) && strings.HasSuffix(cue, "…") {
			prefix := strings.TrimSpace(strings.TrimSuffix(cue, "…"))
			if prefix != "" && strings.Count(content, prefix) == 1 {
				cue = prefix
			}
		}
		if cue == "" || seen[cue] || utf8.RuneCountInString(cue) > 384 || !strings.Contains(content, cue) || scorer.Score(cue) <= 0 {
			continue
		}
		seen[cue] = true
		result = append(result, cue)
	}
	sort.SliceStable(result, func(i, j int) bool { return scorer.Score(result[i]) > scorer.Score(result[j]) })
	return result
}

func previewTokens(runes []rune, emit func(string, int, int)) {
	start := -1
	flush := func(end int) {
		if start >= 0 {
			emit(strings.ToLower(string(runes[start:end])), start, end)
			start = -1
		}
	}
	for i, r := range runes {
		if unicode.Is(unicode.Han, r) {
			flush(i)
			emit(string(r), i, i+1)
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
		} else {
			flush(i)
		}
	}
	flush(len(runes))
}
