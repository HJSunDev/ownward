package boundedstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/semanticstream"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func (s *Store) stageGraph(ctx context.Context, v OrganizationVersion, analysis, org streamjson.Node, hasOrg bool) error {
	ordinal := 0
	putLink := func(source, target, typ string, grounded bool, data []byte) error {
		err := s.write(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, "INSERT INTO graph_links VALUES(?,?,?,?,?,?,?)", v.ID, ordinal, source, target, typ, grounded, data)
			return e
		})
		ordinal++
		return err
	}
	putNames := func(name semanticstream.Text) error { return s.stageNameSource(ctx, v.ID, name) }

	meta, e := s.ReadAssetMeta(ctx, v.Asset, v.Revision)
	if e != nil {
		return e
	}
	r, e := s.OpenDetails(ctx, meta.ID, meta.Revision)
	if e != nil {
		return e
	}
	details, e := streamjson.Parse(ctx, s.directory, r, resourcebudget.FromContext(ctx, s.budget), 256*resourcebudget.MiB)
	r.Close()
	if e != nil {
		return e
	}
	defer details.Close()
	if e = visitArray(details.Root(), "explicit_relations", func(n streamjson.Node) error {
		target, e := nodeString(n, "target_id")
		if e != nil {
			return e
		}
		typ, e := nodeString(n, "type")
		if e != nil {
			return e
		}
		data, e := json.Marshal(semantics.Relation{Type: typ, TargetID: target, Confidence: 1})
		if e != nil {
			return e
		}
		if e = s.write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO graph_explicit VALUES(?,?)", v.ID, target)
			return err
		}); e != nil {
			return e
		}
		return putLink(v.Asset, target, typ, false, data)
	}); e != nil {
		return e
	}
	err := visitArray(analysis, "relations", func(n streamjson.Node) error {
		var relation semantics.Relation
		if e := n.DecodeSmall(&relation, 256*1024); e != nil {
			return e
		}
		if e := s.write(ctx, func(tx *sql.Tx) error {
			for _, id := range []string{relation.TargetID, relation.InferredBy} {
				if id != "" && id != v.Asset && relation.TargetRevision != 0 {
					if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO dependencies VALUES(?,?,?,'')", v.ID, id, relation.TargetRevision); e != nil {
						return e
					}
				}
			}
			return nil
		}); e != nil {
			return e
		}
		data, e := json.Marshal(relation)
		if e != nil {
			return e
		}
		source, target := v.Asset, relation.TargetID
		if relation.Direction == "incoming" {
			source, target = target, source
		}
		var overridden bool
		if e = s.view(ctx, func(q queryer) error {
			return q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM graph_explicit WHERE organization=? AND target=?)", v.ID, relation.TargetID).Scan(&overridden)
		}); e != nil {
			return e
		}
		if overridden {
			return nil
		}
		return putLink(source, target, relation.Type, false, data)
	})
	if err != nil || !hasOrg {
		return err
	}
	if e := s.projectUnits(ctx, v, org, putNames); e != nil {
		return e
	}
	return visitArray(org, "links", func(n streamjson.Node) error {
		link, e := semanticstream.ReadLink(n)
		if e != nil {
			return e
		}
		for _, ep := range append([]semanticstream.Endpoint{link.Source, link.Target}, link.Conditions...) {
			if ep.AssetID == v.Asset {
				if e = putNames(ep.ObjectName); e != nil {
					return e
				}
			}
		}
		projection, e := s.projectLink(ctx, v, n)
		if e != nil {
			return e
		}
		data, e := json.Marshal(projection)
		if e != nil {
			return e
		}
		return putLink(link.Source.AssetID, link.Target.AssetID, link.Type, true, data)
	})
}

func (s *Store) NameSearch(ctx context.Context, generation, query string, limit int) ([]Ranked, error) {
	return s.NameSource(ctx, generation, StringSource(query), limit)
}

func bindGraphEndpoint(ctx context.Context, q queryer, generation string, ep semantics.GraphEndpoint, loc EndpointProjection) (semantics.GraphEndpoint, EndpointProjection, bool, error) {
	var rev uint64
	e := q.QueryRowContext(ctx, "SELECT revision FROM assets WHERE id=? AND deleted=0", ep.AssetID).Scan(&rev)
	if errors.Is(e, sql.ErrNoRows) {
		return ep, loc, false, nil
	}
	if e != nil || rev != ep.Revision {
		return ep, loc, false, e
	}
	if ep.UnitID == "" {
		return ep, loc, true, nil
	}
	var org, snapshot string
	e = q.QueryRowContext(ctx, "SELECT o.id,o.snapshot FROM organization_current c JOIN organizations o ON o.id=c.organization WHERE c.generation=? AND c.asset=?", generation, ep.AssetID).Scan(&org, &snapshot)
	if errors.Is(e, sql.ErrNoRows) {
		return ep, loc, false, nil
	}
	if e != nil {
		return ep, loc, false, e
	}
	valid, e := organizationInputsCurrent(ctx, q, org)
	if e != nil || !valid {
		return ep, loc, false, e
	}
	query, key := "SELECT data FROM graph_units WHERE organization=? AND id=?", ep.UnitID
	if snapshot != ep.Snapshot {
		if ep.Fingerprint == "" {
			return ep, loc, false, nil
		}
		query = "SELECT data FROM graph_units WHERE organization=? AND fingerprint=?"
		key = ep.Fingerprint
	}
	rows, e := q.QueryContext(ctx, query, org, key)
	if e != nil {
		return ep, loc, false, e
	}
	var unit UnitProjection
	count := 0
	for rows.Next() {
		var data []byte
		if e = rows.Scan(&data); e != nil {
			rows.Close()
			return ep, loc, false, e
		}
		if e = json.Unmarshal(data, &unit); e != nil {
			rows.Close()
			return ep, loc, false, e
		}
		count++
	}
	e = rows.Err()
	rows.Close()
	if e != nil || count != 1 {
		return ep, loc, false, e
	}
	loc.Span = unit.Span
	loc.Context = append([]GraphSpan(nil), unit.Context...)
	if ep.MentionID != "" {
		found := -1
		for i, m := range unit.Mentions {
			if ep.MentionFingerprint != "" && ep.MentionFingerprint == m.Fingerprint || ep.MentionFingerprint == "" && snapshot == ep.Snapshot && ep.MentionID == m.ID {
				if found >= 0 {
					return ep, loc, false, nil
				}
				found = i
			}
		}
		if found < 0 {
			return ep, loc, false, nil
		}
		ep.MentionID = unit.Mentions[found].ID
		loc.Span = unit.Mentions[found].Span
		if loc.Span != unit.Span {
			loc.Context = append([]GraphSpan{unit.Span}, loc.Context...)
		}
	}
	ep.UnitID = unit.ID
	ep.Snapshot = snapshot
	return ep, loc, true, nil
}

func graphEdge(ctx context.Context, q queryer, generation, owner, organization string, grounded bool, data []byte) (Edge, bool, error) {
	valid, err := organizationInputsCurrent(ctx, q, organization)
	if err != nil || !valid {
		return Edge{}, false, err
	}
	if !grounded {
		var r semantics.Relation
		if err = json.Unmarshal(data, &r); err != nil {
			return Edge{}, false, err
		}
		var revision uint64
		err = q.QueryRowContext(ctx, "SELECT a.revision FROM organization_current c JOIN organizations o ON o.id=c.organization JOIN assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0 WHERE c.generation=? AND c.asset=?", generation, r.TargetID).Scan(&revision)
		if errors.Is(err, sql.ErrNoRows) {
			return Edge{}, false, nil
		}
		if err != nil {
			return Edge{}, false, err
		}
		if r.TargetID == owner || (r.TargetRevision != 0 && r.TargetRevision != revision) {
			return Edge{}, false, nil
		}
		source, target := owner, r.TargetID
		if r.Direction == "incoming" {
			source, target = target, source
			var explicit bool
			if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM graph_explicit e JOIN organization_current c ON c.organization=e.organization JOIN organizations o ON o.id=c.organization JOIN assets a ON a.id=c.asset AND a.revision=o.revision AND a.deleted=0 WHERE c.generation=? AND c.asset=? AND e.target=?)`, generation, source, target).Scan(&explicit); err != nil {
				return Edge{}, false, err
			}
			if explicit {
				return Edge{}, false, nil
			}
		}
		return Edge{Edge: derived.Edge{SourceID: source, TargetID: target, Type: r.Type, Confidence: r.Confidence, Evidence: r.Evidence}}, true, nil
	}
	var p LinkProjection
	if err = json.Unmarshal(data, &p); err != nil {
		return Edge{}, false, err
	}
	link := p.Link
	endpoints := []*semantics.GraphEndpoint{&link.Source, &link.Target}
	for i := range link.Conditions {
		endpoints = append(endpoints, &link.Conditions[i])
	}
	if len(endpoints) != len(p.Endpoints) {
		return Edge{}, false, errors.New("关系投影无效")
	}
	for i, ep := range endpoints {
		*ep, p.Endpoints[i], valid, err = bindGraphEndpoint(ctx, q, generation, *ep, p.Endpoints[i])
		if err != nil || !valid {
			return Edge{}, false, err
		}
	}
	return Edge{Edge: derived.Edge{SourceID: link.Source.AssetID, TargetID: link.Target.AssetID, Type: link.Type, OwnerID: owner, Grounded: &link}, Meaning: p.Meaning, Endpoints: p.Endpoints}, true, nil

}

// VisitAdjacent preserves forward/reverse/grounded order and loads one edge
// at a time; the caller supplies the existing navigation budget and cursor.
func (s *Store) VisitAdjacent(ctx context.Context, generation, id string, offset int, visit func(Edge, bool) error) error {
	return s.view(ctx, func(q queryer) error {
		rows, err := q.QueryContext(ctx, `SELECT o.asset,o.id,l.grounded,l.data FROM (
SELECT organization,ordinal,grounded,data,0 AS direction FROM graph_links WHERE source=? AND grounded=0
UNION ALL SELECT organization,ordinal,grounded,data,1 FROM graph_links WHERE target=? AND grounded=0
UNION ALL SELECT organization,ordinal,grounded,data,2 FROM graph_links WHERE grounded=1 AND (source=? OR target=?)
) l JOIN organizations o ON o.id=l.organization JOIN organization_publications p ON p.organization=o.id JOIN organization_current c ON c.organization=o.id AND c.generation=? JOIN assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0 ORDER BY l.direction,p.sequence,l.ordinal LIMIT -1 OFFSET ?`, id, id, id, id, generation, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var owner, org string
			var grounded bool
			var data []byte
			if err = rows.Scan(&owner, &org, &grounded, &data); err != nil {
				return err
			}
			edge, valid, e := graphEdge(ctx, q, generation, owner, org, grounded, data)
			if e != nil {
				return e
			}
			if e = visit(edge, valid); e != nil {
				if errors.Is(e, io.EOF) {
					return nil
				}
				return e
			}
		}
		return rows.Err()
	})
}

func (s *Store) OpenOrganization(ctx context.Context, version OrganizationVersion) (io.ReadCloser, error) {
	c, done, err := s.snapshotReader(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := c.QueryContext(ctx, "SELECT bytes FROM organization_chunks WHERE organization=? ORDER BY ordinal", version.ID)
	if err != nil {
		done()
		return nil, err
	}
	return &chunkReader{ctx: ctx, rows: rows, release: done}, nil
}
