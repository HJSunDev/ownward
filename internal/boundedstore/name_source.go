package boundedstore

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"strings"
	"unicode"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semanticstream"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

// visitNameWords preserves the original word and adjacent-Han terms while
// spooling arbitrarily long words instead of retaining them in memory.
func (s *Store) visitNameWords(ctx context.Context, source contract.ContentSource, visit func(io.Reader) error) error {
	f, e := resourcebudget.TempFile(ctx, s.directory, "name-word-", 256*resourcebudget.MiB)
	if e != nil {
		return e
	}
	defer f.Close()
	r, e := source.Open(ctx)
	if e != nil {
		return e
	}
	defer r.Close()
	b := bufio.NewReaderSize(r, ChunkBytes)
	out := bufio.NewWriterSize(io.NewOffsetWriter(f, 0), ChunkBytes)
	var size int64
	small := make([]byte, 0, ChunkBytes)
	spilled := false
	var prev rune
	flush := func() error {
		if spilled {
			if _, e := out.Write(small); e != nil {
				return e
			}
			if e := out.Flush(); e != nil {
				return e
			}
			if e := visit(io.NewSectionReader(f, 0, size)); e != nil {
				return e
			}
		} else if len(small) > 0 {
			if e := visit(bytes.NewReader(small)); e != nil {
				return e
			}
		}
		small = small[:0]
		spilled = false
		size = 0
		prev = 0
		out.Reset(io.NewOffsetWriter(f, 0))
		return ctx.Err()
	}

	for {
		v, _, e := b.ReadRune()
		if e == io.EOF {
			return flush()
		}
		if e != nil {
			return e
		}
		v = unicode.ToLower(v)
		if unicode.IsLetter(v) || unicode.IsNumber(v) {
			encoded := string(v)
			if len(small)+len(encoded) > cap(small) {
				if _, e := out.Write(small); e != nil {
					return e
				}
				small = small[:0]
				spilled = true
			}
			small = append(small, encoded...)
			size += int64(len(encoded))

			if unicode.Is(unicode.Han, v) && unicode.Is(unicode.Han, prev) {
				if e = visit(strings.NewReader(string([]rune{prev, v}))); e != nil {
					return e
				}
			}
			prev = v
		} else {
			if e = flush(); e != nil {
				return e
			}
		}
	}
}
func (s *Store) stageNameSource(ctx context.Context, organization string, source contract.ContentSource) error {
	return s.visitNameWords(ctx, source, func(r io.Reader) error {
		h := sha256.New()
		if _, e := io.CopyBuffer(h, r, make([]byte, ChunkBytes)); e != nil {
			return e
		}
		return s.write(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO graph_names VALUES(?,?)", hex.EncodeToString(h.Sum(nil)), organization)
			return e
		})
	})
}
func (s *Store) NameSource(ctx context.Context, generation string, source contract.ContentSource, limit int) ([]Ranked, error) {
	if _, ok := ctx.Value(snapshotKey{}).(snapshot); !ok {
		var out []Ranked
		e := s.WithSnapshot(ctx, func(ctx context.Context) error {
			var e error
			out, e = s.NameSource(ctx, generation, source, limit)
			return e
		})
		return out, e
	}

	if limit <= 0 {
		return nil, nil
	}
	limit = min(limit, 100)
	sorted, e := streamjson.NewSortedStrings(ctx, s.directory)
	if e != nil {
		return nil, e
	}
	defer sorted.Close()
	e = s.view(ctx, func(q queryer) error {
		if _, e := q.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS name_seen(term BLOB PRIMARY KEY) WITHOUT ROWID;DELETE FROM name_seen"); e != nil {
			return e
		}
		recent := map[[32]byte]bool{}
		buffer := make([]byte, ChunkBytes)
		return s.visitNameWords(ctx, source, func(r io.Reader) error {
			// Replay only a new term into the external sorter.
			h := sha256.New()
			if _, e := io.CopyBuffer(h, r, buffer); e != nil {
				return e
			}
			var d [32]byte
			copy(d[:], h.Sum(nil))
			if recent[d] {
				return nil
			}
			if len(recent) >= 512 {
				clear(recent)
			}
			recent[d] = true
			result, e := q.ExecContext(ctx, "INSERT OR IGNORE INTO name_seen VALUES(?)", d[:])
			if e != nil {
				return e
			}
			added, e := result.RowsAffected()
			if e != nil || added == 0 {
				return e
			}
			if _, e = r.(io.Seeker).Seek(0, io.SeekStart); e != nil {
				return e
			}
			return sorted.Add(r)

		})
	})
	if e != nil {
		return nil, e
	}

	doc, e := streamjson.Build(ctx, s.directory, resourcebudget.FromContext(ctx, s.budget), 256*resourcebudget.MiB, sorted.WriteJSON)
	if e != nil {
		return nil, e
	}
	defer doc.Close()
	var result []Ranked
	e = s.view(ctx, func(q queryer) error {
		if _, e := q.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS source_names(term TEXT PRIMARY KEY,ordinal INTEGER) WITHOUT ROWID;DELETE FROM source_names;CREATE TEMP TABLE IF NOT EXISTS source_name_scores(asset TEXT PRIMARY KEY,score REAL) WITHOUT ROWID;DELETE FROM source_name_scores;"); e != nil {
			return e
		}
		c := doc.Root().Children()
		ordinal := 0
		for {
			n, e := c.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			digest, e := (semanticstream.Text{Source: n}).Digest(ctx)
			if e != nil {
				return e
			}
			if _, e = q.ExecContext(ctx, "INSERT OR IGNORE INTO source_names VALUES(?,?)", digest, ordinal); e != nil {
				return e
			}
			ordinal++
		}
		rows, e := q.QueryContext(ctx, `SELECT o.id,o.asset FROM source_names t JOIN graph_names n ON n.term=t.term JOIN organizations o ON o.id=n.organization JOIN organization_current c ON c.organization=o.id AND c.generation=? JOIN assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0 ORDER BY t.ordinal,o.asset LIMIT ?`, generation, limit*16)
		if e != nil {
			return e
		}
		for rows.Next() {
			var org, id string
			if e = rows.Scan(&org, &id); e != nil {
				rows.Close()
				return e
			}
			valid, e := organizationInputsCurrent(ctx, q, org)
			if e != nil {
				rows.Close()
				return e
			}
			if valid {
				if _, e = q.ExecContext(ctx, "INSERT INTO source_name_scores VALUES(?,1) ON CONFLICT(asset) DO UPDATE SET score=score+1", id); e != nil {
					rows.Close()
					return e
				}
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		rows, e = q.QueryContext(ctx, "SELECT asset,score FROM source_name_scores ORDER BY score DESC,asset LIMIT ?", limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v Ranked
			if e = rows.Scan(&v.ID, &v.Score); e != nil {
				return e
			}
			result = append(result, v)
		}
		return rows.Err()
	})
	return result, e
}
