package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"github.com/HJSunDev/ownward/internal/contract"
)

type ForgetTarget struct {
	ID       string
	Revision uint64
}

func management(ctx context.Context, q querier) error {
	if e := checkAccess(ctx, q); e != nil {
		return e
	}
	if lease, ok := ctx.Value(accessKey{}).(accessLease); ok && lease.permission&4 == 0 {
		return errors.New("需要管理权限")
	}
	return nil
}

// StageForget is a trusted control-layer port. Batches remain invisible until
// CommitForget publishes the stop-use barrier; the operation identity is stable.
func (s *Store) StageForget(ctx context.Context, op string, targets []ForgetTarget) error {
	if op == "" || len(op) > 256 || len(targets) == 0 || len(targets) > 64 {
		return errors.New("遗忘身份或批次无效")
	}
	ctx = workContext(ctx, controlWork)
	return s.write(ctx, func(tx *sql.Tx) error {
		if e := management(ctx, tx); e != nil {
			return e
		}
		principal := contract.AuthenticationDigest(ctx)
		if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO forget_operations(id,principal,digest,state) VALUES(?,?,'','staging')", op, principal); e != nil {
			return e
		}
		var owner, state string
		if e := tx.QueryRowContext(ctx, "SELECT principal,state FROM forget_operations WHERE id=?", op).Scan(&owner, &state); e != nil {
			return e
		}
		if owner != principal || state != "staging" {
			return errors.New("遗忘操作已发布或不属于当前管理者")
		}
		for _, v := range targets {
			var revision uint64
			if e := tx.QueryRowContext(ctx, "SELECT revision FROM live_assets WHERE id=?", v.ID).Scan(&revision); e != nil {
				return e
			}
			if revision != v.Revision || v.Revision == 0 {
				return errors.New("遗忘目标版本已变化")
			}
			var previous uint64
			e := tx.QueryRowContext(ctx, "SELECT revision FROM forget_targets WHERE operation=? AND asset=?", op, v.ID).Scan(&previous)
			if e == nil && previous != v.Revision {
				return errors.New("同一遗忘操作不能更换目标版本")
			}
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			result, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO forget_targets VALUES(?,?,?)", op, v.ID, v.Revision)
			if e != nil {
				return e
			}
			added, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if added > 0 {
				if _, e = tx.ExecContext(ctx, "UPDATE forget_operations SET targets=targets+1,terms=terms+coalesce((SELECT d.length FROM assets a JOIN lexical_documents d ON d.payload=a.payload WHERE a.id=?),0) WHERE id=?", v.ID, op); e != nil {
					return e
				}
			}
		}
		return nil
	})
}

func (s *Store) CommitForget(ctx context.Context, op string) error {
	ctx = workContext(ctx, controlWork)
	return contract.Commit(ctx, func() error {
		return s.write(ctx, func(tx *sql.Tx) error {
			if e := management(ctx, tx); e != nil {
				return e
			}
			var principal, state string
			var count int
			if e := tx.QueryRowContext(ctx, "SELECT principal,state,targets FROM forget_operations WHERE id=?", op).Scan(&principal, &state, &count); e != nil {
				return e
			}
			if principal != contract.AuthenticationDigest(ctx) {
				return errors.New("遗忘操作不属于当前管理者")
			}
			if state != "staging" {
				return nil
			}
			if count == 0 {
				return errors.New("遗忘目标为空")
			}
			var changed bool
			if e := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM forget_targets t LEFT JOIN live_assets a ON a.id=t.asset WHERE t.operation=? AND (a.id IS NULL OR a.revision<>t.revision))", op).Scan(&changed); e != nil {
				return e
			}
			if changed {
				return errors.New("遗忘目标版本已变化，请重新确认")
			}
			if _, e := tx.ExecContext(ctx, "UPDATE lexical_stats SET documents=documents-(SELECT targets FROM forget_operations WHERE id=?),terms=terms-(SELECT terms FROM forget_operations WHERE id=?) WHERE singleton=1", op, op); e != nil {
				return e
			}
			for _, q := range []string{"UPDATE forget_operations SET state='cleaning' WHERE id=?", "UPDATE access_header SET deletion_epoch=deletion_epoch+1,revision=revision+1 WHERE singleton=1", "UPDATE store_meta SET value=value+1 WHERE key IN ('asset_epoch','run_epoch','operation_generation')", "UPDATE derived_state SET epoch=epoch+1 WHERE singleton=1"} {
				var e error
				if q == "UPDATE forget_operations SET state='cleaning' WHERE id=?" {
					_, e = tx.ExecContext(ctx, q, op)
				} else {
					_, e = tx.ExecContext(ctx, q)
				}
				if e != nil {
					return e
				}
			}
			return nil
		})
	})
}
func (s *Store) ForgetState(ctx context.Context, op string) (string, error) {
	ctx = workContext(ctx, controlWork)
	var state string
	e := s.view(ctx, func(q queryer) error {
		if e := management(ctx, q); e != nil {
			return e
		}
		return q.QueryRowContext(ctx, "SELECT state FROM forget_operations WHERE id=?", op).Scan(&state)
	})
	return state, e
}

func (s *Store) forgetBatch(ctx context.Context, tx *sql.Tx) (bool, error) {
	var asset, payload string
	e := tx.QueryRowContext(ctx, `SELECT a.id,a.payload FROM forget_operations f JOIN forget_targets t ON t.operation=f.id JOIN assets a ON a.id=t.asset WHERE f.state='cleaning' LIMIT 1`).Scan(&asset, &payload)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{"INSERT OR IGNORE INTO invalidation_jobs(asset,revision,snapshot,forget) VALUES(?,0,'',1)", []any{asset}},
		{"DELETE FROM semantic_jobs WHERE asset=?", []any{asset}},
		{"DELETE FROM assets WHERE id=?", []any{asset}},
		{"INSERT OR IGNORE INTO reclaim_jobs VALUES(?,'forgotten')", []any{payload}},
		{"INSERT INTO source_epochs VALUES(?,1) ON CONFLICT(id) DO UPDATE SET revision=revision+1", []any{asset}},
	} {
		if _, e = tx.ExecContext(ctx, stmt.q, stmt.args...); e != nil {
			return false, e
		}
	}
	return true, nil
}

func (s *Store) finishForget(ctx context.Context) (bool, error) {
	var op string
	e := s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT id FROM forget_operations WHERE state='cleaning' LIMIT 1").Scan(&op)
	})
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	// All earlier durable cleanup queues have drained. Stop old snapshots before
	// removing WAL copies; secure_delete also clears freed main-database cells.
	if more, e := s.cleanCopies(ctx, true); e != nil || more {
		return more, e
	}
	var copies bool
	if e = s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM controlled_copies)").Scan(&copies)
	}); e != nil || copies {
		return false, e
	}
	s.cancelReaders()
	s.mu.RLock()
	if e = s.writeMu.acquire(ctx, controlWork); e != nil {
		s.mu.RUnlock()
		return false, e
	}
	ok, e := s.checkpointLocked(ctx, "TRUNCATE")
	s.writeMu.Unlock()
	s.mu.RUnlock()
	if e != nil {
		return false, e
	}
	if !ok {
		return false, nil
	}
	e = s.write(workContext(ctx, controlWork), func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "UPDATE forget_operations SET state='complete' WHERE id=? AND state='cleaning'", op)
		return e
	})
	return true, e
}
