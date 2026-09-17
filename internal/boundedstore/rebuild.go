package boundedstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/HJSunDev/ownward/internal/contract"
)

// BeginRebuild durably owns the candidate so an interrupted process cannot
// leave an unreachable generation. The current generation remains active.
func (s *Store) BeginRebuild(ctx context.Context, space string) (string, error) {
	id, e := newID()
	if e != nil {
		return "", e
	}
	e = s.write(ctx, func(tx *sql.Tx) error {
		if e := checkAccess(ctx, tx); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO generations VALUES(?,?,'building')", id, space); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO staging_owners SELECT 'generation',?,value FROM store_meta WHERE key='run_epoch'", id)
		return e
	})
	return id, e
}

func (s *Store) AbandonRebuild(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var active bool
		if e := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM derived_state WHERE generation=?)", id).Scan(&active); e != nil {
			return e
		}
		if active {
			return errors.New("不能丢弃活动世代")
		}
		if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim VALUES(?,'generation','abandoned_rebuild')", id); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "DELETE FROM staging_owners WHERE kind='generation' AND id=?", id)
		return e
	})
}

// InstallRebuildOrganization builds the complete candidate mapping before
// dependency validation, including cycles. Each transaction owns one record.
func (s *Store) InstallRebuildOrganization(ctx context.Context, v OrganizationVersion) error {
	header, e := s.RecordHeader(ctx, v)
	if e != nil {
		return e
	}
	if e := s.boundDelta(ctx); e != nil {
		return e
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if e := checkAccess(ctx, tx); e != nil {
			return e
		}
		var valid bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM organizations o JOIN generations g ON g.id=o.generation JOIN live_assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0 WHERE o.id=? AND o.generation=? AND o.asset=? AND o.revision=? AND o.state='ready' AND g.state='building')`, v.ID, v.Generation, v.Asset, v.Revision).Scan(&valid); e != nil {
			return e
		}
		if !valid {
			return errors.New("重建输入或候选状态已变化")
		}
		for _, q := range []string{
			"UPDATE organizations SET state='active' WHERE id=?",
			"INSERT INTO organization_current SELECT generation,asset,id FROM organizations WHERE id=?",
			"INSERT INTO organization_heads SELECT generation,asset,id FROM organizations WHERE id=?",
			"INSERT INTO organization_publications(organization) VALUES(?)",
			"INSERT OR IGNORE INTO vector_delta SELECT organization,space FROM vectors WHERE organization=?",
			"INSERT OR IGNORE INTO derived_reclaim SELECT space,'vector_pack','rebuild' FROM vectors WHERE organization=?",
		} {
			if _, e := tx.ExecContext(ctx, q, v.ID); e != nil {
				return e
			}
		}
		if !header.HasSemanticResult() {
			_, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO semantic_jobs VALUES(?,?,'rebuild')", v.Asset, v.Revision)
			if e != nil {
				return e
			}
		}
		return nil
	})
}

// FinishRebuild validates incrementally without pinning a corpus-wide read or
// write transaction. The final epoch comparison makes that validation atomic.
func (s *Store) FinishRebuild(ctx context.Context, id string, before RetrievalStamp) error {
	cursor := ""
	for {
		page, e := s.ScanAssets(ctx, cursor, 4096)
		if e != nil {
			return e
		}
		for _, m := range page.Items {
			if _, e = s.CurrentOrganization(ctx, id, m.ID); e != nil {
				return e
			}
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	return contract.Commit(ctx, func() error {
		return s.write(ctx, func(tx *sql.Tx) error {
			if e := checkAccess(ctx, tx); e != nil {
				return e
			}
			var now RetrievalStamp
			if e := tx.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='asset_epoch'").Scan(&now.Assets); e != nil {
				return e
			}
			if e := tx.QueryRowContext(ctx, "SELECT generation,epoch FROM derived_state WHERE singleton=1").Scan(&now.Generation, &now.Derived); e != nil {
				return e
			}
			if now != before {
				return errors.New("重建期间资料或组织已变化，保留原世代")
			}
			var state string
			if e := tx.QueryRowContext(ctx, "SELECT state FROM generations WHERE id=?", id).Scan(&state); e != nil {
				return e
			}
			if state != "building" {
				return errors.New("候选世代不可发布")
			}
			if _, e := tx.ExecContext(ctx, "UPDATE generations SET state='retired' WHERE id=?", before.Generation); e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim VALUES(?,'generation','replaced')", before.Generation); e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, "UPDATE generations SET state='active' WHERE id=?", id); e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, "UPDATE derived_state SET generation=?,epoch=epoch+1 WHERE singleton=1", id); e != nil {
				return e
			}
			_, e := tx.ExecContext(ctx, "DELETE FROM staging_owners WHERE kind='generation' AND id=?", id)
			return e
		})
	})
}
