package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"math"
	"strings"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/retrieval"
)

// TextScorer keeps the existing passage scoring, with disk spill for large queries.
type TextScorer interface {
	Score(string) float64
	ScoreReader(io.Reader) (float64, error)
	Err() error
}
type memoryTextScorer struct{ retrieval.QueryTextScorer }

func (memoryTextScorer) Err() error { return nil }

type diskTextScorer struct {
	ctx     context.Context
	q       queryer
	failure error
}

func (d *diskTextScorer) Err() error { return d.failure }
func (d *diskTextScorer) Score(text string) float64 {
	v, e := d.ScoreReader(strings.NewReader(text))
	if e != nil {
		d.failure = e
	}
	return v
}
func (d *diskTextScorer) ScoreReader(r io.Reader) (float64, error) {
	if d.failure != nil {
		return 0, d.failure
	}
	if _, e := d.q.ExecContext(d.ctx, "DELETE FROM text_seen"); e != nil {
		return 0, e
	}
	score := 0.0
	e := retrieval.VisitTermDigests(r, func(term [32]byte) error {
		var weight float64
		e := d.q.QueryRowContext(d.ctx, "SELECT weight FROM text_terms WHERE term=?", term[:]).Scan(&weight)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		result, e := d.q.ExecContext(d.ctx, "INSERT OR IGNORE INTO text_seen VALUES(?)", term[:])
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e == nil && n > 0 {
			score += weight
		}
		return e
	})
	return score, e
}

// The scorer lives inside the caller's read snapshot. No corpus-sized map is retained.
func (s *Store) SourceScorer(ctx context.Context, source contract.ContentSource, walk func(func(string) error) error) (TextScorer, error) {
	r, e := source.Open(ctx)
	if e != nil {
		return nil, e
	}
	data, e := io.ReadAll(io.LimitReader(r, 8193))
	r.Close()
	if e != nil {
		return nil, e
	}
	if len(data) <= 8192 {
		if walk == nil {
			return memoryTextScorer{retrieval.NewQueryTextScorer(string(data))}, nil
		}
		v, e := retrieval.NewStreamPassageTextScorer(string(data), walk)
		return memoryTextScorer{v}, e
	}
	d := &diskTextScorer{ctx: ctx}
	e = s.view(ctx, func(q queryer) error {
		d.q = q
		if _, e := q.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS text_terms(term BLOB PRIMARY KEY,weight REAL,freq INTEGER) WITHOUT ROWID;
CREATE TEMP TABLE IF NOT EXISTS text_seen(term BLOB PRIMARY KEY) WITHOUT ROWID; DELETE FROM text_terms; DELETE FROM text_seen;`); e != nil {
			return e
		}
		r, e := source.Open(ctx)
		if e != nil {
			return e
		}
		e = retrieval.VisitQueryTerms(r, func(t retrieval.TermPosition) error {
			_, e := q.ExecContext(ctx, "INSERT OR IGNORE INTO text_terms VALUES(?,?,0)", t.Digest[:], t.Weight)
			return e
		})
		r.Close()
		if e != nil {
			return e
		}
		passages := 0
		if walk != nil {
			e = walk(func(text string) error {
				passages++
				if _, e := d.ScoreReader(strings.NewReader(text)); e != nil {
					return e
				}
				_, e := q.ExecContext(ctx, "UPDATE text_terms SET freq=freq+1 WHERE term IN(SELECT term FROM text_seen)")
				return e
			})
			if e != nil {
				return e
			}
		}
		if passages > 0 {
			rows, e := q.QueryContext(ctx, "SELECT term,weight,freq FROM text_terms")
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var term []byte
				var weight float64
				var frequency int
				if e = rows.Scan(&term, &weight, &frequency); e != nil {
					return e
				}
				weight *= math.Log1p(float64(passages) / float64(1+frequency))
				if _, e = q.ExecContext(ctx, "UPDATE text_terms SET weight=? WHERE term=?", weight, term); e != nil {
					return e
				}
			}
			return rows.Err()
		}
		return nil
	})
	return d, e
}
