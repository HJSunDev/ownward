package boundedstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
)

const OwnerEventLimit = 4096
const OwnerHistoryRetention = 30 * 24 * time.Hour

func recordOwnerEvent(ctx context.Context, tx *sql.Tx, kind, id string, revision uint64, op, status string) error {
	return recordOwnerEventWithChanges(ctx, tx, kind, id, revision, op, status, contract.RelationInvalidationCounts{})
}

func recordOwnerEventWithChanges(ctx context.Context, tx *sql.Tx, kind, id string, revision uint64, op, status string, changes contract.RelationInvalidationCounts) error {
	r, e := tx.ExecContext(ctx, "INSERT INTO owner_events(kind,asset,revision,operation,status,at) VALUES(?,?,?,?,?,?)", kind, id, revision, op, status, time.Now().UnixMilli())
	if e != nil || changes == (contract.RelationInvalidationCounts{}) {
		return e
	}
	sequence, e := r.LastInsertId()
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO owner_event_relation_changes VALUES(?,?,?,?)", sequence, changes.QuoteMissing, changes.QuoteAmbiguous, changes.TargetUnavailable)
	return e
}

func (s *Store) OwnerEvents(ctx context.Context, after uint64, limit int) (contract.OwnerEventPage, error) {
	var out contract.OwnerEventPage
	if limit < 1 || limit > 100 {
		return out, errors.New("事件分页上限为 100")
	}
	e := s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return e
		}
		var floor uint64
		cutoff := time.Now().Add(-OwnerHistoryRetention).UnixMilli()
		if e := q.QueryRowContext(ctx, "SELECT max(value,coalesce((SELECT max(sequence) FROM owner_events WHERE at<?),0)) FROM store_meta WHERE key='owner_event_floor'", cutoff).Scan(&floor); e != nil {
			return e
		}
		out.Reset = after != 0 && after < floor
		out.Next = max(after, floor)
		rows, e := q.QueryContext(ctx, `SELECT e.sequence,e.kind,e.asset,e.revision,e.operation,e.status,e.at,
 coalesce(c.quote_missing,0),coalesce(c.quote_ambiguous,0),coalesce(c.target_unavailable,0)
 FROM owner_events e LEFT JOIN owner_event_relation_changes c ON c.sequence=e.sequence
 WHERE e.sequence>? AND e.at>=? ORDER BY e.sequence LIMIT ?`, max(after, floor), cutoff, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v contract.OwnerEvent
			var at int64
			var changes contract.RelationInvalidationCounts
			if e = rows.Scan(&v.Sequence, &v.Kind, &v.Asset.ID, &v.Asset.Revision, &v.Operation, &v.Status, &at, &changes.QuoteMissing, &changes.QuoteAmbiguous, &changes.TargetUnavailable); e != nil {
				return e
			}
			if changes != (contract.RelationInvalidationCounts{}) {
				v.InvalidatedRelations = &changes
			}
			v.At = time.UnixMilli(at).UTC()
			out.Items = append(out.Items, v)
			out.Next = v.Sequence
		}
		return rows.Err()
	})
	s.wakeMaintenance()
	return out, e
}

// Pending decisions stay in the authoritative queue. Completed display history
// expires; permission and replay facts are not deleted to prune a timeline.
func (s *Store) OwnerOperations(ctx context.Context, after string, limit int, pending bool) ([]contract.ManagementReceipt, string, error) {
	var out []contract.ManagementReceipt
	next := ""
	if limit < 1 || limit > 100 {
		return nil, "", errors.New("操作分页上限为 100")
	}
	e := s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return e
		}
		condition := "a.state NOT IN ('completed','declined')"
		if !pending {
			condition = "a.state IN ('completed','declined') AND EXISTS(SELECT 1 FROM owner_operation_times t WHERE t.id=a.id AND t.updated>=?)"
		}
		args := []any{after}
		if !pending {
			args = append(args, time.Now().Add(-OwnerHistoryRetention).UnixMilli())
		}
		args = append(args, limit)
		rows, e := q.QueryContext(ctx, `WITH page AS (SELECT a.id,a.data FROM authority_items a
 WHERE a.kind='operations' AND a.id>? AND `+condition+` ORDER BY a.id LIMIT ?)
 SELECT id,data FROM (SELECT id,data,sum(length(data)) OVER(ORDER BY id) AS bytes FROM page)
 WHERE bytes<=524288 ORDER BY id`, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var b []byte
			if e = rows.Scan(&id, &b); e != nil {
				return e
			}
			var v contract.ManagementReceipt
			if e = json.Unmarshal(b, &v); e != nil {
				return e
			}
			out = append(out, v)
			next = id
		}
		return rows.Err()
	})
	s.wakeMaintenance()
	return out, next, e
}

func (s *Store) expireOwnerHistory(ctx context.Context, tx *sql.Tx) (bool, error) {
	cutoff := time.Now().Add(-OwnerHistoryRetention).UnixMilli()
	var last sql.NullInt64
	e := tx.QueryRowContext(ctx, `SELECT max(sequence) FROM (SELECT sequence FROM owner_events
 WHERE at<? OR sequence<=coalesce((SELECT max(sequence) FROM owner_events),0)-? ORDER BY sequence LIMIT 64)`, cutoff, OwnerEventLimit).Scan(&last)
	if e != nil {
		return false, e
	}
	if last.Valid {
		if _, e = tx.ExecContext(ctx, "DELETE FROM owner_events WHERE sequence<=?", last.Int64); e != nil {
			return false, e
		}
		_, e = tx.ExecContext(ctx, "UPDATE store_meta SET value=max(value,?) WHERE key='owner_event_floor'", last.Int64)
		return true, e
	}
	r, e := tx.ExecContext(ctx, `DELETE FROM owner_operation_times WHERE id IN (
 SELECT id FROM owner_operation_times WHERE state IN ('completed','declined') AND updated<? LIMIT 64)`, cutoff)
	if e != nil {
		return false, e
	}
	n, e := r.RowsAffected()
	if e != nil || n > 0 {
		return n > 0, e
	}
	r, e = tx.ExecContext(ctx, `DELETE FROM owner_operation_times WHERE id IN (
 SELECT id FROM owner_operation_times WHERE state IN ('completed','declined') ORDER BY updated DESC,id DESC LIMIT 64 OFFSET ?)`, OwnerEventLimit)
	if e != nil {
		return false, e
	}
	n, e = r.RowsAffected()
	return n > 0, e
}
