package boundedstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/HJSunDev/ownward/internal/contract"
)

// publishAuthorizedForget shares the transaction with the approved control
// decision. A changed target rolls back both the decision and the barrier.
func publishAuthorizedForget(ctx context.Context, tx *sql.Tx, op contract.ManagementReceipt) error {
	var exists bool
	if e := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM forget_operations WHERE id=? AND state<>'staging')", op.Request.ID).Scan(&exists); e != nil || exists {
		return e
	}
	if len(op.Request.Targets) == 0 || len(op.Request.Targets) > contract.MaxForgetTargets {
		return errors.New("遗忘目标数量无效")
	}
	if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO forget_operations(id,principal,digest,state) VALUES(?,'control','','staging')", op.Request.ID); e != nil {
		return e
	}
	for _, v := range op.Request.Targets {
		var revision uint64
		if e := tx.QueryRowContext(ctx, "SELECT revision FROM live_assets WHERE id=?", v.ID).Scan(&revision); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return contract.ErrForgetScopeChanged
			}
			return e
		}
		if revision != v.Revision {
			return contract.ErrForgetScopeChanged
		}
		r, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO forget_targets VALUES(?,?,?)", op.Request.ID, v.ID, v.Revision)
		if e != nil {
			return e
		}
		added, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if added > 0 {
			if _, e = tx.ExecContext(ctx, "UPDATE forget_operations SET targets=targets+1,terms=terms+coalesce((SELECT d.length FROM assets a JOIN lexical_documents d ON d.payload=a.payload WHERE a.id=?),0) WHERE id=?", v.ID, op.Request.ID); e != nil {
				return e
			}
		}
	}
	if _, e := tx.ExecContext(ctx, "UPDATE lexical_stats SET documents=documents-(SELECT targets FROM forget_operations WHERE id=?),terms=terms-(SELECT terms FROM forget_operations WHERE id=?) WHERE singleton=1", op.Request.ID, op.Request.ID); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, "UPDATE forget_operations SET state='cleaning' WHERE id=?", op.Request.ID); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, "UPDATE store_meta SET value=value+1 WHERE key IN ('asset_epoch','run_epoch','operation_generation')"); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, "UPDATE derived_state SET epoch=epoch+1 WHERE singleton=1")
	return e
}
