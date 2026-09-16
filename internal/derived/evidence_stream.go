package derived

import (
	"bufio"
	"io"
	"strings"
	"unicode/utf8"
)

func EvidenceStreamMatches(unit EvidenceUnit, digest string) bool {
	return unit.ID == encodeEvidenceIdentity(evidenceIdentity{SourceID: unit.SourceID, SourceRevision: unit.SourceRevision, StartRune: unit.StartRune, EndRune: unit.EndRune, StartByte: unit.StartByte, EndByte: unit.EndByte, ContentSHA256: digest}, strings.HasPrefix(unit.ID, evidenceIDPrefix))
}

// WalkEvidenceRanges is the streaming form of EvidenceRanges. It retains a
// single 384-rune window and emits precisely the same natural boundaries.
func WalkEvidenceRanges(id string, revision uint64, r io.Reader, visit func(EvidenceUnit) error) error {
	b := bufio.NewReaderSize(r, 65536)
	window := make([]rune, 0, DefaultEvidenceUnitRunes+1)
	exhausted := false
	startRune, startByte := 0, 0
	for {
		for !exhausted && len(window) < DefaultEvidenceUnitRunes+1 {
			v, _, e := b.ReadRune()
			if e == io.EOF {
				exhausted = true
				break
			}
			if e != nil {
				return e
			}
			window = append(window, v)
		}
		if len(window) == 0 {
			return nil
		}
		if startRune == 0 && exhausted && len(window) <= DefaultEvidenceUnitRunes {
			return nil
		}
		n := min(DefaultEvidenceUnitRunes, len(window))
		if n == DefaultEvidenceUnitRunes {
			natural := 0
			for i, v := range window[:n] {
				if i+1 > minimumEvidenceUnitRunes && naturalBoundary(v) {
					natural = i + 1
				}
			}
			if natural > 0 {
				n = natural
			}
		}
		text := string(window[:n])
		endByte := startByte + len(text)
		if e := visit(EvidenceUnit{Schema: EvidenceUnitSchema, SourceID: id, SourceRevision: revision, StartRune: startRune, EndRune: startRune + n, StartByte: startByte, EndByte: endByte, Content: text}); e != nil {
			return e
		}
		startRune += n
		startByte = endByte
		copy(window, window[n:])
		window = window[:len(window)-n]
	}
}

// CompleteStreamPassage reads at most one base passage on either side. ReaderAt
// access is backed by the source file/database, never an in-memory whole body.
func CompleteStreamPassage(source io.ReaderAt, bytes int64, unit EvidenceUnit) (EvidenceUnit, error) {
	before := max(0, int64(unit.StartByte)-4*(DefaultEvidenceUnitRunes+2))
	after := min(bytes, int64(unit.EndByte)+4*(DefaultEvidenceUnitRunes+2))
	left := make([]byte, int64(unit.StartByte)-before)
	if _, e := source.ReadAt(left, before); e != nil && e != io.EOF {
		return unit, e
	}
	for n := 0; n < DefaultEvidenceUnitRunes && unit.StartByte > 0; n++ {
		if endsParagraph(left) {
			break
		}
		_, size := utf8.DecodeLastRune(left)
		if size == 0 {
			break
		}
		left = left[:len(left)-size]
		unit.StartByte -= size
		unit.StartRune--
	}
	right := make([]byte, after-int64(unit.EndByte))
	if _, e := source.ReadAt(right, int64(unit.EndByte)); e != nil && e != io.EOF {
		return unit, e
	}
	prefix := make([]byte, min(4, unit.EndByte))
	if _, e := source.ReadAt(prefix, int64(unit.EndByte-len(prefix))); e != nil && e != io.EOF {
		return unit, e
	}
	for n := 0; n < DefaultEvidenceUnitRunes && int64(unit.EndByte) < bytes; n++ {
		if endsParagraph(prefix) {
			break
		}
		_, size := utf8.DecodeRune(right)
		if size == 0 {
			break
		}
		prefix = append(prefix, right[:size]...)
		if len(prefix) > 4 {
			prefix = prefix[len(prefix)-4:]
		}
		right = right[size:]
		unit.EndByte += size
		unit.EndRune++
	}
	text := make([]byte, unit.EndByte-unit.StartByte)
	if _, e := source.ReadAt(text, int64(unit.StartByte)); e != nil {
		return unit, e
	}
	unit.Content = string(text)
	return unit, nil
}
func endsParagraph(v []byte) bool {
	n := len(v)
	return n >= 2 && v[n-2] == '\n' && v[n-1] == '\n' || n >= 4 && v[n-4] == '\r' && v[n-3] == '\n' && v[n-2] == '\r' && v[n-1] == '\n'
}
