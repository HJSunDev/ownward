package boundedstore

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/semanticstream"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

// Projections contain positions and identities, never copies of source text.
type GraphSpan struct{ Start, End int64 }
type GraphText struct {
	Organization string
	Start, End   int64
}
type UnitMention struct {
	ID, Fingerprint string
	Span            GraphSpan
}
type UnitProjection struct {
	ID, Fingerprint string
	Ordinal         int
	Text            GraphText
	Span            GraphSpan
	Context         []GraphSpan
	Mentions        []UnitMention
}
type EndpointProjection struct {
	Span    GraphSpan
	Context []GraphSpan
}
type LinkProjection struct {
	Link      semantics.GroundedLink
	Meaning   GraphText
	Endpoints []EndpointProjection
}
type Edge struct {
	derived.Edge
	Meaning   GraphText            `json:"-"`
	Endpoints []EndpointProjection `json:"-"`
}
type NavigationPage struct {
	Edges        []Edge
	Continuation string
	Incomplete   bool
	Examined     int
}

func (s *Store) OpenGraphText(ctx context.Context, ref GraphText) (*streamjson.Document, error) {
	q, done, e := s.snapshotReader(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := q.QueryContext(ctx, `SELECT substr(bytes,max(0,?-ordinal*65536)+1,min(length(bytes),?-ordinal*65536)-max(0,?-ordinal*65536)) FROM organization_chunks WHERE organization=? AND ordinal>=? AND ordinal<=? ORDER BY ordinal`, ref.Start, ref.End, ref.Start, ref.Organization, ref.Start/ChunkBytes, max(0, ref.End-1)/ChunkBytes)
	if e != nil {
		done()
		return nil, e
	}
	r := &chunkReader{ctx: ctx, rows: rows, release: done}
	defer r.Close()
	return streamjson.Parse(ctx, s.directory, r, resourcebudget.FromContext(ctx, s.budget), 256*resourcebudget.MiB)
}
func (s *Store) VisitUnits(ctx context.Context, generation, id string, visit func(UnitProjection) error) error {
	if _, ok := ctx.Value(snapshotKey{}).(snapshot); !ok {
		return s.WithSnapshot(ctx, func(ctx context.Context) error { return s.VisitUnits(ctx, generation, id, visit) })
	}
	v, e := s.CurrentOrganization(ctx, generation, id)
	if errors.Is(e, sql.ErrNoRows) || errors.Is(e, ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	return s.view(ctx, func(q queryer) error {
		rows, e := q.QueryContext(ctx, "SELECT data FROM graph_units WHERE organization=? ORDER BY ordinal", v.ID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if e = rows.Scan(&data); e != nil {
				return e
			}
			var u UnitProjection
			if e = json.Unmarshal(data, &u); e != nil {
				return e
			}
			if e = visit(u); e != nil {
				return e
			}
		}
		return rows.Err()
	})
}
func projectionSpan(ctx context.Context, directory string, source contract.ContentSource, sel semanticstream.Selector) (GraphSpan, error) {
	a, b, e := streamjson.ResolveSelector(ctx, directory, source, sel.Prefix, sel.Exact, sel.Suffix)
	return GraphSpan{a, b}, e
}
func (s *Store) projectUnits(ctx context.Context, v OrganizationVersion, org streamjson.Node, putNames func(semanticstream.Text) error) error {
	m, e := s.ReadAssetMeta(ctx, v.Asset, v.Revision)
	if e != nil {
		return e
	}
	body, e := s.spoolContent(ctx, m)
	if e != nil {
		return e
	}
	defer body.file.Close()
	ordinal := 0
	return visitArray(org, "units", func(n streamjson.Node) error {
		u, e := semanticstream.ReadUnit(n)
		if e != nil {
			return e
		}
		p := UnitProjection{Ordinal: ordinal, Text: GraphText{v.ID, n.Start, n.End}}
		ordinal++
		p.ID, e = u.ID.Digest(ctx)
		if e != nil {
			return e
		}
		p.Fingerprint, e = u.Fingerprint(ctx)
		if e != nil {
			return e
		}
		p.Span, e = projectionSpan(ctx, s.directory, body, u.Selector)
		if e != nil {
			return e
		}
		for _, c := range u.Context {
			span, e := projectionSpan(ctx, s.directory, body, c)
			if e != nil {
				return e
			}
			p.Context = append(p.Context, span)
		}
		for _, m := range u.Mentions {
			var mention UnitMention
			mention.ID, e = m.ID.Digest(ctx)
			if e != nil {
				return e
			}
			mention.Fingerprint, e = m.Fingerprint(ctx)
			if e != nil {
				return e
			}
			mention.Span, e = projectionSpan(ctx, s.directory, body, m.Selector)
			if e != nil {
				return e
			}
			p.Mentions = append(p.Mentions, mention)
			if e = putNames(m.Name); e != nil {
				return e
			}
		}
		data, e := json.Marshal(p)
		if e != nil {
			return e
		}
		return s.write(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, "INSERT INTO graph_units VALUES(?,?,?,?,?)", v.ID, p.ID, p.Fingerprint, p.Ordinal, data)
			return e
		})
	})
}
func (s *Store) projectLink(ctx context.Context, v OrganizationVersion, n streamjson.Node) (LinkProjection, error) {
	var p LinkProjection
	l, e := semanticstream.ReadLink(n)
	if e != nil {
		return p, e
	}
	meaning, _, e := n.Field("meaning")
	if e != nil {
		return p, e
	}
	p.Meaning = GraphText{v.ID, meaning.Start, meaning.End}
	p.Link.ID = l.ID
	p.Link.Type = l.Type
	endpoints := append([]semanticstream.Endpoint{l.Source, l.Target}, l.Conditions...)
	for i, ep := range endpoints {
		out := semantics.GraphEndpoint{AssetID: ep.AssetID, Revision: ep.Revision, Snapshot: ep.Snapshot, Fingerprint: ep.Fingerprint, MentionFingerprint: ep.MentionFingerprint}
		empty, e := ep.UnitID.Empty(ctx)
		if e != nil {
			return p, e
		}
		if !empty {
			out.UnitID, e = ep.UnitID.Digest(ctx)
			if e != nil {
				return p, e
			}
		}
		empty, e = ep.MentionID.Empty(ctx)
		if e != nil {
			return p, e
		}
		if !empty {
			out.MentionID, e = ep.MentionID.Digest(ctx)
			if e != nil {
				return p, e
			}
		}
		var loc EndpointProjection
		if out.UnitID == "" {
			m, e := s.ReadAssetMeta(ctx, ep.AssetID, ep.Revision)
			if e != nil {
				return p, e
			}
			body, e := s.spoolContent(ctx, m)
			if e != nil {
				return p, e
			}
			empty, e = ep.Selector.Exact.Empty(ctx)
			if e == nil {
				if empty {
					r, _ := body.Open(ctx)
					var count int64
					count, e = countGraphRunes(ctx, r)
					r.Close()
					loc.Span = GraphSpan{0, count}
				} else {
					loc.Span, e = projectionSpan(ctx, s.directory, body, ep.Selector)
				}
			}
			body.file.Close()
			if e != nil {
				return p, e
			}
		}
		p.Endpoints = append(p.Endpoints, loc)
		if i == 0 {
			p.Link.Source = out
		} else if i == 1 {
			p.Link.Target = out
		} else {
			p.Link.Conditions = append(p.Link.Conditions, out)
		}
	}
	return p, nil
}

// Keep a file handle scoped to the callback; callers never retain a parsed
// organization or one of its potentially large strings in a navigation page.
func (s *Store) WithUnit(ctx context.Context, p UnitProjection, visit func(semanticstream.Unit) error) error {
	d, e := s.OpenGraphText(ctx, p.Text)
	if e != nil {
		return e
	}
	defer d.Close()
	u, e := semanticstream.ReadUnit(d.Root())
	if e != nil {
		return e
	}
	return visit(u)
}
func (s *Store) WriteGraphMeaning(ctx context.Context, w io.Writer, p GraphText) error {
	d, e := s.OpenGraphText(ctx, p)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Root().Copy(w)
}

func countGraphRunes(ctx context.Context, r io.Reader) (int64, error) {
	b := bufio.NewReaderSize(r, ChunkBytes)
	var n int64
	for {
		_, _, e := b.ReadRune()
		if e == io.EOF {
			return n, nil
		}
		if e != nil {
			return n, e
		}
		n++
		if n%16384 == 0 {
			if e = ctx.Err(); e != nil {
				return n, e
			}
		}
	}
}
