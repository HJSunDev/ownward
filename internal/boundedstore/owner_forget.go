package boundedstore

import (
	"context"
	"database/sql"
	"errors"
)

// Visibility is stopped by live_assets immediately at the forget barrier.
// Cleanup removes one private draft or retained source per short transaction,
// before the asset itself is removed. A restart continues these same joins.
func (s *Store) forgetOwnerWork(ctx context.Context, tx *sql.Tx) (bool, error) {
	var id, payload string
	e := tx.QueryRowContext(ctx, `SELECT d.id,d.payload FROM forget_operations f
 JOIN forget_targets t ON t.operation=f.id JOIN owner_drafts d ON d.target=t.asset
 WHERE f.state='cleaning' LIMIT 1`).Scan(&id, &payload)
	if e == nil {
		return true, deleteDraft(ctx, tx, id, payload)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return false, e
	}
	e = tx.QueryRowContext(ctx, `SELECT o.asset,o.payload FROM forget_operations f
 JOIN forget_targets t ON t.operation=f.id JOIN asset_originals o ON o.asset=t.asset
 WHERE f.state='cleaning' LIMIT 1`).Scan(&id, &payload)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM asset_originals WHERE asset=?", id); e != nil {
		return false, e
	}
	_, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO reclaim_jobs VALUES(?,'forgotten_original')", payload)
	return true, e
}
