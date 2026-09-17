package boundedstore

import (
	"context"
	"errors"
)

// The directory lock excludes other instances. A format marker and renamed
// source table make interrupted upgrades resumable without rebuilding terms.
func (s *Store) upgradeLexicalStorage(ctx context.Context) error {
	var present int
	if e := s.writer.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='store_meta'").Scan(&present); e != nil || present == 0 {
		return e
	}
	var version int
	if e := s.writer.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='format'").Scan(&version); e != nil {
		return e
	}
	if version < 3 {
		tx, e := s.writer.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='postings'").Scan(&present); e != nil {
			return e
		}
		if present != 0 {
			if _, e = tx.ExecContext(ctx, "ALTER TABLE postings RENAME TO lexical_upgrade_source;"); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, retrievalSchema+"UPDATE store_meta SET value=3 WHERE key='format';"); e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	if e := s.writer.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='lexical_upgrade_source'").Scan(&present); e != nil || present == 0 {
		return e
	}
	for {
		more, e := s.upgradeLexicalBatch(ctx)
		if e != nil {
			return e
		}
		// No readers are admitted during the upgrade; keep its WAL bounded too.
		if _, e = s.writer.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
			return e
		}
		if !more {
			return s.reclaimUpgradePages(ctx)
		}
	}
}

func (s *Store) reclaimUpgradePages(ctx context.Context) error {
	for {
		var free int64
		if e := s.writer.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); e != nil || free == 0 {
			return e
		}
		rows, e := s.writer.QueryContext(ctx, "PRAGMA incremental_vacuum(64)")
		if e != nil {
			return e
		}
		for rows.Next() {
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if _, e = s.writer.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
			return e
		}
	}
}

func (s *Store) upgradeLexicalBatch(ctx context.Context) (bool, error) {
	tx, e := s.writer.BeginTx(ctx, nil)
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, "SELECT term,payload,frequency FROM lexical_upgrade_source ORDER BY term,payload LIMIT 64")
	if e != nil {
		return false, e
	}
	type posting struct {
		term      []byte
		payload   string
		frequency int64
	}
	batch := make([]posting, 0, 64)
	for rows.Next() {
		var p posting
		if e = rows.Scan(&p.term, &p.payload, &p.frequency); e != nil {
			rows.Close()
			return false, e
		}
		batch = append(batch, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	for _, p := range batch {
		if _, e = tx.ExecContext(ctx, "INSERT INTO lexical_payload_ids(payload) VALUES(?) ON CONFLICT(payload) DO NOTHING", p.payload); e != nil {
			return false, e
		}
		var id int64
		if e = tx.QueryRowContext(ctx, "SELECT id FROM lexical_payload_ids WHERE payload=?", p.payload).Scan(&id); e != nil {
			return false, e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO postings VALUES(?,?,?)", p.term, id, p.frequency); e != nil {
			return false, e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM lexical_upgrade_source WHERE term=? AND payload=?", p.term, p.payload); e != nil {
			return false, e
		}
	}
	if len(batch) == 0 {
		if _, e = tx.ExecContext(ctx, "DROP TABLE lexical_upgrade_source"); e != nil {
			return false, e
		}
	}
	if e = ctx.Err(); e != nil {
		return false, e
	}
	if e = tx.Commit(); e != nil {
		return false, errors.Join(errors.New("词法格式升级提交未确认，重新打开以接续"), e)
	}
	return len(batch) != 0, nil
}
