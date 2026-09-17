package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"math"
	"strings"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/retrieval"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

type Ranked struct {
	ID      string
	Score   float64
	Signals []string
}

func (s *Store) openPayloadPart(ctx context.Context, payload string, part int) (io.ReadCloser, error) {
	c, done, err := s.reader(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := c.QueryContext(ctx, "SELECT bytes FROM content_chunks WHERE payload=? AND part=? ORDER BY ordinal", payload, part)
	if err != nil {
		done()
		return nil, err
	}
	return &chunkReader{ctx: ctx, rows: rows, release: done}, nil
}

// prepareLexical builds an invisible posting version. Publish switches its
// pointer and corpus statistics in the same transaction as the source asset.
func (s *Store) prepareLexical(ctx context.Context, p Staged, id string) error {
	var complete bool
	if err := s.view(ctx, func(q queryer) error {
		var asset string
		var length int64
		err := q.QueryRowContext(ctx, "SELECT asset,length FROM lexical_documents WHERE payload=?", p.ID).Scan(&asset, &length)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if asset != id {
			return errors.New("词法版本与资产不一致")
		}
		complete = length >= 0
		return nil
	}); err != nil || complete {
		return err
	}
	release, err := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 512*1024, false)
	if err != nil {
		return err
	}
	defer release()
	if err = s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM postings WHERE payload=?", p.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM lexical_contexts WHERE payload=?", p.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO lexical_documents VALUES(?,?,-1) ON CONFLICT(payload) DO UPDATE SET length=-1", p.ID, id)
		return err
	}); err != nil {
		return err
	}
	var length int64
	batch := map[[32]byte]int64{}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := s.write(ctx, func(tx *sql.Tx) error {
			stmt, err := tx.PrepareContext(ctx, "INSERT INTO postings VALUES(?,?,?) ON CONFLICT(term,payload) DO UPDATE SET frequency=frequency+excluded.frequency")
			if err != nil {
				return err
			}
			defer stmt.Close()
			for term, n := range batch {
				if _, err = stmt.ExecContext(ctx, term[:], p.ID, n); err != nil {
					return err
				}
			}
			return nil
		})
		clear(batch)
		return err
	}
	visit := func(d [32]byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		length++
		batch[d]++
		// Limit random B-tree page writes as well as the token payload.
		if len(batch) >= 64 {
			return flush()
		}
		return nil
	}
	if err = retrieval.VisitTermDigests(strings.NewReader(id), visit); err != nil {
		return err
	}
	r, err := s.openPayloadPart(ctx, p.ID, 0)
	if err != nil {
		return err
	}
	err = retrieval.VisitTermDigests(r, visit)
	r.Close()
	if err != nil {
		return err
	}
	r, err = s.openPayloadPart(ctx, p.ID, 1)
	if err != nil {
		return err
	}
	doc, err := streamjson.Parse(ctx, s.directory, r, resourcebudget.FromContext(ctx, s.budget), 256*resourcebudget.MiB)
	r.Close()
	if err != nil {
		return err
	}
	defer doc.Close()
	for _, field := range []string{"contexts", "explicit_relations"} {
		node, ok, err := doc.Root().Field(field)
		if err != nil {
			return err
		}
		if !ok || node.Kind == 'n' {
			continue
		}
		keys := []string{"key", "value"}
		if field == "explicit_relations" {
			keys = []string{"type", "target_id"}
		}
		cursor := node.Children()
		ordinal := 0
		for {
			item, e := cursor.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			for _, key := range keys {
				n, ok, e := item.Field(key)
				if e != nil {
					return e
				}
				if !ok {
					continue
				}
				r, e := n.Open(ctx)
				if e != nil {
					return e
				}
				e = retrieval.VisitTermDigests(r, visit)
				r.Close()
				if e != nil {
					return e
				}
			}
			if field == "contexts" {
				key, _, _ := item.Field("key")
				value, _, _ := item.Field("value")
				kd, e := foldNode(ctx, key)
				if e != nil {
					return e
				}
				vd, e := foldNode(ctx, value)
				if e != nil {
					return e
				}
				if e = s.write(ctx, func(tx *sql.Tx) error {
					lk, e := lowerNode(ctx, key)
					if e != nil {
						return e
					}
					lv, e := lowerNode(ctx, value)
					if e != nil {
						return e
					}
					_, e = tx.ExecContext(ctx, "INSERT INTO lexical_contexts VALUES(?,?,?,?,?,?)", p.ID, ordinal, kd[:], vd[:], lk[:], lv[:])
					return e
				}); e != nil {
					return e
				}
			}
			ordinal++
		}
	}
	if err = flush(); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE lexical_documents SET length=? WHERE payload=?", length, p.ID)
		return err
	})
}

func (s *Store) LexicalSearch(ctx context.Context, query string, contexts []domain.Context, limit int) ([]Ranked, error) {
	return s.LexicalSource(ctx, StringSource(query), strings.TrimSpace(query), contexts, limit)
}

// LexicalSource is also used for source-sized semantic candidate queries.
// Query terms, ordering and scores spill to connection-local SQLite tables.
func (s *Store) LexicalSource(ctx context.Context, source contract.ContentSource, identity string, contexts []domain.Context, limit int) ([]Ranked, error) {
	if limit <= 0 {
		limit = 10
	}
	if limit > 400 {
		return nil, errors.New("检索数量超过工具候选预算")
	}
	var result []Ranked
	err := s.view(ctx, func(q queryer) error {
		var count, total int64
		if err := q.QueryRowContext(ctx, "SELECT documents,terms FROM lexical_stats WHERE singleton=1").Scan(&count, &total); err != nil {
			return err
		}
		average := 1.0
		if count > 0 && total > 0 {
			average = float64(total) / float64(count)
		}
		_, err := q.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS query_terms(term BLOB PRIMARY KEY, run INTEGER, plane INTEGER, ordinal INTEGER, weight REAL) WITHOUT ROWID;
CREATE TEMP TABLE IF NOT EXISTS query_scores(id TEXT PRIMARY KEY,score REAL NOT NULL) WITHOUT ROWID;
DELETE FROM query_terms; DELETE FROM query_scores;`)
		if err != nil {
			return err
		}
		r, err := source.Open(ctx)
		if err != nil {
			return err
		}
		err = retrieval.VisitQueryTerms(r, func(term retrieval.TermPosition) error {
			_, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO query_terms VALUES(?,?,?,?,?)", term.Digest[:], term.Group, term.Plane, term.Position, term.Weight)
			return err
		})
		r.Close()
		if err != nil {
			return err
		}
		terms, err := q.QueryContext(ctx, "SELECT term,weight FROM query_terms")
		if err != nil {
			return err
		}
		for terms.Next() {
			var d []byte
			var weight float64
			if err = terms.Scan(&d, &weight); err != nil {
				terms.Close()
				return err
			}
			var frequency int64
			if err = q.QueryRowContext(ctx, "SELECT count(*) FROM postings p JOIN live_assets a ON a.payload=p.payload AND a.deleted=0 WHERE p.term=?", d).Scan(&frequency); err != nil {
				terms.Close()
				return err
			}
			idf := 0.0
			if frequency > 0 && frequency <= max(count/4, 16) {
				idf = math.Log(1+(float64(count-frequency)+0.5)/(float64(frequency)+0.5)) * weight
			}
			if _, err = q.ExecContext(ctx, "UPDATE query_terms SET weight=? WHERE term=?", idf, d); err != nil {
				terms.Close()
				return err
			}
		}
		err = terms.Err()
		terms.Close()
		if err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT a.id,a.payload,d.length,p.frequency,t.weight FROM query_terms t JOIN postings p ON p.term=t.term JOIN live_assets a ON a.payload=p.payload AND a.deleted=0 JOIN lexical_documents d ON d.payload=a.payload WHERE t.weight>0 ORDER BY a.id,t.run,t.plane,t.ordinal`)
		if err != nil {
			return err
		}
		var last, payload string
		score := 0.0
		put := func() error {
			if last == "" {
				return nil
			}
			ok, err := matchStoredContexts(ctx, q, "lexical_contexts", "payload", payload, contexts)
			if err != nil || !ok {
				return err
			}
			_, err = q.ExecContext(ctx, "INSERT INTO query_scores VALUES(?,?)", last, score)
			return err
		}
		for rows.Next() {
			var id, p string
			var length, f int64
			var weight float64
			if err = rows.Scan(&id, &p, &length, &f, &weight); err != nil {
				rows.Close()
				return err
			}
			if id != last {
				if err = put(); err != nil {
					rows.Close()
					return err
				}
				last, payload, score = id, p, 0
			}
			freq := float64(f)
			score += weight * (freq * 2.2) / (freq + 1.2*(1-0.75+0.75*float64(length)/average))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if err = put(); err != nil {
			return err
		}
		id := identity
		err = q.QueryRowContext(ctx, "SELECT payload FROM live_assets WHERE id=? AND deleted=0", id).Scan(&payload)
		if err == nil {
			ok, e := matchStoredContexts(ctx, q, "lexical_contexts", "payload", payload, contexts)
			if e != nil {
				return e
			}
			if ok {
				_, err = q.ExecContext(ctx, "INSERT INTO query_scores VALUES(?,1000) ON CONFLICT(id) DO UPDATE SET score=score+1000", id)
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		if err != nil {
			return err
		}
		rows, err = q.QueryContext(ctx, "SELECT id,score FROM query_scores WHERE score>0 ORDER BY score DESC,id LIMIT ?", limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var hit Ranked
			if err = rows.Scan(&hit.ID, &hit.Score); err != nil {
				return err
			}
			identity := strings.EqualFold(id, hit.ID)
			if identity {
				hit.Signals = append(hit.Signals, "identity")
			}
			if !identity || hit.Score > 1000 {
				hit.Signals = append(hit.Signals, "lexical")
			}
			result = append(result, hit)
		}
		return rows.Err()
	})
	return result, err
}
