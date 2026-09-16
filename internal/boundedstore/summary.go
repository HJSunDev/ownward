package boundedstore

import (
	"bufio"
	"context"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
)

// EvidenceSummary applies the existing sentence/fragment placement to disk
// ranges. Candidate sentences are sorted on disk, not retained with the source.
func (s *Store) EvidenceSummary(ctx context.Context, id, query, summary string, refs []domain.EvidenceReference) (string, error) {
	return s.EvidenceSummarySource(ctx, id, StringSource(query), summary, refs)
}
func (s *Store) EvidenceSummarySource(ctx context.Context, id string, query contract.ContentSource, summary string, refs []domain.EvidenceReference) (string, error) {
	result := summary
	limit := min(utf8.RuneCountInString(summary), derived.DefaultEvidenceUnitRunes)
	if len(refs) == 0 {
		return result, nil
	}
	e := s.WithSnapshot(ctx, func(ctx context.Context) error {
		m, e := s.ReadAssetMeta(ctx, id, 0)
		if e != nil {
			return e
		}
		text, e := s.spoolContent(ctx, m)
		if e != nil {
			return e
		}
		defer text.file.Close()
		scorer, e := s.SourceScorer(ctx, query, func(visit func(string) error) error {
			return derived.WalkEvidenceRanges(id, m.Revision, io.NewSectionReader(text.file, 0, text.size), func(u derived.EvidenceUnit) error { return visit(u.Content) })
		})
		if e != nil {
			return e
		}
		return s.view(ctx, func(q queryer) error {
			if _, e := q.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS summary_clues(ordinal INTEGER PRIMARY KEY,ref INTEGER,start INTEGER,end INTEGER,text TEXT,score REAL); DELETE FROM summary_clues"); e != nil {
				return e
			}
			ordinal := 0
			for n, ref := range refs {
				if ref.SourceID != id || ref.SourceRevision != m.Revision || ref.StartRune < 0 || ref.EndRune <= ref.StartRune {
					return nil
				}
				u, e := text.span(m, ref.StartRune, ref.EndRune)
				if e != nil {
					return e
				}
				complete := u.StartByte == 0
				if !complete {
					var b [4]byte
					size := min(4, u.StartByte)
					if _, e = text.file.ReadAt(b[:size], int64(u.StartByte-size)); e != nil {
						return e
					}
					prev, _ := utf8.DecodeLastRune(b[:size])
					complete = strings.ContainsRune("\n\r.!?;。！？；", prev)
				}
				reader := bufio.NewReaderSize(io.NewSectionReader(text.file, int64(u.StartByte), int64(u.EndByte-u.StartByte)), ChunkBytes)
				start := ref.StartRune
				var runes []rune
				var trailing []rune
				overflow := false
				spaceCount := 0
				for at := ref.StartRune; at < ref.EndRune; at++ {
					c, _, e := reader.ReadRune()
					if e != nil {
						return e
					}
					if !overflow {
						if unicode.IsSpace(c) {
							if len(runes) > 0 {
								spaceCount++
								if spaceCount <= limit {
									trailing = append(trailing, c)
								}
							}
						} else {
							if len(runes)+spaceCount+1 > limit {
								overflow = true
							} else {
								runes = append(runes, trailing...)
								runes = append(runes, c)
							}
							trailing = trailing[:0]
							spaceCount = 0
						}
					}
					boundary := strings.ContainsRune("\n\r。！？；!?;", c)
					if c == '.' {
						next, _ := reader.Peek(1)
						boundary = at+1 == ref.EndRune || len(next) > 0 && (next[0] == ' ' || next[0] == '\n' || next[0] == '"')
					}
					if !boundary {
						continue
					}
					if complete && !overflow && len(runes) > 0 && len(runes)+5 <= limit {
						v := string(runes)
						score := scorer.Score(v)
						if e := scorer.Err(); e != nil {
							return e
						}
						if score > 0 {
							if _, e = q.ExecContext(ctx, "INSERT INTO summary_clues VALUES(?,?,?,?,?,?)", ordinal, n, start, at+1, v, score); e != nil {
								return e
							}
							ordinal++
						}
					}
					start = at + 1
					complete = true
					runes = runes[:0]
					trailing = trailing[:0]
					spaceCount = 0
					overflow = false
				}
			}
			type clue struct {
				ref, start, end int
				text            string
			}
			var selected []clue
			used := map[int]bool{}
			remaining := limit
			rows, e := q.QueryContext(ctx, "SELECT ref,start,end,text FROM summary_clues ORDER BY score DESC,ordinal")
			if e != nil {
				return e
			}
			for rows.Next() {
				var c clue
				if e = rows.Scan(&c.ref, &c.start, &c.end, &c.text); e != nil {
					rows.Close()
					return e
				}
				if used[c.ref] {
					continue
				}
				overlap := false
				for _, p := range selected {
					overlap = overlap || c.start < p.end && p.start < c.end
				}
				if overlap {
					continue
				}
				cost := utf8.RuneCountInString("[" + strconv.Itoa(c.ref+1) + "] " + c.text)
				if len(selected) > 0 {
					cost++
				}
				if cost > remaining {
					continue
				}
				selected = append(selected, c)
				used[c.ref] = true
				remaining -= cost
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			if len(selected) > 0 {
				sort.Slice(selected, func(i, j int) bool { return selected[i].ref < selected[j].ref })
				lines := make([]string, 0, len(selected))
				for _, c := range selected {
					lines = append(lines, "["+strconv.Itoa(c.ref+1)+"] "+c.text)
				}
				result = strings.Join(lines, "\n")
				return nil
			}
			if limit < len(refs)*24 {
				return nil
			}
			width := (limit - 5*len(refs)) / len(refs)
			lines := make([]string, 0, len(refs))
			for n, ref := range refs {
				u, e := text.span(m, ref.StartRune, ref.EndRune)
				if e != nil {
					return e
				}
				reader := bufio.NewReaderSize(io.NewSectionReader(text.file, int64(u.StartByte), int64(u.EndByte-u.StartByte)), ChunkBytes)
				count := min(width, ref.EndRune-ref.StartRune)
				window := make([]rune, 0, count)
				best := ""
				score := -1.0
				for at := 0; at < ref.EndRune-ref.StartRune; at++ {
					c, _, e := reader.ReadRune()
					if e != nil {
						return e
					}
					if len(window) == count {
						copy(window, window[1:])
						window = window[:count-1]
					}
					window = append(window, c)
					if len(window) == count && ((at+1-count)%16 == 0 || at+1 == ref.EndRune-ref.StartRune) {
						v := string(window)
						s := scorer.Score(v)
						if e := scorer.Err(); e != nil {
							return e
						}
						if s > score {
							best = v
							score = s
						}
					}
				}
				lines = append(lines, "["+strconv.Itoa(n+1)+"] "+best)
			}
			result = strings.Join(lines, "\n")
			return nil
		})
	})
	return result, e
}
