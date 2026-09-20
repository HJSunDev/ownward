package organization

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	_ "modernc.org/sqlite"
)

// Journal stores execution identities and costs, never source materials or a
// duplicate work queue. The kernel remains authoritative for pending work.
type Journal struct {
	db     *sql.DB
	client string
}

func OpenJournal(root, client string) (*Journal, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "execution.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=1000; PRAGMA journal_mode=WAL; PRAGMA cache_size=-512; PRAGMA max_page_count=4096;
CREATE TABLE IF NOT EXISTS clients(id TEXT PRIMARY KEY, active INTEGER NOT NULL, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS execution_blocks(scope TEXT NOT NULL,identity TEXT NOT NULL,reason TEXT NOT NULL,PRIMARY KEY(scope,identity)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS executions(scope TEXT NOT NULL,work TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,tokens INTEGER NOT NULL DEFAULT 0,unknown INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL DEFAULT '',usage TEXT NOT NULL DEFAULT '{}',PRIMARY KEY(scope,work)) WITHOUT ROWID;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Journal{db: db, client: client}, nil
}

// DB is intentionally exposed only for compatibility checks and migrations;
// normal runtime operations use the typed methods below.
func (j *Journal) DB() *sql.DB { return j.db }

func (j *Journal) Heartbeat(ctx context.Context, active bool) error {
	if _, err := j.db.ExecContext(ctx, `DELETE FROM clients WHERE expires<?`, time.Now().UnixMilli()); err != nil {
		return err
	}
	_, err := j.db.ExecContext(ctx, `INSERT INTO clients VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET active=excluded.active,expires=excluded.expires`, j.client, active, time.Now().Add(15*time.Second).UnixMilli())
	return err
}

func (j *Journal) Foreground(ctx context.Context) (bool, error) {
	var active bool
	err := j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM clients WHERE active=1 AND expires>?)`, time.Now().UnixMilli()).Scan(&active)
	return active, err
}

func (j *Journal) Begin(ctx context.Context, scope, work string, p contract.OrganizationExecutionPolicy) (bool, int64, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO executions(scope,work) VALUES(?,?)`, scope, work); err != nil {
		return false, 0, err
	}
	var attempts, unknown int
	var tokens int64
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT attempts,tokens,unknown,status FROM executions WHERE scope=? AND work=?`, scope, work).Scan(&attempts, &tokens, &unknown, &status); err != nil {
		return false, 0, err
	}
	if attempts >= p.MaxAttempts || tokens >= p.MaxTokens {
		return false, tokens, nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE executions SET attempts=attempts+1,unknown=max(unknown,?),status='running' WHERE scope=? AND work=?`, status == "running", scope, work); err != nil {
		return false, tokens, err
	}
	return true, tokens, tx.Commit()
}

func (j *Journal) Save(ctx context.Context, scope, work, status string, prior int64, usage contract.OrganizationUsage) error {
	b, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	_, err = j.db.ExecContext(ctx, `UPDATE executions SET tokens=?,unknown=max(unknown,?),status=?,usage=? WHERE scope=? AND work=?`, prior+usage.TotalTokens, status != "running" && usage.UsageIncomplete, status, string(b), scope, work)
	if err == nil && status == "accepted" {
		_, err = j.db.ExecContext(ctx, `DELETE FROM executions WHERE status='accepted' AND (scope,work) IN (SELECT scope,work FROM executions WHERE status='accepted' ORDER BY scope,work LIMIT -1 OFFSET 256)`)
	}
	return err
}

func (j *Journal) DeleteExecution(ctx context.Context, scope, work string) error {
	_, err := j.db.ExecContext(ctx, `DELETE FROM executions WHERE scope=? AND work=?`, scope, work)
	return err
}

func (j *Journal) Block(ctx context.Context, scope, identity, reason string) error {
	_, err := j.db.ExecContext(ctx, `INSERT INTO execution_blocks VALUES(?,?,?) ON CONFLICT(scope,identity) DO UPDATE SET reason=excluded.reason`, scope, identity, reason)
	return err
}

func (j *Journal) BlockReason(ctx context.Context, scope, identity string) (string, error) {
	var reason string
	err := j.db.QueryRowContext(ctx, `SELECT reason FROM execution_blocks WHERE scope=? AND identity=?`, scope, identity).Scan(&reason)
	return reason, err
}

func (j *Journal) Close() {
	if j == nil {
		return
	}
	_, _ = j.db.Exec(`DELETE FROM clients WHERE id=? OR expires<?`, j.client, time.Now().UnixMilli())
	_ = j.db.Close()
}
