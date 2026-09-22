package boundedstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/retrieval"
)

// OwnerCheckpoint is a small read of authoritative epochs, never a collection
// of assets. Authentication and these epochs are read in the same snapshot.
type OwnerCheckpoint struct {
	System                                                                    string
	Owner                                                                     string
	OwnerRevision, Assets, Derived, Work, Events, Control, Deletion, Receipts uint64
	Generation                                                                string
	Attention                                                                 bool
	Rebuilding                                                                bool
	VisibleUntil                                                              int64
}

func (s *Store) OwnerCheckpoint(ctx context.Context) (out OwnerCheckpoint, err error) {
	err = s.view(ctx, func(q queryer) error {
		a, e := requireOwner(ctx, q, false)
		if e != nil {
			return e
		}
		out.System, out.Owner, out.OwnerRevision = a.system, a.owner, a.ownerRevision
		if e = q.QueryRowContext(ctx, `SELECT
 (SELECT value FROM store_meta WHERE key='asset_epoch'),
 coalesce((SELECT epoch FROM derived_state WHERE singleton=1),0),
 (SELECT value FROM store_meta WHERE key='owner_work_epoch'),
 (SELECT value FROM store_meta WHERE key='operation_generation'),
 max(coalesce((SELECT max(sequence) FROM owner_events),0),(SELECT value FROM store_meta WHERE key='owner_event_floor')),
 revision,deletion_epoch,coalesce((SELECT generation FROM derived_state WHERE singleton=1),''),
 frozen OR stopping OR EXISTS(SELECT 1 FROM authority_items WHERE kind='operations' AND state IN ('stopping','cleaning')),
 EXISTS(SELECT 1 FROM generations WHERE state='building')
 FROM access_header WHERE singleton=1`).Scan(&out.Assets, &out.Derived, &out.Work, &out.Receipts, &out.Events, &out.Control, &out.Deletion, &out.Generation, &out.Attention, &out.Rebuilding); e != nil {
			return e
		}
		// Time-driven removals must invalidate polling even without any write.
		// Events/history use their age indexes; enrollment is capped at 256.
		now := time.Now().UnixMilli()
		retention := OwnerHistoryRetention.Milliseconds()
		return q.QueryRowContext(ctx, `SELECT coalesce(min(deadline),0) FROM (
 SELECT min(at)+?+1 AS deadline FROM owner_events WHERE at>=?
 UNION ALL SELECT min(updated)+?+1 FROM owner_operation_times WHERE state='completed' AND updated>=?
 UNION ALL SELECT min(updated)+?+1 FROM owner_operation_times WHERE state='declined' AND updated>=?
 UNION ALL SELECT min(updated)+?+1 FROM owner_operation_times WHERE state='superseded' AND updated>=?
 UNION ALL SELECT min(expires) FROM owner_draft_grants WHERE expires>?
 UNION ALL SELECT min(CAST((julianday(json_extract(e.value,'$.expires'))-2440587.5)*86400000 AS INTEGER))
 FROM authority_header h,json_each(h.data,'$.access.enrollments') e
 WHERE json_extract(e.value,'$.status') IN ('pending','approved') AND coalesce(json_extract(e.value,'$.claimed'),0)=0
 AND (julianday(json_extract(e.value,'$.expires'))-2440587.5)*86400000>?
 )`, retention, now-retention, retention, now-retention, retention, now-retention, retention, now-retention, now, now).Scan(&out.VisibleUntil)
	})
	s.maintenanceMu.Lock()
	out.Attention = out.Attention || s.maintenanceErr != nil
	s.maintenanceMu.Unlock()
	return
}

type OwnerAssetRow struct {
	Meta     contract.AssetMeta
	State    string
	Original bool
}

const ownerOrganizationState = `CASE WHEN o.status='ready' AND NOT EXISTS(
 SELECT 1 FROM dependencies dep LEFT JOIN live_assets input ON input.id=dep.asset AND input.revision=dep.revision
 WHERE dep.organization=o.id AND input.id IS NULL) THEN 'ready' ELSE 'pending' END`

func (s *Store) OwnerStopped(ctx context.Context, after string, limit int) (out []OwnerAssetRow, next string, err error) {
	if limit < 1 || limit > contract.OwnerPageLimit {
		return nil, "", errors.New("资料分页范围无效")
	}
	err = s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return e
		}
		rows, e := q.QueryContext(ctx, `SELECT t.asset,t.revision FROM forget_targets t JOIN forget_operations f ON f.id=t.operation WHERE f.state='cleaning' AND t.asset>? ORDER BY t.asset LIMIT ?`, after, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v OwnerAssetRow
			v.State = "stopped"
			if e = rows.Scan(&v.Meta.ID, &v.Meta.Revision); e != nil {
				return e
			}
			if len(out) == limit {
				next = out[len(out)-1].Meta.ID
				break
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return
}

// OwnerAssets performs deterministic lexical lookup, not semantic retrieval.
// It uses the existing token index. Rows are ordered by stable identity so any
// number of matches can be paged; no top-k materialization or model is needed.
func (s *Store) OwnerAssets(ctx context.Context, after, query, state string, kind domain.InformationKind, limit int) (out []OwnerAssetRow, next string, err error) {
	if limit < 1 || limit > contract.OwnerPageLimit || len(query) > 1024 {
		return nil, "", errors.New("查找范围无效")
	}
	if state != "" && state != "ready" && state != "pending" {
		return nil, "", errors.New("资料筛选状态无效")
	}
	if kind != "" {
		if _, e := domain.ParseKind(string(kind)); e != nil {
			return nil, "", e
		}
	}
	err = s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return e
		}
		conditions := []string{"a.id>?", "a.deleted=0"}
		args := []any{after}
		if query != "" {
			_, e := q.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS owner_find_terms(term BLOB PRIMARY KEY) WITHOUT ROWID; DELETE FROM owner_find_terms;`)
			if e != nil {
				return e
			}
			e = retrieval.VisitQueryTerms(strings.NewReader(query), func(t retrieval.TermPosition) error {
				_, e := q.ExecContext(ctx, "INSERT OR IGNORE INTO owner_find_terms VALUES(?)", t.Digest[:])
				return e
			})
			if e != nil {
				return e
			}
			conditions = append(conditions, `EXISTS(SELECT 1 FROM lexical_payload_ids i JOIN postings p ON p.payload=i.id JOIN owner_find_terms t ON t.term=p.term WHERE i.payload=a.payload)`)
		}
		if state != "" {
			conditions = append(conditions, "("+ownerOrganizationState+")=?")
			args = append(args, state)
		}
		if kind != "" {
			conditions = append(conditions, "a.kind=?")
			args = append(args, kind)
		}
		args = append(args, limit+1)
		rows, e := q.QueryContext(ctx, `SELECT `+assetColumns+`,`+ownerOrganizationState+`,
 EXISTS(SELECT 1 FROM asset_originals x WHERE x.asset=a.id)
 FROM live_assets a JOIN payloads p ON p.id=a.payload
 LEFT JOIN derived_state ds ON ds.singleton=1
 LEFT JOIN organization_current oc ON oc.generation=ds.generation AND oc.asset=a.id
 LEFT JOIN organizations o ON o.id=oc.organization AND o.revision=a.revision
 WHERE `+strings.Join(conditions, " AND ")+` ORDER BY a.id LIMIT ?`, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v OwnerAssetRow
			var created, updated string
			if e = rows.Scan(&v.Meta.ID, &v.Meta.Revision, &created, &updated, &v.Meta.Kind, &v.Meta.ContentBytes, &v.Meta.ContentSHA256, &v.State, &v.Original); e != nil {
				return e
			}
			if len(out) == limit {
				next = out[len(out)-1].Meta.ID
				break
			}
			if v.Meta.CreatedAt, e = time.Parse(time.RFC3339Nano, created); e != nil {
				return e
			}
			if v.Meta.UpdatedAt, e = time.Parse(time.RFC3339Nano, updated); e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return
}

// OwnerPrincipals excludes credential digests, even from internal projections.
type OwnerPrincipalRow struct {
	contract.Principal
	Order uint64
}

func (s *Store) OwnerPrincipals(ctx context.Context, after string, limit int) (out []OwnerPrincipalRow, next string, err error) {
	if limit < 1 || limit > contract.OwnerPageLimit {
		return nil, "", errors.New("接入分页范围无效")
	}
	err = s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return e
		}
		rows, e := q.QueryContext(ctx, `SELECT id,json_extract(data,'$.name'),json_extract(data,'$.revision'),json_extract(data,'$.permissions'),sequence FROM authority_items WHERE kind='principals' AND id>? ORDER BY id LIMIT ?`, after, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var p OwnerPrincipalRow
			var permissions sql.NullString
			if e = rows.Scan(&p.ID, &p.Name, &p.Revision, &permissions, &p.Order); e != nil {
				return e
			}
			if len(out) == limit {
				next = out[len(out)-1].ID
				break
			}
			if permissions.Valid {
				if e = json.Unmarshal([]byte(permissions.String), &p.Permissions); e != nil {
					return e
				}
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return
}

func (s *Store) OwnerPrincipal(ctx context.Context, id string) (out OwnerPrincipalRow, err error) {
	err = s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return e
		}
		out.ID = id
		var permissions sql.NullString
		if e := q.QueryRowContext(ctx, `SELECT json_extract(data,'$.name'),json_extract(data,'$.revision'),sequence,json_extract(data,'$.permissions') FROM authority_items WHERE kind='principals' AND id=?`, id).Scan(&out.Name, &out.Revision, &out.Order, &permissions); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return ErrNotFound
			}
			return e
		}
		if permissions.Valid {
			return json.Unmarshal([]byte(permissions.String), &out.Permissions)
		}
		return nil
	})
	return
}

type OwnerGrantRow struct {
	contract.DraftGrant
	DraftRevision uint64
}

func (s *Store) OwnerGrants(ctx context.Context, after string, limit int) (out []OwnerGrantRow, next string, err error) {
	if limit < 1 || limit > contract.OwnerPageLimit {
		return nil, "", errors.New("授权分页范围无效")
	}
	err = s.view(ctx, func(q queryer) error {
		a, e := requireOwner(ctx, q, false)
		if e != nil {
			return e
		}
		rows, e := q.QueryContext(ctx, `SELECT g.id,g.draft,d.revision,g.principal,g.expires
 FROM owner_draft_grants g JOIN owner_drafts d ON d.id=g.draft
 JOIN access_principals p ON p.id=g.principal AND p.revision=g.principal_revision AND length(p.credential)=64
 WHERE g.id>? AND g.owner_revision=? AND g.expires>?
 AND (d.target='' OR EXISTS(SELECT 1 FROM live_assets a WHERE a.id=d.target))
 ORDER BY g.id LIMIT ?`, after, a.ownerRevision, time.Now().UnixMilli(), limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v OwnerGrantRow
			var expires int64
			if e = rows.Scan(&v.ID, &v.DraftID, &v.DraftRevision, &v.Principal, &expires); e != nil {
				return e
			}
			if len(out) == limit {
				next = out[len(out)-1].ID
				break
			}
			v.ExpiresAt = time.UnixMilli(expires).UTC()
			out = append(out, v)
		}
		return rows.Err()
	})
	return
}

type OwnerManagementFact struct {
	Kind, Subject        string
	Targets, Unavailable int
}

// Resolve only safe, bounded facts from the immutable request reference. The
// event retains its historical status; target availability is current.
func (s *Store) OwnerManagementFact(ctx context.Context, id string) (out OwnerManagementFact, err error) {
	err = s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return e
		}
		e := q.QueryRowContext(ctx, `SELECT json_extract(data,'$.request.operation'),coalesce(json_extract(data,'$.request.subject_id'),''),
 coalesce(json_array_length(data,'$.request.targets'),0),
 (SELECT count(*) FROM json_each(a.data,'$.request.targets') t WHERE NOT EXISTS(SELECT 1 FROM live_assets l WHERE l.id=json_extract(t.value,'$.id')))
 FROM authority_items a WHERE kind='operations' AND id=?`, id).Scan(&out.Kind, &out.Subject, &out.Targets, &out.Unavailable)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		return e
	})
	return
}

// Text pages acquire only short read transactions. No database reader is held
// while the HTTP client consumes a page. Every page rechecks current authority.
func (s *Store) OwnerText(ctx context.Context, id string, revision uint64, part string, offset int64, grant string) (out contract.OwnerText, err error) {
	if offset < 0 {
		return out, errors.New("正文位置无效")
	}
	err = s.view(ctx, func(q queryer) error {
		var payload string
		n := 0
		switch part {
		case "draft_content":
			if e := authorizeDraft(ctx, q, id, grant, false); e != nil {
				return e
			}
			d, p, e := loadDraft(ctx, q, id)
			if e != nil {
				return e
			}
			if d.Revision != revision {
				return contract.ErrOwnerRefresh
			}
			payload = p
		case "content", "details", "original", "original_details":
			if _, e := requireOwner(ctx, q, false); e != nil {
				return e
			}
			var current uint64
			if e := q.QueryRowContext(ctx, "SELECT payload,revision FROM live_assets WHERE id=? AND deleted=0", id).Scan(&payload, &current); e != nil {
				if errors.Is(e, sql.ErrNoRows) {
					return ErrNotFound
				}
				return e
			}
			if current != revision {
				return contract.ErrOwnerRefresh
			}
			if strings.HasPrefix(part, "original") {
				if e := q.QueryRowContext(ctx, "SELECT payload FROM asset_originals WHERE asset=?", id).Scan(&payload); e != nil {
					if errors.Is(e, sql.ErrNoRows) {
						return ErrNotFound
					}
					return e
				}
			}
			if strings.Contains(part, "details") {
				n = 1
			}
		default:
			return errors.New("正文类型无效")
		}
		return readOwnerText(ctx, q, "content_chunks", "payload", payload, n, offset, 0, 0, &out)
	})
	return
}

func readOwnerText(ctx context.Context, q queryer, table, column, id string, part int, offset, start, end int64, out *contract.OwnerText) error {
	// table and column are internal constants, never request parameters.
	from := start + offset
	stop := from + contract.OwnerTextBytes + utf8.UTFMax
	if end > 0 {
		stop = min(stop, end)
		if from > end {
			return errors.New("正文位置无效")
		}
	}
	condition := ""
	args := []any{id}
	if table == "content_chunks" {
		condition = " AND part=?"
		args = append(args, part)
	}
	args = append(args, from/ChunkBytes, (stop-1)/ChunkBytes)
	rows, e := q.QueryContext(ctx, "SELECT ordinal,bytes FROM "+table+" WHERE "+column+"=?"+condition+" AND ordinal>=? AND ordinal<=? ORDER BY ordinal", args...)
	if e != nil {
		return e
	}
	defer rows.Close()
	buf := make([]byte, 0, contract.OwnerTextBytes+utf8.UTFMax)
	for rows.Next() {
		var ordinal int64
		var data []byte
		if e = rows.Scan(&ordinal, &data); e != nil {
			return e
		}
		lo := max(0, from-ordinal*ChunkBytes)
		hi := min(int64(len(data)), stop-ordinal*ChunkBytes)
		if lo < hi {
			buf = append(buf, data[lo:hi]...)
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	n := min(len(buf), contract.OwnerTextBytes)
	for n > 0 && !utf8.Valid(buf[:n]) {
		n--
		if len(buf)-n > utf8.UTFMax+3 {
			return errors.New("正文位置必须在字符边界")
		}
	}
	if n == 0 && len(buf) > 0 {
		return errors.New("正文位置必须在字符边界")
	}
	out.Text = string(buf[:n])
	out.NextOffset = offset + int64(n)
	out.More = n < len(buf)
	return nil
}

func (s *Store) OwnerMeaning(ctx context.Context, ref GraphText, offset int64) (out contract.OwnerText, err error) {
	if offset < 0 || ref.End < ref.Start {
		return out, errors.New("关系位置无效")
	}
	err = s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return e
		}
		return readOwnerText(ctx, q, "organization_chunks", "organization", ref.Organization, 0, offset, ref.Start, ref.End, &out)
	})
	return
}

// DraftMetadata validates a grant without opening or materializing its body.
func (s *Store) DraftMetadata(ctx context.Context, id, grant string) (out contract.Draft, err error) {
	err = s.view(ctx, func(q queryer) error {
		if e := authorizeDraft(ctx, q, id, grant, false); e != nil {
			return e
		}
		var e error
		out, _, e = loadDraft(ctx, q, id)
		return e
	})
	return
}
