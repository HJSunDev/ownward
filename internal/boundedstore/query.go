package boundedstore

import (
	"context"
	"database/sql"
	"errors"
)

type queryer interface {
	querier
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}
type snapshotKey struct{}
type snapshot struct {
	store *Store
	tx    *sql.Tx
}

var ErrSnapshotInterrupted = errors.New("读取被存储维护中断，请重新读取")

// WithSnapshot scopes all reads in one tool to the same database version.
// No model or network call may run while fn owns this snapshot.
func (s *Store) WithSnapshot(ctx context.Context, fn func(context.Context) error) (err error) {
	if v, ok := ctx.Value(snapshotKey{}).(snapshot); ok {
		if v.store != s {
			return errors.New("读快照属于其他资料库")
		}
		return fn(ctx)
	}
	c, done, err := s.reader(ctx)
	if err != nil {
		return err
	}
	defer done()
	// Forget and WAL pressure cancel the internal read lease, not the caller.
	// database/sql may then return ErrTxDone instead of context.Canceled. Keep
	// the interruption's origin explicit, including a cancellation at delivery.
	defer func() {
		if ctx.Err() == nil && c.ctx.Err() != nil {
			err = ErrSnapshotInterrupted
		}
	}()
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = checkAccess(ctx, tx); err != nil {
		return err
	}
	return fn(context.WithValue(ctx, snapshotKey{}, snapshot{s, tx}))
}
func (s *Store) view(ctx context.Context, fn func(queryer) error) error {
	return s.WithSnapshot(ctx, func(ctx context.Context) error { return fn(ctx.Value(snapshotKey{}).(snapshot).tx) })
}

func (s *Store) snapshotReader(ctx context.Context) (queryer, func(), error) {
	if v, ok := ctx.Value(snapshotKey{}).(snapshot); ok {
		if v.store != s {
			return nil, nil, errors.New("读快照属于其他资料库")
		}
		return v.tx, func() {}, nil
	}
	c, done, err := s.reader(ctx)
	if err != nil {
		return nil, nil, err
	}
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		done()
		return nil, nil, err
	}
	finish := func() { tx.Rollback(); done() }
	if err = checkAccess(ctx, tx); err != nil {
		finish()
		return nil, nil, err
	}
	return tx, finish, nil
}
