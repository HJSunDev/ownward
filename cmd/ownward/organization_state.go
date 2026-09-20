package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/HJSunDev/ownward/internal/codexplugin"
	_ "modernc.org/sqlite"
)

// This journal stores execution identities and costs, never source materials or a
// duplicate work queue. The kernel alone determines pending work and completion.
type organizationJournal struct {
	db     *sql.DB
	client string
}

func openOrganizationJournal(root string) (*organizationJournal, error) {
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", filepath.Join(root, "execution.sqlite"))
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA busy_timeout=1000; PRAGMA journal_mode=WAL; PRAGMA cache_size=-512; PRAGMA max_page_count=4096;
CREATE TABLE IF NOT EXISTS clients(id TEXT PRIMARY KEY, active INTEGER NOT NULL, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS executions(scope TEXT NOT NULL,work TEXT NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,tokens INTEGER NOT NULL DEFAULT 0,unknown INTEGER NOT NULL DEFAULT 0,status TEXT NOT NULL DEFAULT '',usage TEXT NOT NULL DEFAULT '{}',PRIMARY KEY(scope,work)) WITHOUT ROWID;`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return &organizationJournal{db: db, client: connectionID()}, nil
}
func (j *organizationJournal) heartbeat(ctx context.Context, active bool) error {
	if _, e := j.db.ExecContext(ctx, `DELETE FROM clients WHERE expires<?`, time.Now().UnixMilli()); e != nil {
		return e
	}
	_, e := j.db.ExecContext(ctx, `INSERT INTO clients VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET active=excluded.active,expires=excluded.expires`, j.client, active, time.Now().Add(15*time.Second).UnixMilli())
	return e
}
func (j *organizationJournal) foreground(ctx context.Context) (bool, error) {
	var v bool
	e := j.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM clients WHERE active=1 AND expires>?)`, time.Now().UnixMilli()).Scan(&v)
	return v, e
}
func (j *organizationJournal) begin(ctx context.Context, scope, work string, p codexplugin.OrganizationProfile) (bool, int64, error) {
	tx, e := j.db.BeginTx(ctx, nil)
	if e != nil {
		return false, 0, e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO executions(scope,work) VALUES(?,?)`, scope, work)
	if e != nil {
		return false, 0, e
	}
	var attempts, unknown int
	var tokens int64
	var status string
	e = tx.QueryRowContext(ctx, `SELECT attempts,tokens,unknown,status FROM executions WHERE scope=? AND work=?`, scope, work).Scan(&attempts, &tokens, &unknown, &status)
	if e != nil {
		return false, 0, e
	}
	if attempts >= p.MaxAttempts || tokens >= p.MaxTokens {
		return false, tokens, nil
	}
	_, e = tx.ExecContext(ctx, `UPDATE executions SET attempts=attempts+1,unknown=max(unknown,?),status='running' WHERE scope=? AND work=?`, status == "running", scope, work)
	if e != nil {
		return false, tokens, e
	}
	return true, tokens, tx.Commit()
}
func (j *organizationJournal) save(ctx context.Context, scope, work, status string, prior int64, u codexplugin.OrganizationUsage) error {
	b, e := json.Marshal(u)
	if e != nil {
		return e
	}
	_, e = j.db.ExecContext(ctx, `UPDATE executions SET tokens=?,unknown=max(unknown,?),status=?,usage=? WHERE scope=? AND work=?`, prior+u.TotalTokens, status != "running" && u.UsageIncomplete, status, string(b), scope, work)
	if e == nil && status == "accepted" {
		_, e = j.db.ExecContext(ctx, `DELETE FROM executions WHERE status='accepted' AND (scope,work) IN (SELECT scope,work FROM executions WHERE status='accepted' ORDER BY scope,work LIMIT -1 OFFSET 256)`)
	}
	return e
}
func (j *organizationJournal) close() {
	if j == nil {
		return
	}
	j.db.Exec(`DELETE FROM clients WHERE id=? OR expires<?`, j.client, time.Now().UnixMilli())
	j.db.Close()
}
