package main

import (
	"context"
	"database/sql"

	"github.com/HJSunDev/ownward/internal/contract"
	organizationruntime "github.com/HJSunDev/ownward/internal/organization"
)

// organizationJournal is a compatibility facade for the connector package.
// The durable implementation belongs to internal/organization so any host can
// reuse the same journal without importing the Codex adapter.
type organizationJournal struct {
	*organizationruntime.Journal
	db *sql.DB // retained for package-local migration/status tests
}

func openOrganizationJournal(root string) (*organizationJournal, error) {
	j, err := organizationruntime.OpenJournal(root, connectionID())
	if err != nil {
		return nil, err
	}
	return &organizationJournal{Journal: j, db: j.DB()}, nil
}

func (j *organizationJournal) heartbeat(ctx context.Context, active bool) error {
	return j.Heartbeat(ctx, active)
}

func (j *organizationJournal) foreground(ctx context.Context) (bool, error) {
	return j.Foreground(ctx)
}

func (j *organizationJournal) begin(ctx context.Context, scope, work string, p contract.OrganizationExecutionPolicy) (bool, int64, error) {
	return j.Begin(ctx, scope, work, p)
}

func (j *organizationJournal) save(ctx context.Context, scope, work, status string, prior int64, usage contract.OrganizationUsage) error {
	return j.Save(ctx, scope, work, status, prior, usage)
}

func (j *organizationJournal) close() { j.Close() }
