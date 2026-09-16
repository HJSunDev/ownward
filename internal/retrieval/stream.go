package retrieval

import (
	"bufio"
	"crypto/sha256"
	"io"
	"unicode"
	"unicode/utf8"
)

// VisitTermDigests keeps even a single unbroken word bounded. Corpus postings
// use the digest; tokenization, case folding and CJK pairs match tokenize.
func VisitTermDigests(r io.Reader, visit func([32]byte) error) error {
	b := bufio.NewReaderSize(r, 65536)
	word := sha256.New()
	var runeBytes [4]byte
	hasWord := false
	var previous rune
	flush := func() error {
		if !hasWord {
			return nil
		}
		var digest [32]byte
		copy(digest[:], word.Sum(nil))
		word.Reset()
		hasWord = false
		return visit(digest)
	}
	for {
		v, _, err := b.ReadRune()
		if err == io.EOF {
			return flush()
		}
		if err != nil {
			return err
		}
		v = unicode.ToLower(v)
		switch {
		case isCJK(v):
			if err := flush(); err != nil {
				return err
			}
			if err := visit(sha256.Sum256([]byte(string(v)))); err != nil {
				return err
			}
			if previous != 0 {
				if err := visit(sha256.Sum256([]byte(string([]rune{previous, v})))); err != nil {
					return err
				}
			}
			previous = v
		case unicode.IsLetter(v) || unicode.IsDigit(v):
			previous = 0
			hasWord = true
			_, _ = word.Write(utf8.AppendRune(runeBytes[:0], v))
		default:
			previous = 0
			if err := flush(); err != nil {
				return err
			}
		}
	}
}

// QueryTerms preserves the existing accumulation order for bit-identical scores.
func QueryTerms(query string) []string { return tokenize(query) }
func TermWeight(term string) float64   { return queryTermWeight(term) }

// TermPosition preserves tokenize's order without retaining an entire CJK run:
// each run emits all characters before its pairs. The receiving disk index
// sorts by (Group, Plane, Position) before accumulating floating-point scores.
type TermPosition struct {
	Digest                 [32]byte
	Weight                 float64
	Group, Plane, Position int64
}

func VisitQueryTerms(r io.Reader, visit func(TermPosition) error) error {
	b := bufio.NewReaderSize(r, 65536)
	word := sha256.New()
	var runeBytes [4]byte
	var wordRunes, group, position int64
	var previous rune
	flush := func() error {
		if wordRunes == 0 {
			return nil
		}
		var d [32]byte
		copy(d[:], word.Sum(nil))
		weight := 1.5
		if wordRunes == 1 {
			weight = 1
		}
		err := visit(TermPosition{Digest: d, Weight: weight, Group: group})
		word.Reset()
		wordRunes = 0
		group++
		return err
	}
	for {
		v, _, err := b.ReadRune()
		if err == io.EOF {
			return flush()
		}
		if err != nil {
			return err
		}
		v = unicode.ToLower(v)
		switch {
		case isCJK(v):
			if err = flush(); err != nil {
				return err
			}
			if err = visit(TermPosition{Digest: sha256.Sum256([]byte(string(v))), Weight: .25, Group: group, Position: position}); err != nil {
				return err
			}
			if previous != 0 {
				if err = visit(TermPosition{Digest: sha256.Sum256([]byte(string([]rune{previous, v}))), Weight: 1, Group: group, Plane: 1, Position: position - 1}); err != nil {
					return err
				}
			}
			previous = v
			position++
		case unicode.IsLetter(v) || unicode.IsDigit(v):
			if previous != 0 {
				group++
				previous = 0
				position = 0
			}
			wordRunes++
			_, _ = word.Write(utf8.AppendRune(runeBytes[:0], v))
		default:
			if previous != 0 {
				group++
				previous = 0
				position = 0
			}
			if err = flush(); err != nil {
				return err
			}
		}
	}
}
