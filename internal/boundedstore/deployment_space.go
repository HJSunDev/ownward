package boundedstore

import (
	"context"
	"database/sql"
	"errors"
)

// InitializeDeploymentSpace is restricted to a closed migration gate. It can
// attach the current model to originals that did not yet have organization.
func (s *Store) InitializeDeploymentSpace(ctx context.Context, space string) error {
	var active bool
	if e := s.writer.QueryRowContext(ctx, "SELECT activated FROM storage_migration").Scan(&active); e != nil {
		return e
	}
	if active {
		return errors.New("活动存储不能隐式更换向量空间")
	}
	_, current, e := s.Generation(ctx)
	if e == nil {
		if current != space {
			return errors.New("已保存向量空间与运行时不一致，需要已验证的兼容转换")
		}
		return nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	id, e := newID()
	if e != nil {
		return e
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "INSERT INTO generations VALUES(?,?,'active')", id, space); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO derived_state VALUES(1,?,1)", id)
		return e
	})
}
