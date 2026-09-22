package boundedstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/HJSunDev/ownward/internal/contract"
)

func storageCredential(id, digest string) string {
	if digest == "" {
		return "invalidated:" + id
	}
	return digest
}

func (s *Store) invalidateRestoreCredentials(ctx context.Context) error {
	after := ""
	for {
		var id string
		var b []byte
		e := s.writer.QueryRowContext(ctx, "SELECT id,data FROM authority_items WHERE kind='principals' AND id>? ORDER BY id LIMIT 1", after).Scan(&id, &b)
		if errors.Is(e, sql.ErrNoRows) {
			break
		}
		if e != nil {
			return e
		}
		var p contract.Principal
		if e = json.Unmarshal(b, &p); e != nil {
			return e
		}
		p.Revision++
		p.CredentialDigest = ""
		b, e = json.Marshal(p)
		if e != nil {
			return e
		}
		e = s.write(ctx, func(tx *sql.Tx) error {
			if e := controlItem(ctx, tx, "principals", id, "", b); e != nil {
				return e
			}
			_, e := tx.ExecContext(ctx, "UPDATE access_principals SET credential=?,revision=? WHERE id=?", storageCredential(id, ""), p.Revision, id)
			return e
		})
		if e != nil {
			return e
		}
		after = id
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		// A historical backup preserves private work, never active work grants.
		if _, e := tx.ExecContext(ctx, "DELETE FROM owner_draft_grants"); e != nil {
			return e
		}
		var b []byte
		if e := tx.QueryRowContext(ctx, "SELECT data FROM authority_header").Scan(&b); e != nil {
			return e
		}
		var h contract.ControlState
		if e := json.Unmarshal(b, &h); e != nil {
			return e
		}
		h.Revision++
		if h.InformationControl != nil {
			h.InformationControl.DeletionRevision++
		}
		b, e := json.Marshal(h)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE authority_header SET data=?", b); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "UPDATE access_header SET revision=revision+1,deletion_epoch=deletion_epoch+1")
		return e
	})
}
