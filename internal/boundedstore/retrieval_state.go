package boundedstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

type RetrievalStamp struct {
	Assets, Derived uint64
	Generation      string
}

func (s *Store) RetrievalStamp(ctx context.Context) (RetrievalStamp, error) {
	var out RetrievalStamp
	err := s.view(ctx, func(q queryer) error {
		if e := q.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='asset_epoch'").Scan(&out.Assets); e != nil {
			return e
		}
		e := q.QueryRowContext(ctx, "SELECT generation,epoch FROM derived_state WHERE singleton=1").Scan(&out.Generation, &out.Derived)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		return e
	})
	return out, err
}
func (s *Store) AuthorizeRetrieval(ctx context.Context, before RetrievalStamp) error {
	// Deliberately acquire a fresh reader, not the query's former read snapshot.
	c, done, e := s.reader(ctx)
	if e != nil {
		return e
	}
	defer done()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if e = checkAccess(ctx, c); e != nil {
		return e
	}
	var now RetrievalStamp
	if e = c.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='asset_epoch'").Scan(&now.Assets); e != nil {
		return e
	}
	e = c.QueryRowContext(ctx, "SELECT generation,epoch FROM derived_state WHERE singleton=1").Scan(&now.Generation, &now.Derived)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if before != now {
		return errors.New("检索资料已变化，请重新获取")
	}
	return ctx.Err()
}

// RecordHeader reads normalized, bounded semantic fields. Organization bodies
// stay in their chunked representation and are accessed independently.
func (s *Store) RecordHeader(ctx context.Context, v OrganizationVersion) (derived.Record, error) {
	var out derived.Record
	var data []byte
	e := s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT data FROM organization_headers WHERE organization=?", v.ID).Scan(&data)
	})
	if e != nil {
		return out, e
	}
	doc, e := streamjson.Parse(ctx, s.directory, bytes.NewReader(data), resourcebudget.FromContext(ctx, s.budget), 256*resourcebudget.MiB)
	if e != nil {
		return out, e
	}
	defer doc.Close()
	root := doc.Root()
	for _, f := range []struct {
		key   string
		value any
	}{
		{"asset_id", &out.AssetID}, {"asset_revision", &out.AssetRevision}, {"generated_at", &out.GeneratedAt}, {"provider", &out.Provider}, {"status", &out.Status}, {"error", &out.Error}, {"inputs_known", &out.InputsKnown}, {"input_assets", &out.InputAssets}, {"embedding_space", &out.EmbeddingSpace}, {"semantic_work_reference", &out.SemanticWorkReference}, {"semantic_receipt", &out.SemanticReceipt},
	} {
		if e = nodeDecode(root, f.key, f.value); e != nil {
			return out, e
		}
	}
	analysis, ok, e := root.Field("analysis")
	if e != nil || !ok {
		return out, e
	}
	for _, f := range []struct {
		key   string
		value any
	}{
		{"summary", &out.Analysis.Summary}, {"cues", &out.Analysis.Cues}, {"topics", &out.Analysis.Topics}, {"inferred_contexts", &out.Analysis.Contexts}, {"relations", &out.Analysis.Relations},
	} {
		if e = nodeDecode(analysis, f.key, f.value); e != nil {
			return out, e
		}
	}
	if org, ok, e := analysis.Field("organization"); e != nil {
		return out, e
	} else if ok && org.Kind != 'n' {
		out.Analysis.Organization = &semantics.Organization{}
		if e = nodeDecode(org, "schema", &out.Analysis.Organization.Schema); e != nil {
			return out, e
		}
		out.Analysis.Organization.Snapshot = v.Snapshot
	}
	return out, nil
}
func (s *Store) PendingAssets(ctx context.Context, limit int) ([]string, error) {
	if limit < 1 || limit > 20 {
		return nil, errors.New("语义工作数量必须介于一和二十之间")
	}
	var out []string
	e := s.view(ctx, func(q queryer) error {
		rows, e := q.QueryContext(ctx, "SELECT j.asset FROM semantic_jobs j JOIN assets a ON a.id=j.asset AND a.revision=j.revision AND a.deleted=0 ORDER BY a.updated,j.asset LIMIT ?", limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, e
}

func (s *Store) CopyOrganizationInventory(ctx context.Context, v OrganizationVersion, w io.Writer) error {
	r, e := s.OpenOrganization(ctx, v)
	if e != nil {
		return e
	}
	doc, e := streamjson.Parse(ctx, s.directory, r, resourcebudget.FromContext(ctx, s.budget), 256*resourcebudget.MiB)
	r.Close()
	if e != nil {
		return e
	}
	defer doc.Close()
	a, ok, e := doc.Root().Field("analysis")
	if e != nil || !ok {
		return e
	}
	org, ok, e := a.Field("organization")
	if e != nil {
		return e
	}
	if !ok || org.Kind == 'n' {
		_, e = io.WriteString(w, "null")
		return e
	}
	if _, e = io.WriteString(w, "{"); e != nil {
		return e
	}
	for i, key := range []string{"schema", "snapshot", "units"} {
		if i > 0 {
			io.WriteString(w, ",")
		}
		io.WriteString(w, `"`+key+`":`)
		n, ok, e := org.Field(key)
		if e != nil {
			return e
		}
		if !ok {
			io.WriteString(w, "null")
		} else if e = n.Copy(w); e != nil {
			return e
		}
	}
	_, e = io.WriteString(w, `,"links":null}`)
	return e
}

func (s *Store) stageOrganizationHeader(ctx context.Context, v OrganizationVersion, root, analysis streamjson.Node) error {
	var out bytes.Buffer
	e := root.Object(&out, map[string]func(io.Writer) error{"analysis": func(w io.Writer) error {
		return analysis.Object(w, map[string]func(io.Writer) error{"organization": func(w io.Writer) error {
			if v.Snapshot == "" {
				_, e := io.WriteString(w, "null")
				return e
			}
			return json.NewEncoder(w).Encode(&semantics.Organization{Schema: semantics.OrganizationSchema, Snapshot: v.Snapshot})
		}})
	}})
	if e != nil {
		return e
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO organization_headers VALUES(?,?)", v.ID, out.Bytes())
		return e
	})
}
