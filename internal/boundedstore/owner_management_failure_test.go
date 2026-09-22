package boundedstore

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

// Exercise the product's control orchestration against the real SQLite
// authority. The streaming kernel delegates this step to the same callback.
type authorityForgetKernel struct{ contract.ProductCapability }

func (authorityForgetKernel) StopUsing(targets, _ []contract.AssetVersion, _ string, persist func([]contract.AssetVersion) error) error {
	return persist(targets)
}
func (authorityForgetKernel) CleanForgotten() error { return nil }

func TestOwnerForgetConflictPreservesAtomicFailureAndRetry(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	stopMaintenance(s)
	d := newDraft(t, s, ctx, "current content")
	a, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "first"})
	if e != nil {
		t.Fatal(e)
	}
	other := newDraft(t, s, ctx, "also retain this")
	if _, e = s.PublishDraft(ctx, other.ID, other.Revision, contract.OperationIdentity{ID: "second"}); e != nil {
		t.Fatal(e)
	}
	rows, _, e := s.OwnerAssets(ctx, "", "", "", "", 10)
	if e != nil || len(rows) != 2 {
		t.Fatal(rows, e)
	}
	// A definitely stale version, not a transient storage or authorization error.
	// The first target is valid and staged before the second target conflicts.
	request := contract.ManagementRequest{ID: "stale", Operation: "forget", Targets: []contract.AssetVersion{{ID: rows[0].Meta.ID, Revision: rows[0].Meta.Revision}, {ID: rows[1].Meta.ID, Revision: rows[1].Meta.Revision + 1}}}
	p := informationcontrol.NewProduct(authorityForgetKernel{}, c)
	defer p.Close()
	changeSQL := func(query string) {
		t.Helper()
		if e := s.write(ctx, func(tx *sql.Tx) error { _, e := tx.ExecContext(ctx, query); return e }); e != nil {
			t.Fatal(e)
		}
	}
	for _, fault := range []string{"storage", "terminal"} {
		trigger := `CREATE TRIGGER fail_attempt BEFORE INSERT ON forget_operations BEGIN SELECT RAISE(ABORT,'injected storage failure'); END`
		if fault == "terminal" {
			trigger = `CREATE TRIGGER fail_attempt BEFORE INSERT ON owner_events WHEN NEW.status='superseded' BEGIN SELECT RAISE(ABORT,'injected terminal failure'); END`
		}
		changeSQL(trigger)
		if _, e = p.Manage(ctx, request); e == nil || !strings.Contains(e.Error(), "injected "+fault+" failure") {
			t.Fatal("failure was hidden or misclassified", fault, e)
		}
		if op, e := c.Receipt(ctx, request.ID); e != nil || op.Status != "approved" {
			t.Fatal("failed transaction left a false terminal result", op, e)
		}
		if countTest(t, s, "SELECT count(*) FROM forget_operations") != 0 || countTest(t, s, "SELECT count(*) FROM forget_targets") != 0 || countTest(t, s, "SELECT count(*) FROM owner_events WHERE status='superseded'") != 0 {
			t.Fatal("failed transaction left partial deletion or terminal evidence")
		}
		changeSQL("DROP TRIGGER fail_attempt")
	}
	before, e := s.OwnerCheckpoint(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if op, e := p.Manage(ctx, request); e != nil || op.Status != "superseded" {
			t.Fatal("retry did not close obsolete request", op, e)
		}
	}
	after, e := s.OwnerCheckpoint(ctx)
	if e != nil || before.Deletion != after.Deletion {
		t.Fatal("terminal close advanced deletion", before, after, e)
	}
	if countTest(t, s, "SELECT count(*) FROM forget_operations") != 0 || countTest(t, s, "SELECT count(*) FROM owner_events WHERE status='superseded'") != 1 {
		t.Fatal("terminal close did not preserve one result without deletion")
	}
	if got, e := s.ReadAssetMeta(ctx, a.ID, a.Revision); e != nil || got.ID != a.ID {
		t.Fatal("current asset lost", got, e)
	}
}
