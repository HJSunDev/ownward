package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func (s *Store) initializeMaintenance(ctx context.Context) error {
	return s.writer.QueryRowContext(ctx, "UPDATE store_meta SET value=value+1 WHERE key='run_epoch' RETURNING value").Scan(&s.runEpoch)
}
func (s *Store) wakeMaintenance() {
	select {
	case s.maintenanceWake <- struct{}{}:
	default:
	}
}
func (s *Store) startMaintenance() {
	ctx, cancel := context.WithCancel(workContext(context.Background(), maintenanceWork))
	s.maintenanceCancel = cancel
	s.maintenanceDone = make(chan struct{})
	go func() {
		defer close(s.maintenanceDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.maintenanceWake:
			}
			for {
				more, e := s.Maintain(ctx)
				s.maintenanceMu.Lock()
				s.maintenanceErr = e
				s.maintenanceMu.Unlock()
				if e != nil || !more {
					break
				}
			}
		}
	}()
	s.wakeMaintenance()
}

// Maintain executes one bounded batch. Durable jobs remain authoritative across
// cancellation; callers can drain the same queue without starting another worker.
func (s *Store) Maintain(ctx context.Context) (bool, error) {
	if ctx.Value(foregroundKey{}) != s {
		if e := s.workAdmission.acquire(ctx, maintenanceWork); e != nil {
			return false, e
		}
		defer s.workAdmission.Unlock()
	}
	if e := s.maintenanceBatch.acquire(ctx, maintenanceWork); e != nil {
		return false, e
	}
	defer s.maintenanceBatch.Unlock()
	ctx = workContext(ctx, maintenanceWork)
	if e := s.maintainWAL(ctx); e != nil {
		return false, e
	}
	release, e := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 256*1024, false)
	if e != nil {
		return false, e
	}
	defer release()
	more := false
	e = s.write(ctx, func(tx *sql.Tx) error {
		var e error
		for _, step := range []func(context.Context, *sql.Tx) (bool, error){s.recoverStage, s.invalidateBatch, s.forgetBatch, s.reclaimPayload, s.reclaimDerived, s.expireCursors} {
			more, e = step(ctx, tx)
			if e != nil || more {
				return e
			}
		}
		return nil
	})
	if e != nil || more {
		return more, e
	}
	// Packing has its own bounded workspace and must not wait while holding ours.
	release()
	release = func() {}
	if more, e = s.maintainVectors(ctx); e != nil || more {
		return more, e
	}
	if more, e = s.finishForget(ctx); e != nil || more {
		return more, e
	}
	if more, e = s.cleanCopies(ctx, false); e != nil || more {
		return more, e
	}
	return s.vacuumBatch(ctx)
}

// DrainMaintenance waits only for accepted storage work, not for new AI output.
func (s *Store) DrainMaintenance(ctx context.Context) error {
	for {
		more, e := s.Maintain(ctx)
		if e != nil {
			return e
		}
		if !more {
			return nil
		}
	}
}

func (s *Store) maintainWAL(ctx context.Context) error {
	if s.walBytes() < walPassive {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errors.New("存储已关闭")
	}
	if e := s.writeMu.acquire(ctx, maintenanceWork); e != nil {
		return e
	}
	defer s.writeMu.Unlock()
	mode := "PASSIVE"
	if s.walBytes()+walTransactionReserve >= walOrdinary {
		s.setDraining(true)
		mode = "TRUNCATE"
	}
	ok, e := s.checkpointLocked(ctx, mode)
	if ok && mode == "TRUNCATE" {
		s.setDraining(false)
	}
	return e
}

func (s *Store) recoverStage(ctx context.Context, tx *sql.Tx) (bool, error) {
	var kind, id string
	e := tx.QueryRowContext(ctx, "SELECT kind,id FROM staging_owners WHERE epoch<(SELECT value FROM store_meta WHERE key='run_epoch') ORDER BY epoch,kind,id LIMIT 1").Scan(&kind, &id)
	if errors.Is(e, sql.ErrNoRows) {
		// Unit-two stores may contain unpublished rows created before ownership
		// markers existed. Locate them through state indexes, one per batch.
		for _, v := range []struct{ kind, table string }{{"payload", "payloads"}, {"organization", "organizations"}, {"vector_block", "vector_blocks"}} {
			queued := "NOT EXISTS(SELECT 1 FROM derived_reclaim r WHERE r.id=o.id)"
			if v.kind == "payload" {
				queued = "NOT EXISTS(SELECT 1 FROM reclaim_jobs r WHERE r.payload=o.id)"
			}
			e = tx.QueryRowContext(ctx, "SELECT id FROM "+v.table+" o WHERE state IN ('staging','ready') AND "+queued+" AND NOT EXISTS(SELECT 1 FROM staging_owners w WHERE w.kind=? AND w.id=o.id) LIMIT 1", v.kind).Scan(&id)
			if e == nil {
				kind = v.kind
				break
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return false, e
			}
		}
		if errors.Is(e, sql.ErrNoRows) {
			return false, nil
		}
	}
	if e != nil {
		return false, e
	}
	if kind == "payload" {
		_, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO reclaim_jobs VALUES(?,'interrupted_stage')", id)
	} else {
		_, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim VALUES(?,?,'interrupted_stage')", id, kind)
	}
	if e == nil {
		_, e = tx.ExecContext(ctx, "DELETE FROM staging_owners WHERE kind=? AND id=?", kind, id)
	}
	return true, e
}

func deleteBatch(ctx context.Context, tx *sql.Tx, table, key, id string, keys string, limit int) (bool, error) {
	// Table/key names are internal constants, never supplied by tool input.
	q := fmt.Sprintf("DELETE FROM %s WHERE (%s) IN (SELECT %s FROM %s WHERE %s=? LIMIT %d)", table, keys, keys, table, key, limit)
	r, e := tx.ExecContext(ctx, q, id)
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	return n > 0, e
}
func (s *Store) reclaimPayload(ctx context.Context, tx *sql.Tx) (bool, error) {
	var id string
	e := tx.QueryRowContext(ctx, "SELECT payload FROM reclaim_jobs ORDER BY payload LIMIT 1").Scan(&id)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	var used bool
	if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM assets WHERE payload=? UNION ALL SELECT 1 FROM control_records WHERE payload=?)", id, id).Scan(&used); e != nil {
		return false, e
	}
	if used {
		return false, errors.New("回收任务仍引用当前原文或控制记录")
	}
	r, e := tx.ExecContext(ctx, "DELETE FROM postings WHERE (term,payload) IN (SELECT term,payload FROM postings WHERE payload=(SELECT id FROM lexical_payload_ids WHERE payload=?) LIMIT 16)", id)
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	if e != nil || n > 0 {
		return n > 0, e
	}
	for _, v := range []struct {
		table, keys string
		n           int
	}{
		{"content_chunks", "payload,part,ordinal", 8},
		{"lexical_contexts", "payload,ordinal", 16}, {"explicit_links", "payload,ordinal", 64},
		{"lexical_documents", "payload", 1},
	} {
		if more, e := deleteBatch(ctx, tx, v.table, "payload", id, v.keys, v.n); e != nil || more {
			return more, e
		}
	}
	for _, q := range []string{"DELETE FROM reclaim_jobs WHERE payload=?", "DELETE FROM staging_owners WHERE kind='payload' AND id=?", "DELETE FROM payloads WHERE id=?"} {
		if _, e = tx.ExecContext(ctx, q, id); e != nil {
			return false, e
		}
	}
	return true, nil
}

func (s *Store) retireOrganization(ctx context.Context, tx *sql.Tx, id string) error {
	if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO organization_heads SELECT generation,asset,organization FROM organization_current WHERE organization=?", id); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, "DELETE FROM organization_current WHERE organization=?", id); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, "UPDATE organizations SET state='retired' WHERE id=?", id); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim VALUES(?,'organization','invalidated')", id)
	return e
}
func (s *Store) reclaimDerived(ctx context.Context, tx *sql.Tx) (bool, error) {
	var id, kind string
	e := tx.QueryRowContext(ctx, "SELECT id,kind FROM derived_reclaim WHERE kind IN ('organization','generation','vector_block') ORDER BY (kind='generation'),kind,id LIMIT 1").Scan(&id, &kind)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	switch kind {
	case "organization":
		var current bool
		if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM organization_current WHERE organization=?)", id).Scan(&current); e != nil {
			return false, e
		}
		if current {
			return false, errors.New("回收任务仍引用当前组织结果")
		}
		// 块头和筛选页也包含成员信息；删除成员前登记清理，不能依赖占用率阈值。
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim SELECT block,'vector_filter','member_retired' FROM vector_members WHERE organization=?", id); e != nil {
			return false, e
		}
		for _, v := range []struct {
			table, keys string
			n           int
		}{
			{"organization_chunks", "organization,ordinal", 8}, {"graph_links", "organization,ordinal", 2},
			{"graph_units", "organization,id", 8}, {"graph_names", "term,organization", 64},
			{"graph_explicit", "organization,target", 64}, {"organization_contexts", "organization,ordinal", 16},
			{"organization_headers", "organization", 1}, {"vector_members", "block,ordinal", 16},
			{"vector_delta", "organization", 1}, {"vectors", "organization", 1},
			{"dependencies", "organization,asset,revision,snapshot", 64}, {"organization_publications", "sequence", 1},
		} {
			if more, e := deleteBatch(ctx, tx, v.table, "organization", id, v.keys, v.n); e != nil || more {
				return more, e
			}
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM organizations WHERE id=?", id); e != nil {
			return false, e
		}
	case "generation":
		var active bool
		if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM derived_state WHERE generation=?)", id).Scan(&active); e != nil {
			return false, e
		}
		if active {
			return false, errors.New("不能回收活动世代")
		}
		var org string
		e = tx.QueryRowContext(ctx, "SELECT id FROM organizations WHERE generation=? LIMIT 1", id).Scan(&org)
		if e == nil {
			return true, s.retireOrganization(ctx, tx, org)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return false, e
		}
		if more, e := deleteBatch(ctx, tx, "organization_heads", "generation", id, "generation,asset", 64); e != nil || more {
			return more, e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM generations WHERE id=?", id); e != nil {
			return false, e
		}
	case "vector_block":
		var active bool
		if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM vector_blocks WHERE id=? AND state='active')", id).Scan(&active); e != nil {
			return false, e
		}
		if active {
			return false, errors.New("不能回收活动向量块")
		}
		for _, v := range []struct {
			table, keys string
			n           int
		}{{"vector_members", "block,ordinal", 64}, {"vector_filter_pages", "block,level,page", 4}} {
			if more, e := deleteBatch(ctx, tx, v.table, "block", id, v.keys, v.n); e != nil || more {
				return more, e
			}
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM vector_blocks WHERE id=?", id); e != nil {
			return false, e
		}
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM derived_reclaim WHERE id=?", id); e != nil {
		return false, e
	}
	_, e = tx.ExecContext(ctx, "DELETE FROM staging_owners WHERE kind=? AND id=?", kind, id)
	return true, e
}

func (s *Store) expireCursors(ctx context.Context, tx *sql.Tx) (bool, error) {
	r, e := tx.ExecContext(ctx, "DELETE FROM navigation_cursors WHERE id IN (SELECT id FROM navigation_cursors WHERE expires<? LIMIT 16)", time.Now().Unix())
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	return n > 0, e
}
func (s *Store) vacuumBatch(ctx context.Context) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false, errors.New("存储已关闭")
	}
	if e := s.admitWrite(ctx); e != nil {
		return false, e
	}
	defer s.writeMu.Unlock()
	var free int
	if e := s.writer.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); e != nil {
		return false, e
	}
	if free <= 512 {
		return false, nil
	}
	// Consume the result so SQLite performs every requested incremental step.
	rows, e := s.writer.QueryContext(ctx, "PRAGMA incremental_vacuum(64)")
	if e != nil {
		return false, e
	}
	defer rows.Close()
	for rows.Next() {
	}
	return true, rows.Err()
}

func (s *Store) awaitReclaim(ctx context.Context) error {
	if classOf(ctx) == controlWork {
		return nil
	}
	s.maintenanceMu.Lock()
	failure := s.maintenanceErr
	s.maintenanceMu.Unlock()
	if failure != nil && !errors.Is(failure, context.Canceled) {
		return fmt.Errorf("存储维护未完成: %w", failure)
	}
	for {
		var backlog int64
		e := s.view(workContext(ctx, maintenanceWork), func(q queryer) error {
			return q.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='reclaim_bytes'").Scan(&backlog)
		})
		if e != nil || backlog < 64*resourcebudget.MiB {
			return e
		}
		more, e := s.Maintain(ctx)
		if e != nil {
			return e
		}
		if !more {
			return errors.New("回收积压无法推进，未接受新的替换")
		}
	}
}
