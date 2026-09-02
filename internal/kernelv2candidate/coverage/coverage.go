// Package coverage defines the compact, rebuildable source metadata exchanged
// by the V2 candidate lexical boundary and its bounded read-frontier planner.
// It deliberately contains no authority text or product state.
package coverage

import (
	"strings"
	"unicode"
)

// Sketch is a fixed bottom-k set of source-term hashes. Unlike an unbounded
// term slice it costs the same for every source, while its sorted hashes retain
// a deterministic approximation of source diversity without saturating on a
// long multilingual document.
type Sketch struct {
	Values [128]uint64
	Count  uint8
}

type Source struct {
	PassageScore float64
	Diversity    Sketch
}

// Add retains the 128 smallest distinct term hashes in sorted order. This is
// exact for ordinary short sources and remains bounded for long sources.
func (s *Sketch) Add(term string) {
	value := hash64(term, 14695981039346656037)
	count := int(s.Count)
	position := 0
	for position < count && s.Values[position] < value {
		position++
	}
	if position < count && s.Values[position] == value {
		return
	}
	if count == len(s.Values) && position == count {
		return
	}
	limit := min(count, len(s.Values)-1)
	for index := limit; index > position; index-- {
		s.Values[index] = s.Values[index-1]
	}
	s.Values[position] = value
	if count < len(s.Values) {
		s.Count++
	}
}

// Distance is the deterministic Jaccard distance between two bottom-k source
// sketches. It is used only as a bounded diversity tie-break.
func Distance(left, right Sketch) float64 {
	leftCount, rightCount := int(left.Count), int(right.Count)
	intersection := 0
	for leftIndex, rightIndex := 0, 0; leftIndex < leftCount && rightIndex < rightCount; {
		switch {
		case left.Values[leftIndex] < right.Values[rightIndex]:
			leftIndex++
		case left.Values[leftIndex] > right.Values[rightIndex]:
			rightIndex++
		default:
			intersection++
			leftIndex++
			rightIndex++
		}
	}
	union := leftCount + rightCount - intersection
	if union == 0 {
		return 0
	}
	return 1 - float64(intersection)/float64(union)
}

// FromText builds a bounded, language-independent source sketch from an
// already bounded public summary. It preserves words plus dense-script
// unigrams/bigrams and never reads authority content.
func FromText(value string) Sketch {
	var result Sketch
	var word, dense []rune
	flushWord := func() {
		if len(word) > 0 {
			result.Add("word:" + string(word))
			word = word[:0]
		}
	}
	flushDense := func() {
		for _, current := range dense {
			result.Add("dense:" + string(current))
		}
		for index := 0; index+1 < len(dense); index++ {
			result.Add("dense-pair:" + string(dense[index:index+2]))
		}
		dense = dense[:0]
	}
	for _, current := range strings.ToLower(value) {
		switch {
		case unicode.In(current, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			flushWord()
			dense = append(dense, current)
		case unicode.IsLetter(current) || unicode.IsNumber(current):
			flushDense()
			word = append(word, current)
		default:
			flushWord()
			flushDense()
		}
	}
	flushWord()
	flushDense()
	return result
}

func hash64(value string, seed uint64) uint64 {
	hash := seed
	for index := 0; index < len(value); index++ {
		hash ^= uint64(value[index])
		hash *= 1099511628211
	}
	return hash
}
