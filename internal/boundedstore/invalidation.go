package boundedstore

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) invalidateBatch(ctx context.Context, tx *sql.Tx) (bool, error) {
	var asset, snapshot, cursor string
	var rev uint64
	var forget bool
	e := tx.QueryRowContext(ctx, "SELECT asset,revision,snapshot,forget,cursor FROM invalidation_jobs ORDER BY forget DESC,asset,revision,snapshot LIMIT 1").Scan(&asset, &rev, &snapshot, &forget, &cursor)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var id, owner, oldSnapshot string
	var ownerRev uint64
	e = tx.QueryRowContext(ctx, `SELECT o.id,o.asset,o.revision,o.snapshot FROM organizations o WHERE o.id IN (
 SELECT d.organization FROM dependencies d WHERE d.asset=? AND ( ? OR (?<>0 AND d.revision<>?) OR (?<>'' AND d.snapshot<>'' AND d.snapshot<>?))
 UNION SELECT id FROM organizations WHERE asset=? AND (? OR (?<>0 AND revision<>?))
 ) AND o.id>? AND o.state<>'retired' ORDER BY o.id LIMIT 1`, asset, forget, rev, rev, snapshot, snapshot, asset, forget, rev, rev, cursor).Scan(&id, &owner, &ownerRev, &oldSnapshot)
	if errors.Is(e, sql.ErrNoRows) {
		_, e = tx.ExecContext(ctx, "DELETE FROM invalidation_jobs WHERE asset=? AND revision=? AND snapshot=? AND forget=?", asset, rev, snapshot, forget)
		return true, e
	}
	if e != nil {
		return false, e
	}
	if !forget {
		valid, e := organizationInputsCurrent(ctx, tx, id)
		if e != nil {
			return false, e
		}
		if valid {
			_, e = tx.ExecContext(ctx, "UPDATE invalidation_jobs SET cursor=? WHERE asset=? AND revision=? AND snapshot=? AND forget=?", id, asset, rev, snapshot, forget)
			return true, e
		}
	}
	// Only retire a version whose recorded input changed. New publications have
	// their own identity and are checked again when they become visible.
	if e = s.retireOrganization(ctx, tx, id); e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO semantic_jobs SELECT id,revision,'dependency_changed' FROM live_assets WHERE id=? ON CONFLICT(asset) DO UPDATE SET revision=excluded.revision,reason=excluded.reason", owner); e != nil {
		return false, e
	}
	// A missing snapshot is represented by a nonempty sentinel, so every consumer
	// of the retired snapshot becomes stale without changing raw-input consumers.
	if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO invalidation_jobs(asset,revision,snapshot,forget) VALUES(?,0,?,0)", owner, "retired:"+oldSnapshot); e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE derived_state SET epoch=epoch+1 WHERE singleton=1"); e != nil {
		return false, e
	}
	_, e = tx.ExecContext(ctx, "UPDATE invalidation_jobs SET cursor=? WHERE asset=? AND revision=? AND snapshot=? AND forget=?", id, asset, rev, snapshot, forget)
	return true, e
}
