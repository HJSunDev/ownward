package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

type workClass int

const (
	controlWork workClass = iota
	maintenanceWork
	ordinaryWork
)

type workClassKey struct{}
type foregroundKey struct{}
type snapshotWriteKey struct{}

// BeginForeground orders whole workspaces before their memory reservations.
// A pending maintenance batch gets a turn when the active product call ends.
func (s *Store) BeginForeground(ctx context.Context) (context.Context, func(), error) {
	if e := s.workAdmission.acquire(ctx, ordinaryWork); e != nil {
		return nil, nil, e
	}
	var once sync.Once
	return context.WithValue(ctx, foregroundKey{}, s), func() { once.Do(s.workAdmission.Unlock) }, nil
}

func workContext(ctx context.Context, class workClass) context.Context {
	return context.WithValue(ctx, workClassKey{}, class)
}
func classOf(ctx context.Context) workClass {
	if c, ok := ctx.Value(workClassKey{}).(workClass); ok {
		return c
	}
	return ordinaryWork
}

// Controls take the next transaction; maintenance and ordinary writers alternate
// when both are waiting. No waiter holds a database transaction.
type writeGate struct {
	mu      sync.Mutex
	busy    bool
	waiting [3]int
	last    workClass
	changed chan struct{}
}

func (g *writeGate) acquire(ctx context.Context, class workClass) error {
	g.mu.Lock()
	if g.changed == nil {
		g.changed = make(chan struct{})
	}
	g.waiting[class]++
	defer func() { g.waiting[class]--; g.mu.Unlock() }()
	for {
		if err := ctx.Err(); err != nil {
			g.signal()
			return err
		}
		allowed := !g.busy
		if class != controlWork && g.waiting[controlWork] > 0 {
			allowed = false
		}
		if class == ordinaryWork && g.waiting[maintenanceWork] > 0 && g.last != maintenanceWork {
			allowed = false
		}
		if class == maintenanceWork && g.waiting[ordinaryWork] > 0 && g.last == maintenanceWork {
			allowed = false
		}
		if allowed {
			g.busy = true
			// 控制插队不改变普通写入与维护的轮换历史。
			if class != controlWork {
				g.last = class
			}
			return nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		g.mu.Lock()
	}
}
func (g *writeGate) signal() { close(g.changed); g.changed = make(chan struct{}) }
func (g *writeGate) Lock()   { _ = g.acquire(context.Background(), ordinaryWork) }
func (g *writeGate) Unlock() { g.mu.Lock(); g.busy = false; g.signal(); g.mu.Unlock() }

const (
	walPassive  = 8 * resourcebudget.MiB
	walOrdinary = 24 * resourcebudget.MiB
	walControl  = 32 * resourcebudget.MiB
	// Reserve for a <=1 MiB batch plus measured B-tree/overflow-page writes.
	walTransactionReserve = 2 * resourcebudget.MiB
)

type readConnection struct {
	*sql.Conn
	ctx context.Context
}

func (c *readConnection) BeginTx(_ context.Context, o *sql.TxOptions) (*sql.Tx, error) {
	return c.Conn.BeginTx(c.ctx, o)
}
func (c *readConnection) QueryContext(_ context.Context, q string, a ...any) (*sql.Rows, error) {
	return c.Conn.QueryContext(c.ctx, q, a...)
}
func (c *readConnection) QueryRowContext(_ context.Context, q string, a ...any) *sql.Row {
	return c.Conn.QueryRowContext(c.ctx, q, a...)
}
func (c *readConnection) ExecContext(_ context.Context, q string, a ...any) (sql.Result, error) {
	return c.Conn.ExecContext(c.ctx, q, a...)
}

func (s *Store) walBytes() int64 {
	i, e := os.Stat(s.path + "-wal")
	if e != nil {
		return 0
	}
	return i.Size()
}
func (s *Store) signalReadersLocked() { close(s.readerChanged); s.readerChanged = make(chan struct{}) }
func (s *Store) setDraining(v bool) {
	s.readerMu.Lock()
	if s.draining != v {
		s.draining = v
		s.signalReadersLocked()
	}
	s.readerMu.Unlock()
}

type readerLease struct {
	cancel  context.CancelFunc
	control bool
}

func (s *Store) cancelReaders(all ...bool) {
	s.readerMu.Lock()
	defer s.readerMu.Unlock()
	for _, lease := range s.activeReaders {
		if !lease.control || (len(all) > 0 && all[0]) {
			lease.cancel()
		}
	}
}
func (s *Store) checkpointLocked(ctx context.Context, mode string) (bool, error) {
	var busy, log, done int
	// Do not wait inside SQLite while owning the control write slot.
	if _, e := s.writer.ExecContext(ctx, "PRAGMA busy_timeout=0"); e != nil {
		return false, e
	}
	e := s.writer.QueryRowContext(ctx, "PRAGMA wal_checkpoint("+mode+")").Scan(&busy, &log, &done)
	_, restore := s.writer.ExecContext(context.WithoutCancel(ctx), "PRAGMA busy_timeout=1000")
	return busy == 0 && (log < 0 || log == done), errors.Join(e, restore)
}

// admitWrite returns with writeMu held. Read leases are cancellable only when
// the control reserve is exhausted, never on periodic PASSIVE checkpoints.
func (s *Store) admitWrite(ctx context.Context) error {
	class := classOf(ctx)
	for {
		if e := s.writeMu.acquire(ctx, class); e != nil {
			return e
		}
		s.readerMu.Lock()
		frozen, freezeChanged := s.copyFrozen, s.readerChanged
		s.readerMu.Unlock()
		if frozen && class != controlWork {
			s.writeMu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-freezeChanged:
			}
			continue
		}
		wal := s.walBytes()
		limit := int64(walOrdinary)
		if class == controlWork {
			limit = walControl
		}
		if wal+walTransactionReserve < limit {
			return nil
		}
		s.setDraining(true)
		if class == controlWork {
			s.cancelReaders()
		}
		s.readerMu.Lock()
		changed := s.readerChanged
		s.readerMu.Unlock()
		ok, e := s.checkpointLocked(ctx, "TRUNCATE")
		if e != nil {
			s.writeMu.Unlock()
			return e
		}
		if ok {
			s.setDraining(false)
			return nil
		}
		// An in-snapshot repair hint may not wait on its own snapshot.
		_, inside := ctx.Value(snapshotKey{}).(snapshot)
		if inside || ctx.Value(snapshotWriteKey{}) != nil {
			s.writeMu.Unlock()
			return errors.New("旧读快照阻止维护，请结束当前读取后接续")
		}
		s.writeMu.Unlock()
		if class == controlWork {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-changed:
			case <-time.After(10 * time.Millisecond):
			}
		} else {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-changed:
			}
		}
	}
}

func (s *Store) afterWrite(ctx context.Context) {
	if classOf(ctx) != maintenanceWork {
		s.wakeMaintenance()
	}
}
