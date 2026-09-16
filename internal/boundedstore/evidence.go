package boundedstore

import (
	"bufio"
	"context"
	"crypto/sha256"

	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"

	"github.com/HJSunDev/ownward/internal/semanticstream"
)

type textFile struct {
	file *resourcebudget.File
	size int64
}

func (t textFile) Open(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(io.NewSectionReader(t.file, 0, t.size)), nil
}
func (s *Store) spoolContent(ctx context.Context, m contract.AssetMeta) (textFile, error) {
	var out textFile
	f, e := resourcebudget.TempFile(ctx, s.directory, "evidence-", 256*resourcebudget.MiB)
	if e != nil {
		return out, e
	}
	r, e := s.OpenContent(ctx, m.ID, m.Revision)
	if e != nil {
		f.Close()
		return out, e
	}
	n, e := io.CopyBuffer(f, r, make([]byte, ChunkBytes))
	r.Close()
	if e != nil {
		f.Close()
		return out, e
	}
	return textFile{f, n}, nil
}
func (t textFile) span(m contract.AssetMeta, start, end int) (derived.EvidenceUnit, error) {
	u := derived.EvidenceUnit{Schema: derived.EvidenceUnitSchema, SourceID: m.ID, SourceRevision: m.Revision, StartRune: start, EndRune: end}
	if start < 0 || end <= start {
		return u, errors.New("证据区间无效")
	}
	b := bufio.NewReaderSize(io.NewSectionReader(t.file, 0, t.size), ChunkBytes)
	at := 0
	for i := 0; i < end; i++ {
		if i == start {
			u.StartByte = at
		}
		_, n, e := b.ReadRune()
		if e != nil {
			return u, e
		}
		at += n
	}
	u.EndByte = at
	return u, nil
}
func (t textFile) reference(u derived.EvidenceUnit) (domain.EvidenceReference, error) {
	h := sha256.New()
	if _, e := io.CopyBuffer(h, io.NewSectionReader(t.file, int64(u.StartByte), int64(u.EndByte-u.StartByte)), make([]byte, ChunkBytes)); e != nil {
		return domain.EvidenceReference{}, e
	}
	return derived.StreamEvidenceReference(u.SourceID, u.SourceRevision, u.StartRune, u.EndRune, u.StartByte, u.EndByte, hex.EncodeToString(h.Sum(nil)))
}

// RankEvidence keeps passage ordering on disk. Only the selected references
// and one bounded text window are retained, regardless of document length.
func (s *Store) RankEvidence(ctx context.Context, generation, id, query string, limit int) ([]domain.EvidenceReference, error) {
	return s.RankEvidenceSource(ctx, generation, id, StringSource(query), limit)
}
func (s *Store) RankEvidenceSource(ctx context.Context, generation, id string, query contract.ContentSource, limit int) ([]domain.EvidenceReference, error) {
	if limit <= 0 {
		return nil, nil
	}
	if limit > 8 {
		return nil, errors.New("证据数量超过工具预算")
	}
	release, e := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 1024*1024, false)
	if e != nil {
		return nil, e
	}
	defer release()
	var refs []domain.EvidenceReference
	e = s.WithSnapshot(ctx, func(ctx context.Context) error {
		m, e := s.ReadAssetMeta(ctx, id, 0)
		if e != nil {
			return e
		}
		text, e := s.spoolContent(ctx, m)
		if e != nil {
			return e
		}
		defer text.file.Close()
		walk := func(visit func(derived.EvidenceUnit) error) error {
			return derived.WalkEvidenceRanges(id, m.Revision, io.NewSectionReader(text.file, 0, text.size), visit)
		}
		scorer, e := s.SourceScorer(ctx, query, func(visit func(string) error) error {
			return walk(func(u derived.EvidenceUnit) error { return visit(u.Content) })
		})
		if e != nil {
			return e
		}
		return s.view(ctx, func(q queryer) error {
			if _, e := q.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS evidence_choices(ordinal INTEGER PRIMARY KEY,score REAL,start INTEGER,spans BLOB); DELETE FROM evidence_choices"); e != nil {
				return e
			}
			ordinal := 0
			put := func(units []derived.EvidenceUnit, score float64) error {
				if len(units) == 0 {
					return nil
				}
				width := 0
				for i := range units {
					width += units[i].EndRune - units[i].StartRune
					units[i].Content = ""
				}
				score /= math.Sqrt(math.Max(1, float64(width)/float64(derived.DefaultEvidenceUnitRunes)))
				// Byte positions are internal and deliberately absent from EvidenceUnit's
				// public JSON. The temporary row needs them for exact source hashing.
				spans := make([][4]int, len(units))
				for i, u := range units {
					spans[i] = [4]int{u.StartRune, u.EndRune, u.StartByte, u.EndByte}
				}
				data, e := json.Marshal(spans)
				if e != nil {
					return e
				}
				_, e = q.ExecContext(ctx, "INSERT INTO evidence_choices VALUES(?,?,?,?)", ordinal, score, units[0].StartRune, data)
				ordinal++
				return e
			}
			if e = s.VisitUnits(ctx, generation, id, func(p UnitProjection) error {
				var units []derived.EvidenceUnit
				for _, span := range append([]GraphSpan{p.Span}, p.Context...) {
					u, e := text.span(m, int(span.Start), int(span.End))
					if e != nil {
						return e
					}
					units = append(units, u)
				}
				return s.WithUnit(ctx, p, func(unit semanticstream.Unit) error {
					terms := []semanticstream.Text{unit.Statement, unit.Selector.Exact}
					for _, m := range unit.Mentions {
						terms = append(terms, m.Name, m.Role)
					}
					readers := []io.Reader{}
					for i, t := range terms {
						r, e := t.Open(ctx)
						if e != nil {
							return e
						}
						defer r.Close()
						if i > 0 {
							readers = append(readers, strings.NewReader(" "))
						}
						readers = append(readers, r)
					}
					score, e := scorer.ScoreReader(io.MultiReader(readers...))
					if e != nil {
						return e
					}
					return put(units, score)
				})
			}); e != nil {
				return e
			}
			if e = walk(func(u derived.EvidenceUnit) error {
				expanded, e := derived.CompleteStreamPassage(text.file, text.size, u)
				if e != nil {
					return e
				}
				score := scorer.Score(u.Content)
				if e := scorer.Err(); e != nil {
					return e
				}
				return put([]derived.EvidenceUnit{expanded}, score)
			}); e != nil {
				return e
			}
			rows, e := q.QueryContext(ctx, "SELECT spans FROM evidence_choices WHERE score>0 ORDER BY score DESC,start,ordinal")
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var data []byte
				if e = rows.Scan(&data); e != nil {
					return e
				}
				var spans [][4]int
				if e = json.Unmarshal(data, &spans); e != nil {
					return e
				}
				var pending []domain.EvidenceReference
				for _, p := range spans {
					covered := false
					for _, r := range refs {
						covered = covered || (r.StartRune <= p[0] && r.EndRune >= p[1])
					}
					if covered {
						continue
					}
					ref, e := text.reference(derived.EvidenceUnit{SourceID: id, SourceRevision: m.Revision, StartRune: p[0], EndRune: p[1], StartByte: p[2], EndByte: p[3]})
					if e != nil {
						return e
					}
					pending = append(pending, ref)
				}
				if len(refs)+len(pending) > limit {
					continue
				}
				refs = append(refs, pending...)
				if len(refs) == limit {
					break
				}
			}
			return rows.Err()
		})
	})
	return refs, e
}
