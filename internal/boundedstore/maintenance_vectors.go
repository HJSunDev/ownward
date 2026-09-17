package boundedstore

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) maintainVectors(ctx context.Context) (bool, error) {
	var id, kind string
	e := s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT id,kind FROM derived_reclaim WHERE kind IN ('vector_filter','vector_pack') ORDER BY kind,id LIMIT 1").Scan(&id, &kind)
	})
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if kind == "vector_filter" {
		s.vectorMu.Lock()
		defer s.vectorMu.Unlock()
		e = s.write(ctx, func(tx *sql.Tx) error {
			// Filters are disposable; exact vectors remain authoritative and searchable.
			if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO vector_delta SELECT v.organization,v.space FROM vector_members m JOIN vectors v ON v.organization=m.organization WHERE m.block=?", id); e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim SELECT space,'vector_pack','filter_repair' FROM vector_blocks WHERE id=?", id); e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, "UPDATE vector_blocks SET state='retired' WHERE id=?", id); e != nil {
				return e
			}
			_, e := tx.ExecContext(ctx, "UPDATE derived_reclaim SET kind='vector_block',reason='filter_repair' WHERE id=?", id)
			return e
		})
		return true, e
	}
	var eligible bool
	e = s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM vector_delta WHERE space=?)>=256 OR EXISTS(SELECT 1 FROM vector_blocks b WHERE b.space=? AND b.state='active' AND (SELECT count(*) FROM vector_members m JOIN organization_current c ON c.organization=m.organization WHERE m.block=b.id)*4<=b.members*3)`, id, id).Scan(&eligible)
	})
	if e != nil {
		return false, e
	}
	if eligible {
		if _, e = s.PackVectors(ctx, id, false); e != nil {
			return false, e
		}
		return true, nil
	}
	e = s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "DELETE FROM derived_reclaim WHERE id=? AND kind='vector_pack'", id)
		return e
	})
	return true, e
}
