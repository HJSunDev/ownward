package boundedstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
)

const collaborationTTL = time.Hour

// A primary connection uses its existing authority. The public invitation only
// locates the draft; it does not manufacture another temporary permission.
func (s *Store) ResolveDraftInvitation(ctx context.Context, id string) (draft string, err error) {
	err = s.view(ctx, func(q queryer) error {
		a, e := requireDraftAuthor(ctx, q, false)
		if e != nil {
			return e
		}
		var revision uint64
		var expires int64
		e = q.QueryRowContext(ctx, "SELECT draft,owner_revision,expires FROM owner_draft_invitations WHERE id=?", id).Scan(&draft, &revision, &expires)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrAccess
		}
		if e != nil {
			return e
		}
		if revision != a.ownerRevision || expires <= time.Now().UnixMilli() {
			return ErrAccess
		}
		_, _, e = loadDraft(ctx, q, draft)
		return e
	})
	return
}

// Only the public identity of the actual requester is exposed to a manager.
func (s *Store) DraftRequestSubject(ctx context.Context, request string) (out OwnerPrincipalRow, err error) {
	err = s.view(ctx, func(q queryer) error {
		a, e := requireDraftManager(ctx, q, false)
		if e != nil {
			return e
		}
		v, e := collaboration(ctx, q, request, a)
		if e != nil {
			return e
		}
		out.ID = v.Principal
		return q.QueryRowContext(ctx, `SELECT json_extract(data,'$.name'),json_extract(data,'$.revision'),sequence FROM authority_items WHERE kind='principals' AND id=?`, v.Principal).Scan(&out.Name, &out.Revision, &out.Order)
	})
	return
}

func (s *Store) InviteDraft(ctx context.Context, draft string, revision uint64) (out contract.DraftInvitation, err error) {
	err = s.write(ctx, func(tx *sql.Tx) error {
		a, e := requireDraftManager(ctx, tx, true)
		if e != nil {
			return e
		}
		d, _, e := loadDraft(ctx, tx, draft)
		if e != nil {
			return e
		}
		if d.Revision != revision {
			return contract.ErrOwnerRefresh
		}
		now := time.Now()
		// Retain terminal requests until every possible grant has also expired.
		if _, e = tx.ExecContext(ctx, "DELETE FROM owner_draft_invitations WHERE expires<=?", now.Add(-collaborationTTL).UnixMilli()); e != nil {
			return e
		}
		var expires int64
		e = tx.QueryRowContext(ctx, `SELECT i.id,i.expires FROM owner_draft_invitations i WHERE draft=? AND owner_revision=? AND expires>?
 AND NOT EXISTS(SELECT 1 FROM owner_draft_requests r WHERE r.invitation=i.id) ORDER BY expires DESC LIMIT 1`, draft, a.ownerRevision, now.UnixMilli()).Scan(&out.ID, &expires)
		if e == nil {
			out.DraftID = draft
			out.ExpiresAt = time.UnixMilli(expires).UTC()
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var count int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM owner_draft_invitations").Scan(&count); e != nil {
			return e
		}
		if count >= 256 {
			return errors.New("协作请求过多，请稍后再试")
		}
		out.ID, e = newID()
		if e != nil {
			return e
		}
		out.DraftID = draft
		out.ExpiresAt = now.UTC().Add(collaborationTTL)
		if _, e = tx.ExecContext(ctx, "INSERT INTO owner_draft_invitations VALUES(?,?,?,?)", out.ID, draft, a.ownerRevision, out.ExpiresAt.UnixMilli()); e != nil {
			return e
		}
		return workChanged(ctx, tx)
	})
	return
}

func requestMarker(id, principal string) string {
	h := sha256.Sum256([]byte(id + ":" + principal))
	return fmt.Sprintf("%X-%X", h[:3], h[3:6])
}

func collaboration(ctx context.Context, q querier, id string, a ownerActor) (v contract.DraftCollaboration, err error) {
	var principalRevision, ownerRevision uint64
	var expires int64
	err = q.QueryRowContext(ctx, `SELECT r.id,r.invitation,i.draft,r.principal,r.principal_revision,r.owner_revision,r.revision,r.state,r.grant_id,r.expires
 FROM owner_draft_requests r JOIN owner_draft_invitations i ON i.id=r.invitation WHERE r.id=?`, id).Scan(&v.ID, &v.Invitation, &v.DraftID, &v.Principal, &principalRevision, &ownerRevision, &v.Revision, &v.State, &v.Grant, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return
	}
	if a.id != a.owner && a.id != v.Principal && a.permissions&4 == 0 {
		return contract.DraftCollaboration{}, ErrAccess
	}
	v.ExpiresAt = time.UnixMilli(expires).UTC()
	v.Verification = requestMarker(v.ID, v.Principal)
	if _, _, e := loadDraft(ctx, q, v.DraftID); e != nil {
		if !errors.Is(e, ErrNotFound) {
			return v, e
		}
		v.State = "ended"
	}
	var current uint64
	if e := q.QueryRowContext(ctx, "SELECT revision FROM access_principals WHERE id=? AND length(credential)=64", v.Principal).Scan(&current); e != nil && !errors.Is(e, sql.ErrNoRows) {
		return v, e
	}
	if ownerRevision != a.ownerRevision || current != principalRevision {
		v.State = "ended"
	}
	if !time.Now().Before(v.ExpiresAt) {
		v.State = "expired"
	}
	if v.State == "approved" {
		var valid bool
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM owner_draft_grants g WHERE id=? AND draft=? AND principal=? AND principal_revision=? AND owner_revision=? AND expires>? AND `+validDraftDelegation+`)`, v.Grant, v.DraftID, v.Principal, principalRevision, ownerRevision, time.Now().UnixMilli()).Scan(&valid)
		if err != nil {
			return
		}
		if !valid {
			v.State = "ended"
		}
	}
	if v.State != "approved" {
		v.Grant = ""
	}
	return
}

func (s *Store) RequestDraft(ctx context.Context, invitation string) (out contract.DraftCollaboration, err error) {
	err = s.write(ctx, func(tx *sql.Tx) error {
		a, e := authenticateWork(ctx, tx, true)
		if e != nil {
			return e
		}
		if a.id == a.owner {
			return errors.New("请使用智能体自身连接申请协作")
		}
		var id string
		e = tx.QueryRowContext(ctx, "SELECT id FROM owner_draft_requests WHERE invitation=? AND principal=?", invitation, a.id).Scan(&id)
		if e == nil {
			out, e = collaboration(ctx, tx, id, a)
			return e
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var draft string
		var ownerRevision uint64
		var expires int64
		if e = tx.QueryRowContext(ctx, "SELECT draft,owner_revision,expires FROM owner_draft_invitations WHERE id=?", invitation).Scan(&draft, &ownerRevision, &expires); e != nil {
			return ErrAccess
		}
		if ownerRevision != a.ownerRevision || expires <= time.Now().UnixMilli() {
			return errors.New("协作请求已失效，请从文稿重新发起")
		}
		if _, _, e = loadDraft(ctx, tx, draft); e != nil {
			return e
		}
		var count int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM owner_draft_requests WHERE invitation=?", invitation).Scan(&count); e != nil {
			return e
		}
		if count >= 16 {
			return errors.New("此文稿的协作申请过多")
		}
		id, e = newID()
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO owner_draft_requests VALUES(?,?,?,?,?,1,'awaiting_approval','',?)", id, invitation, a.id, a.revision, a.ownerRevision, expires); e != nil {
			return e
		}
		if e = workChanged(ctx, tx); e != nil {
			return e
		}
		out, e = collaboration(ctx, tx, id, a)
		return e
	})
	return
}

func (s *Store) DraftCollaboration(ctx context.Context, id string) (out contract.DraftCollaboration, err error) {
	err = s.view(ctx, func(q queryer) error {
		a, e := authenticateWork(ctx, q, false)
		if e != nil {
			return e
		}
		out, e = collaboration(ctx, q, id, a)
		return e
	})
	return
}

func (s *Store) DraftCollaborations(ctx context.Context, draft, after string, limit int, state, invitation string) (out []contract.DraftCollaboration, next string, err error) {
	if limit < 1 || limit > 100 || (state != "" && state != "active" && state != "awaiting_approval") {
		return nil, "", errors.New("协作列表分页无效")
	}
	err = s.view(ctx, func(q queryer) error {
		a, e := requireDraftManager(ctx, q, false)
		if e != nil {
			return e
		}
		// Filter the effective state before LIMIT: terminal or revoked requests
		// must not consume a pending/active page or manufacture its continuation.
		condition := ""
		args := []any{after, draft, draft, invitation, invitation}
		if state != "" {
			condition = ` AND r.owner_revision=? AND r.expires>?
 AND EXISTS(SELECT 1 FROM access_principals p WHERE p.id=r.principal AND p.revision=r.principal_revision AND length(p.credential)=64)
 AND EXISTS(SELECT 1 FROM owner_drafts d WHERE d.id=i.draft AND (d.target='' OR EXISTS(SELECT 1 FROM live_assets a WHERE a.id=d.target)))`
			args = append(args, a.ownerRevision, time.Now().UnixMilli())
			if state == "awaiting_approval" {
				condition += " AND r.state='awaiting_approval'"
			} else {
				condition += ` AND (r.state='awaiting_approval' OR (r.state='approved' AND EXISTS(
 SELECT 1 FROM owner_draft_grants g WHERE g.id=r.grant_id AND g.draft=i.draft AND g.principal=r.principal
 AND g.principal_revision=r.principal_revision AND g.owner_revision=r.owner_revision AND g.expires>? AND ` + validDraftDelegation + `)))`
				args = append(args, time.Now().UnixMilli())
			}
		}
		args = append(args, limit+1)
		rows, e := q.QueryContext(ctx, `SELECT r.id FROM owner_draft_requests r JOIN owner_draft_invitations i ON i.id=r.invitation
 WHERE r.id>? AND (?='' OR i.draft=?) AND (?='' OR i.id=?)`+condition+` ORDER BY r.id LIMIT ?`, args...)
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
			next = ids[len(ids)-1]
		}
		for _, id := range ids {
			v, e := collaboration(ctx, q, id, a)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return nil
	})
	return
}

func (s *Store) DecideDraftCollaboration(ctx context.Context, id string, revision uint64, accept bool) (out contract.DraftCollaboration, err error) {
	err = s.write(ctx, func(tx *sql.Tx) error {
		a, e := requireDraftManager(ctx, tx, true)
		if e != nil {
			return e
		}
		v, e := collaboration(ctx, tx, id, a)
		if e != nil {
			return e
		}
		if v.State != "awaiting_approval" {
			out = v
			return nil
		}
		if v.Revision != revision {
			return contract.ErrOwnerRefresh
		}
		state, grant, expires := "declined", "", v.ExpiresAt
		if accept {
			var rev uint64
			if e = tx.QueryRowContext(ctx, "SELECT revision FROM access_principals WHERE id=?", v.Principal).Scan(&rev); e != nil {
				return e
			}
			var count int
			if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM owner_draft_grants WHERE draft=? AND expires>?", v.DraftID, time.Now().UnixMilli()).Scan(&count); e != nil {
				return e
			}
			if count >= 64 {
				return errors.New("当前文稿授权过多")
			}
			grant, e = newID()
			if e != nil {
				return e
			}
			state = "approved"
			expires = time.Now().UTC().Add(collaborationTTL)
			if _, e = tx.ExecContext(ctx, "INSERT INTO owner_draft_grants VALUES(?,?,?,?,?,?)", grant, v.DraftID, v.Principal, rev, a.ownerRevision, expires.UnixMilli()); e != nil {
				return e
			}
		}
		if accept {
			if _, e = tx.ExecContext(ctx, "INSERT INTO owner_draft_delegations VALUES(?,?,?)", grant, a.id, a.revision); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, "UPDATE owner_draft_requests SET state=?,grant_id=?,expires=?,revision=revision+1 WHERE id=?", state, grant, expires.UnixMilli(), id); e != nil {
			return e
		}
		var name string
		if e = tx.QueryRowContext(ctx, "SELECT json_extract(data,'$.name') FROM authority_items WHERE kind='principals' AND id=?", v.Principal).Scan(&name); e != nil {
			return e
		}
		if e = recordOwnerAccess(ctx, tx, "draft_collaboration", v.ID, state, contract.OwnerAccessFact{Subject: name}); e != nil {
			return e
		}
		if e = workChanged(ctx, tx); e != nil {
			return e
		}
		out, e = collaboration(ctx, tx, id, a)
		return e
	})
	return
}

func (s *Store) EndDraftCollaboration(ctx context.Context, id string) error {
	return s.write(workContext(ctx, controlWork), func(tx *sql.Tx) error {
		a, e := requireDraftManager(ctx, tx, false)
		if e != nil {
			return e
		}
		v, e := collaboration(ctx, tx, id, a)
		if e != nil {
			return e
		}
		if v.State != "approved" && v.State != "awaiting_approval" {
			return nil
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM owner_draft_grants WHERE id=?", v.Grant); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE owner_draft_requests SET state='cancelled',revision=revision+1 WHERE id=?", id); e != nil {
			return e
		}
		if e = cancelFrozenHandoffForOwnerWork(ctx, tx); e != nil {
			return e
		}
		return workChanged(ctx, tx)
	})
}

func (s *Store) CancelDraftInvitation(ctx context.Context, id string) error {
	return s.write(workContext(ctx, controlWork), func(tx *sql.Tx) error {
		if _, e := requireDraftManager(ctx, tx, false); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "DELETE FROM owner_draft_grants WHERE id IN (SELECT grant_id FROM owner_draft_requests WHERE invitation=?)", id); e != nil {
			return e
		}
		r, e := tx.ExecContext(ctx, "DELETE FROM owner_draft_invitations WHERE id=?", id)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil || n == 0 {
			return e
		}
		if e := cancelFrozenHandoffForOwnerWork(ctx, tx); e != nil {
			return e
		}
		return workChanged(ctx, tx)
	})
}
