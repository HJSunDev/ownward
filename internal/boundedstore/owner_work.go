package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

var _ contract.OwnerWork = (*Store)(nil)

var ErrDraftConflict = errors.New("文稿已变化，请保留当前输入并重新核对")

var errOwnerStreamSnapshot = errors.New("文稿与原件流须在数据库读快照之外打开")

// A content stream outlives an individual read. Inheriting an outer snapshot
// would retain stale authorization and occupy a reader while the user consumes
// it. Reject that context rather than silently reuse or detach its transaction.
func ownerStreamContext(ctx context.Context) error {
	if _, ok := ctx.Value(snapshotKey{}).(snapshot); ok {
		return errOwnerStreamSnapshot
	}
	return nil
}

type ownerActor struct {
	id, owner, system       string
	revision, ownerRevision uint64
}

func authenticateWork(ctx context.Context, q querier, writing bool) (ownerActor, error) {
	var a ownerActor
	digest := contract.AuthenticationDigest(ctx)
	if len(digest) != 64 {
		return a, ErrAccess
	}
	var frozen, retired, stopping bool
	e := q.QueryRowContext(ctx, `SELECT p.id,p.revision,h.system,h.frozen,h.retired,h.stopping,
 json_extract(a.data,'$.information_control.owner_id'),o.revision
 FROM access_header h JOIN access_principals p ON p.credential=?
 JOIN authority_header a ON a.singleton=h.singleton
 JOIN access_principals o ON o.id=json_extract(a.data,'$.information_control.owner_id')
 WHERE h.singleton=1`, digest).Scan(&a.id, &a.revision, &a.system, &frozen, &retired, &stopping, &a.owner, &a.ownerRevision)
	if e != nil || retired || stopping || (writing && frozen) {
		return a, ErrAccess
	}
	return a, nil
}

func requireOwner(ctx context.Context, q querier, writing bool) (ownerActor, error) {
	a, e := authenticateWork(ctx, q, writing)
	if e == nil && a.id != a.owner {
		e = ErrAccess
	}
	return a, e
}

func authorizeDraft(ctx context.Context, q querier, id, grant string, writing bool) error {
	a, e := authenticateWork(ctx, q, writing)
	if e != nil {
		return e
	}
	if a.id == a.owner {
		return nil
	}
	var allowed bool
	e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owner_draft_grants
 WHERE id=? AND draft=? AND principal=? AND principal_revision=? AND owner_revision=? AND expires>?)`,
		grant, id, a.id, a.revision, a.ownerRevision, time.Now().UnixMilli()).Scan(&allowed)
	if e != nil {
		return e
	}
	if !allowed {
		return ErrAccess
	}
	return nil
}

func loadDraft(ctx context.Context, q querier, id string) (contract.Draft, string, error) {
	var d contract.Draft
	var payload, created, updated string
	e := q.QueryRowContext(ctx, `SELECT d.id,d.revision,d.target,d.target_revision,d.kind,d.created,d.updated,d.payload,p.content_bytes,p.digest
 FROM owner_drafts d JOIN payloads p ON p.id=d.payload
 WHERE d.id=? AND (d.target='' OR EXISTS(SELECT 1 FROM live_assets a WHERE a.id=d.target))`, id).
		Scan(&d.ID, &d.Revision, &d.Target.ID, &d.Target.Revision, &d.Kind, &created, &updated, &payload, &d.ContentBytes, &d.ContentSHA256)
	if errors.Is(e, sql.ErrNoRows) {
		return d, "", ErrNotFound
	}
	if e != nil {
		return d, "", e
	}
	if d.CreatedAt, e = time.Parse(time.RFC3339Nano, created); e != nil {
		return d, "", e
	}
	d.UpdatedAt, e = time.Parse(time.RFC3339Nano, updated)
	return d, payload, e
}

func workChanged(ctx context.Context, tx *sql.Tx) error {
	_, e := tx.ExecContext(ctx, "UPDATE store_meta SET value=value+1 WHERE key='owner_work_epoch'")
	return e
}

type contentFunc func(context.Context) (io.ReadCloser, error)

func (f contentFunc) Open(ctx context.Context) (io.ReadCloser, error) { return f(ctx) }

// Reserve a complete mutation workspace before streaming. Independent drafts
// still use short transactions, without holding the asset admission lock.
func (s *Store) ownerWorkspace(ctx context.Context) (context.Context, func(), error) {
	release, e := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 2*resourcebudget.MiB, false)
	if e != nil {
		return ctx, nil, e
	}
	b, _ := resourcebudget.New(2*resourcebudget.MiB, 0)
	return resourcebudget.WithContext(ctx, b), release, nil
}

func (s *Store) CreateDraft(ctx context.Context, in contract.DraftInput) (contract.Draft, error) {
	var out contract.Draft
	if e := s.view(ctx, func(q queryer) error { _, e := requireOwner(ctx, q, true); return e }); e != nil {
		return out, e
	}
	ctx, release, e := s.ownerWorkspace(ctx)
	if e != nil {
		return out, e
	}
	defer release()
	var details contract.ContentSource
	if in.Target.ID != "" {
		if in.Target.Revision == 0 {
			return out, ErrDraftConflict
		}
		m, e := s.ReadAssetMeta(ctx, in.Target.ID, in.Target.Revision)
		if e != nil {
			return out, e
		}
		in.Kind = m.Kind
		if in.Content == nil {
			in.Content = contentFunc(func(c context.Context) (io.ReadCloser, error) { return s.openOwnerAssetPart(c, m.ID, m.Revision, 0) })
		}
		details = contentFunc(func(c context.Context) (io.ReadCloser, error) { return s.openOwnerAssetPart(c, m.ID, m.Revision, 1) })
	} else if in.Target.Revision != 0 {
		return out, ErrDraftConflict
	}
	if in.Kind == "" {
		in.Kind = domain.KindGeneral
	}
	if in.Kind, e = domain.ParseKind(string(in.Kind)); e != nil {
		return out, e
	}
	if in.Content == nil {
		in.Content = StringSource("")
	}
	id, e := newID()
	if e != nil {
		return out, e
	}
	p, e := s.stage(ctx, "owner-draft:"+id, in.Content, details, true)
	if e != nil {
		return out, e
	}
	defer s.Abandon(context.WithoutCancel(ctx), p)
	now := time.Now().UTC()
	out = contract.Draft{ID: id, Revision: 1, Target: in.Target, Kind: in.Kind, CreatedAt: now, UpdatedAt: now, ContentBytes: p.ContentBytes, ContentSHA256: p.ContentSHA256}
	e = s.write(ctx, func(tx *sql.Tx) error {
		if _, e := requireOwner(ctx, tx, true); e != nil {
			return e
		}
		if in.Target.ID != "" {
			var current uint64
			if e := tx.QueryRowContext(ctx, "SELECT revision FROM live_assets WHERE id=?", in.Target.ID).Scan(&current); e != nil {
				return e
			}
			if current != in.Target.Revision {
				return ErrDraftConflict
			}
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO owner_drafts VALUES(?,?,?,?,?,?,?,?)", id, 1, in.Target.ID, in.Target.Revision, in.Kind, stamp(now), stamp(now), p.ID); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "UPDATE payloads SET state='published' WHERE id=?", p.ID); e != nil {
			return e
		}
		return workChanged(ctx, tx)
	})
	return out, e
}

// Reads recheck authorization and the exact draft version for every chunk.
// No database snapshot is held while a browser or agent consumes text.
type ownerPartReader struct {
	s             *Store
	ctx           context.Context
	resolve       func(queryer) (string, error)
	part, ordinal int
	buffer        []byte
	closed        bool
	release       func()
}

func (s *Store) openDraftPart(ctx context.Context, id, grant, payload string, revision uint64, part int) (io.ReadCloser, error) {
	return s.openOwnerPart(ctx, part, func(q queryer) (string, error) {
		if e := authorizeDraft(ctx, q, id, grant, false); e != nil {
			return "", e
		}
		d, p, e := loadDraft(ctx, q, id)
		if e != nil {
			return "", e
		}
		if d.Revision != revision || p != payload {
			return "", ErrDraftConflict
		}
		return p, nil
	})
}

func (s *Store) openOwnerAssetPart(ctx context.Context, id string, revision uint64, part int) (io.ReadCloser, error) {
	return s.openOwnerPart(ctx, part, func(q queryer) (string, error) {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return "", e
		}
		var p string
		var rev uint64
		e := q.QueryRowContext(ctx, "SELECT payload,revision FROM live_assets WHERE id=?", id).Scan(&p, &rev)
		if errors.Is(e, sql.ErrNoRows) {
			e = ErrNotFound
		}
		if e != nil {
			return "", e
		}
		if rev != revision {
			return "", ErrDraftConflict
		}
		return p, nil
	})
}

func (s *Store) openOwnerPart(ctx context.Context, part int, resolve func(queryer) (string, error)) (io.ReadCloser, error) {
	if e := ownerStreamContext(ctx); e != nil {
		return nil, e
	}
	release, e := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 2*ChunkBytes, false)
	if e != nil {
		return nil, e
	}
	return &ownerPartReader{s: s, ctx: ctx, resolve: resolve, part: part, release: release}, nil
}

func (r *ownerPartReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	e := r.s.view(r.ctx, func(q queryer) error {
		payload, e := r.resolve(q)
		if e != nil {
			return e
		}
		if len(r.buffer) == 0 {
			e = q.QueryRowContext(r.ctx, "SELECT bytes FROM content_chunks WHERE payload=? AND part=? AND ordinal=?", payload, r.part, r.ordinal).Scan(&r.buffer)
			if errors.Is(e, sql.ErrNoRows) {
				return io.EOF
			}
			if e != nil {
				return e
			}
			r.ordinal++
		}
		return nil
	})
	if e != nil {
		r.Close()
		return 0, e
	}
	n := copy(p, r.buffer)
	r.buffer = r.buffer[n:]
	return n, nil
}
func (r *ownerPartReader) Close() error {
	r.closed = true
	r.buffer = nil
	if r.release != nil {
		r.release()
		r.release = nil
	}
	return nil
}

func (s *Store) ReadDraft(ctx context.Context, id, grant string) (contract.Draft, io.ReadCloser, error) {
	var d contract.Draft
	if e := ownerStreamContext(ctx); e != nil {
		return d, nil, e
	}
	var payload string
	e := s.view(ctx, func(q queryer) error {
		if e := authorizeDraft(ctx, q, id, grant, false); e != nil {
			return e
		}
		var e error
		d, payload, e = loadDraft(ctx, q, id)
		return e
	})
	if e != nil {
		return d, nil, e
	}
	r, e := s.openDraftPart(ctx, id, grant, payload, d.Revision, 0)
	return d, r, e
}

type joinedContent struct{ a, b contract.ContentSource }
type joinedReader struct {
	io.Reader
	a, b io.Closer
}

func (r *joinedReader) Close() error { return errors.Join(r.a.Close(), r.b.Close()) }
func (v joinedContent) Open(ctx context.Context) (io.ReadCloser, error) {
	a, e := v.a.Open(ctx)
	if e != nil {
		return nil, e
	}
	b, e := v.b.Open(ctx)
	if e != nil {
		a.Close()
		return nil, e
	}
	return &joinedReader{io.MultiReader(a, b), a, b}, nil
}

func (s *Store) WriteDraft(ctx context.Context, in contract.DraftWrite) (contract.Draft, error) {
	var d contract.Draft
	var oldPayload string
	if in.Content == nil {
		return d, errors.New("缺少文稿内容")
	}
	e := s.view(ctx, func(q queryer) error {
		if e := authorizeDraft(ctx, q, in.ID, in.GrantID, true); e != nil {
			return e
		}
		var e error
		d, oldPayload, e = loadDraft(ctx, q, in.ID)
		return e
	})
	if e != nil {
		return d, e
	}
	if d.Revision != in.ExpectedRevision {
		return d, ErrDraftConflict
	}
	if e = s.awaitReclaim(ctx); e != nil {
		return d, e
	}
	ctx, release, e := s.ownerWorkspace(ctx)
	if e != nil {
		return d, e
	}
	defer release()
	part := func(n int) contract.ContentSource {
		return contentFunc(func(c context.Context) (io.ReadCloser, error) {
			return s.openDraftPart(c, d.ID, in.GrantID, oldPayload, d.Revision, n)
		})
	}
	content := in.Content
	if in.Append {
		content = joinedContent{part(0), content}
	}
	p, e := s.stage(ctx, "owner-draft:"+in.ID, content, part(1), true)
	if e != nil {
		return d, e
	}
	defer s.Abandon(context.WithoutCancel(ctx), p)
	now := time.Now().UTC()
	e = s.write(ctx, func(tx *sql.Tx) error {
		if e := authorizeDraft(ctx, tx, in.ID, in.GrantID, true); e != nil {
			return e
		}
		current, payload, e := loadDraft(ctx, tx, in.ID)
		if e != nil {
			return e
		}
		if current.Revision != in.ExpectedRevision || payload != oldPayload {
			return ErrDraftConflict
		}
		if _, e = tx.ExecContext(ctx, "UPDATE owner_drafts SET revision=revision+1,updated=?,payload=? WHERE id=?", stamp(now), p.ID, in.ID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE payloads SET state='published' WHERE id=?", p.ID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO reclaim_jobs VALUES(?,'draft_replaced')", oldPayload); e != nil {
			return e
		}
		return workChanged(ctx, tx)
	})
	if e == nil {
		d.Revision++
		d.UpdatedAt = now
		d.ContentBytes = p.ContentBytes
		d.ContentSHA256 = p.ContentSHA256
	}
	return d, e
}

func (s *Store) ListDrafts(ctx context.Context, after string, limit int) (contract.DraftPage, error) {
	var out contract.DraftPage
	if limit < 1 || limit > 100 {
		return out, errors.New("文稿分页上限为 100")
	}
	e := s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return e
		}
		rows, e := q.QueryContext(ctx, `SELECT d.id FROM owner_drafts d WHERE id>? AND (target='' OR EXISTS(SELECT 1 FROM live_assets a WHERE a.id=d.target)) ORDER BY id LIMIT ?`, after, limit+1)
		if e != nil {
			return e
		}
		var ids []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(ids) > limit {
			ids = ids[:limit]
			out.Next = ids[len(ids)-1]
		}
		for _, id := range ids {
			d, _, e := loadDraft(ctx, q, id)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, d)
		}
		return nil
	})
	return out, e
}

func (s *Store) GrantDraft(ctx context.Context, id, principal string, ttl time.Duration) (contract.DraftGrant, error) {
	var out contract.DraftGrant
	if ttl < time.Second || ttl > 24*time.Hour {
		return out, errors.New("工作授权有效期须为 1 秒至 24 小时")
	}
	key, e := newID()
	if e != nil {
		return out, e
	}
	out = contract.DraftGrant{ID: key, DraftID: id, Principal: principal, ExpiresAt: time.Now().UTC().Add(ttl)}
	e = s.write(ctx, func(tx *sql.Tx) error {
		a, e := requireOwner(ctx, tx, true)
		if e != nil {
			return e
		}
		if _, _, e = loadDraft(ctx, tx, id); e != nil {
			return e
		}
		var rev uint64
		if e = tx.QueryRowContext(ctx, "SELECT revision FROM access_principals WHERE id=? AND permissions<>0", principal).Scan(&rev); e != nil {
			return ErrAccess
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM owner_draft_grants WHERE draft=? AND (principal=? OR expires<=?)", id, principal, time.Now().UnixMilli()); e != nil {
			return e
		}
		var count int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM owner_draft_grants WHERE draft=?", id).Scan(&count); e != nil {
			return e
		}
		if count >= 64 {
			return errors.New("当前文稿授权过多")
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO owner_draft_grants VALUES(?,?,?,?,?,?)", key, id, principal, rev, a.ownerRevision, out.ExpiresAt.UnixMilli()); e != nil {
			return e
		}
		return workChanged(ctx, tx)
	})
	return out, e
}

func (s *Store) RevokeDraftGrant(ctx context.Context, id string) error {
	return s.write(workContext(ctx, controlWork), func(tx *sql.Tx) error {
		if _, e := requireOwner(ctx, tx, false); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "DELETE FROM owner_draft_grants WHERE id=?", id); e != nil {
			return e
		}
		return workChanged(ctx, tx)
	})
}

func deleteDraft(ctx context.Context, tx *sql.Tx, id, payload string) error {
	if _, e := tx.ExecContext(ctx, "DELETE FROM owner_drafts WHERE id=?", id); e != nil {
		return e
	}
	// A protected snapshot must not keep a discarded or published private draft.
	if _, e := tx.ExecContext(ctx, "UPDATE controlled_copies SET state='building'"); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO reclaim_jobs VALUES(?,'draft_removed')", payload); e != nil {
		return e
	}
	return workChanged(ctx, tx)
}

func (s *Store) DiscardDraft(ctx context.Context, id string, expected uint64) error {
	return s.write(workContext(ctx, controlWork), func(tx *sql.Tx) error {
		if _, e := requireOwner(ctx, tx, true); e != nil {
			return e
		}
		d, p, e := loadDraft(ctx, tx, id)
		if errors.Is(e, ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		if d.Revision != expected {
			return ErrDraftConflict
		}
		return deleteDraft(ctx, tx, id, p)
	})
}

func (s *Store) OwnerWorkRevision(ctx context.Context) (uint64, error) {
	var rev uint64
	e := s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return e
		}
		return q.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='owner_work_epoch'").Scan(&rev)
	})
	return rev, e
}
