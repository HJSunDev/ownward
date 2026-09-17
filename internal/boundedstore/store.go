package boundedstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	_ "modernc.org/sqlite"
)

const ChunkBytes = 64 * 1024
const schemaVersion = 3

type Options struct {
	Budget   *resourcebudget.Budget
	LockPath string
	paused   bool      // Restore drains control cleanup before background admission.
	lease    io.Closer // Deployment already owns the shared legacy lock.
}

// Store 不读取旧日志；旧格式迁移与启用由交付阶段控制。
type Store struct {
	db                *sql.DB
	writer            *sql.Conn
	readers           chan *sql.Conn
	writeMu           writeGate
	vectorMu          sync.Mutex
	organizationMu    sync.Mutex
	writeFailure      error
	mu                sync.RWMutex
	closed            bool
	budget            *resourcebudget.Budget
	closeOnce         sync.Once
	closeErr          error
	lock              io.Closer
	directory         string
	path              string
	readerMu          sync.Mutex
	readerChanged     chan struct{}
	activeReaders     map[*sql.Conn]readerLease
	draining          bool
	maintenanceMu     sync.Mutex
	maintenanceBatch  writeGate
	workAdmission     writeGate
	copyMu            sync.Mutex
	copyFrozen        bool
	maintenanceWake   chan struct{}
	maintenanceCancel context.CancelFunc
	maintenanceDone   chan struct{}
	maintenanceErr    error
	runEpoch          int64
	releaseCache      func()
}

const schema = `
CREATE TABLE IF NOT EXISTS store_meta(key TEXT PRIMARY KEY, value INTEGER NOT NULL) WITHOUT ROWID;
INSERT OR IGNORE INTO store_meta VALUES('format',3),('operation_generation',1),('asset_epoch',1);
CREATE TABLE IF NOT EXISTS payloads(
 id TEXT PRIMARY KEY, operation TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('staging','ready','published')),
 content_bytes INTEGER NOT NULL DEFAULT 0, details_bytes INTEGER NOT NULL DEFAULT 0, digest TEXT NOT NULL DEFAULT '',
 CHECK(content_bytes>=0 AND details_bytes>=0)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS content_chunks(
 payload TEXT NOT NULL REFERENCES payloads(id), part INTEGER NOT NULL, ordinal INTEGER NOT NULL,
 bytes BLOB NOT NULL CHECK(length(bytes)<=65536), PRIMARY KEY(payload,part,ordinal)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS assets(
 id TEXT PRIMARY KEY, revision INTEGER NOT NULL, created TEXT NOT NULL, updated TEXT NOT NULL,
 kind TEXT NOT NULL, payload TEXT NOT NULL REFERENCES payloads(id), deleted INTEGER NOT NULL DEFAULT 0,
 CHECK(revision>0)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS operation_receipts(
 system TEXT NOT NULL, principal TEXT NOT NULL, id TEXT NOT NULL, generation INTEGER NOT NULL,
 kind TEXT NOT NULL, digest TEXT NOT NULL, results BLOB NOT NULL,
 PRIMARY KEY(system,principal,id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS receipt_generation ON operation_receipts(generation);
CREATE TABLE IF NOT EXISTS control_records(
 key TEXT PRIMARY KEY, revision INTEGER NOT NULL, payload TEXT NOT NULL REFERENCES payloads(id)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS semantic_jobs(
 asset TEXT PRIMARY KEY REFERENCES assets(id), revision INTEGER NOT NULL, reason TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS reclaim_jobs(
 payload TEXT PRIMARY KEY REFERENCES payloads(id), reason TEXT NOT NULL) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS pending_payloads ON payloads(state,operation,id);
CREATE TABLE IF NOT EXISTS access_header(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), system TEXT NOT NULL, revision INTEGER NOT NULL,
 deletion_epoch INTEGER NOT NULL, frozen INTEGER NOT NULL, retired INTEGER NOT NULL, stopping INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS access_principals(
 id TEXT PRIMARY KEY, revision INTEGER NOT NULL, credential TEXT NOT NULL UNIQUE,
 permissions INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS explicit_links(
 payload TEXT NOT NULL REFERENCES payloads(id), ordinal INTEGER NOT NULL,target TEXT NOT NULL,
 qualifies INTEGER NOT NULL,start_rune INTEGER NOT NULL,end_rune INTEGER NOT NULL,
 PRIMARY KEY(payload,ordinal)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS explicit_target ON explicit_links(target,payload,ordinal);
CREATE TABLE IF NOT EXISTS source_epochs(id TEXT PRIMARY KEY, revision INTEGER NOT NULL) WITHOUT ROWID;
`

func Open(ctx context.Context, path string, options Options) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("数据库路径必须为绝对路径")
	}
	if options.Budget == nil {
		return nil, errors.New("缺少共享工作区预算")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if options.LockPath == "" {
		options.LockPath = filepath.Join(filepath.Dir(path), ".ownward.lock")
	}
	if !filepath.IsAbs(options.LockPath) {
		return nil, errors.New("存储锁路径必须为绝对路径")
	}
	if err := os.MkdirAll(filepath.Dir(options.LockPath), 0700); err != nil {
		return nil, err
	}
	lock := options.lease
	if lock == nil {
		var err error
		lock, err = assetlog.LockDirectory(options.LockPath)
		if err != nil {
			return nil, err
		}
	}
	if err := resourcebudget.RecoverScratch(filepath.Dir(path)); err != nil {
		lock.Close()
		return nil, err
	}
	releaseCache, err := options.Budget.Acquire(ctx, 4*resourcebudget.MiB+128*1024, false)
	if err != nil {
		lock.Close()
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.ToSlash(path))
	if err != nil {
		lock.Close()
		releaseCache()
		return nil, err
	}
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(3)
	s := &Store{db: db, readers: make(chan *sql.Conn, 2), budget: options.Budget, lock: lock, releaseCache: releaseCache, directory: filepath.Dir(path), path: path, readerChanged: make(chan struct{}), activeReaders: map[*sql.Conn]readerLease{}, maintenanceWake: make(chan struct{}, 1)}
	fail := func(err error) (*Store, error) { s.Close(); return nil, err }
	s.writer, err = db.Conn(ctx)
	if err != nil {
		return fail(err)
	}
	var tableCount int
	if err = s.writer.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tableCount); err != nil {
		return fail(err)
	}
	if tableCount > 0 {
		var version int
		if err = s.writer.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='format'").Scan(&version); err != nil {
			return fail(err)
		}
		if version < 1 || version > schemaVersion {
			return fail(errors.New("不支持的数据库格式"))
		}
	}
	// 页大小与增量回收必须在新库写入表结构前确定。
	if _, err = s.writer.ExecContext(ctx, "PRAGMA page_size=16384; PRAGMA auto_vacuum=INCREMENTAL;"); err != nil {
		return fail(err)
	}
	var pageSize, vacuum int
	if err = s.writer.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return fail(err)
	}
	if err = s.writer.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&vacuum); err != nil {
		return fail(err)
	}
	if pageSize != 16384 || vacuum != 2 {
		return fail(errors.New("数据库物理格式不匹配"))
	}
	if err = configure(ctx, s.writer, 2048, false); err != nil {
		return fail(err)
	}
	if err = s.upgradeLexicalStorage(ctx); err != nil {
		return fail(err)
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, schema+retrievalSchema+maintenanceSchema); err != nil {
		tx.Rollback()
		return fail(err)
	}
	// Publish the new format before exposing any stop-use barrier. Old binaries
	// must reject it rather than bypassing the new visibility view.
	if _, err = tx.ExecContext(ctx, "UPDATE store_meta SET value=3 WHERE key='format'"); err != nil {
		tx.Rollback()
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	if err = s.initializeMaintenance(ctx); err != nil {
		return fail(err)
	}
	var version int
	if err = s.writer.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='format'").Scan(&version); err != nil {
		return fail(err)
	}
	if version != schemaVersion {
		return fail(errors.New("不支持的数据库格式"))
	}
	for i := 0; i < 2; i++ {
		conn, e := db.Conn(ctx)
		if e != nil {
			return fail(e)
		}
		if e = configure(ctx, conn, 1024, true); e != nil {
			conn.Close()
			return fail(e)
		}
		s.readers <- conn
	}
	if !options.paused {
		s.startMaintenance()
	}
	return s, nil
}

func configure(ctx context.Context, c *sql.Conn, cacheKB int, readOnly bool) error {
	_, err := c.ExecContext(ctx, fmt.Sprintf("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA secure_delete=ON; PRAGMA foreign_keys=ON; PRAGMA automatic_index=OFF; PRAGMA wal_autocheckpoint=0; PRAGMA temp_store=FILE; PRAGMA mmap_size=0; PRAGMA cache_size=-%d; PRAGMA busy_timeout=1000;", cacheKB))
	if err != nil {
		return err
	}
	if readOnly {
		// Query execution writes only connection-local temporary tables. Main
		// database mutations remain exclusively behind Store.write.
		_, err = c.ExecContext(ctx, "PRAGMA temp.cache_size=-128;")
	} else {
		_, err = c.ExecContext(ctx, "PRAGMA temp.cache_size=-128; CREATE TEMP TABLE IF NOT EXISTS work_keys(scope TEXT NOT NULL,digest BLOB NOT NULL,PRIMARY KEY(scope,digest)) WITHOUT ROWID;")
	}
	return err
}

func (s *Store) reader(ctx context.Context) (*readConnection, func(), error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, nil, errors.New("存储已关闭")
	}
	for {
		s.readerMu.Lock()
		blocked := s.draining && classOf(ctx) == ordinaryWork
		changed := s.readerChanged
		s.readerMu.Unlock()
		if blocked {
			select {
			case <-ctx.Done():
				s.mu.RUnlock()
				return nil, nil, ctx.Err()
			case <-changed:
			}
			continue
		}
		select {
		case c := <-s.readers:
			s.readerMu.Lock()
			if s.draining && classOf(ctx) == ordinaryWork {
				s.readerMu.Unlock()
				s.readers <- c
				continue
			}
			readCtx, cancel := context.WithCancel(ctx)
			s.activeReaders[c] = readerLease{cancel: cancel, control: classOf(ctx) == controlWork}
			s.readerMu.Unlock()
			var once sync.Once
			return &readConnection{c, readCtx}, func() {
				once.Do(func() {
					cancel()
					s.readerMu.Lock()
					delete(s.activeReaders, c)
					s.signalReadersLocked()
					s.readerMu.Unlock()
					s.readers <- c
					s.mu.RUnlock()
					if classOf(ctx) != maintenanceWork && s.walBytes() >= walPassive {
						s.wakeMaintenance()
					}
				})
			}, nil
		case <-ctx.Done():
			s.mu.RUnlock()
			return nil, nil, ctx.Err()
		}
	}
}

func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errors.New("存储已关闭")
	}
	if err := s.admitWrite(ctx); err != nil {
		return err
	}
	defer s.writeMu.Unlock()
	if s.writeFailure != nil {
		return s.writeFailure
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Writer lifetime is owned here; cancellation is checked by each statement
	// and before commit, so rollback finishes before the connection is reused.
	tx, err := s.writer.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		s.afterWrite(ctx)
		return nil
	}
	// 提交结果不确定时废弃物理连接；原操作依靠耐久回执接续。
	_ = s.writer.Raw(func(any) error { return driver.ErrBadConn })
	_ = s.writer.Close()
	s.writer, s.writeFailure = s.db.Conn(context.WithoutCancel(ctx))
	if s.writeFailure == nil {
		s.writeFailure = configure(context.WithoutCancel(ctx), s.writer, 2048, false)
	}
	return errors.Join(errors.New("提交结果未确认，请使用原操作身份核对回执后接续"), err, s.writeFailure)
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		if s.maintenanceCancel != nil {
			s.maintenanceCancel()
			s.cancelReaders(true)
			<-s.maintenanceDone
		}
		s.setDraining(false)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.closed = true
		for len(s.readers) > 0 {
			if err := (<-s.readers).Close(); err != nil {
				s.closeErr = errors.Join(s.closeErr, err)
			}
		}
		if s.writer != nil {
			s.closeErr = errors.Join(s.closeErr, s.writer.Close())
		}
		if s.db != nil {
			s.closeErr = errors.Join(s.closeErr, s.db.Close())
		}
		if s.lock != nil {
			s.closeErr = errors.Join(s.closeErr, s.lock.Close())
		}
		if s.releaseCache != nil {
			s.releaseCache()
		}
	})
	return s.closeErr
}
