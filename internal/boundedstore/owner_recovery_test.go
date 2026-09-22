package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
)

func TestSnapshotInterruptionPreservesItsCause(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	for _, queryAfterCancellation := range []bool{false, true} {
		e := s.WithSnapshot(ctx, func(bound context.Context) error {
			s.cancelReaders()
			if !queryAfterCancellation {
				return nil
			}
			return s.view(bound, func(q queryer) error {
				var n int
				return q.QueryRowContext(bound, "SELECT 1").Scan(&n)
			})
		})
		if !errors.Is(e, ErrSnapshotInterrupted) {
			t.Fatal("lost maintenance interruption", queryAfterCancellation, e)
		}
	}
	caller, cancel := context.WithCancel(ctx)
	e := s.WithSnapshot(caller, func(context.Context) error { cancel(); return caller.Err() })
	if !errors.Is(e, context.Canceled) || errors.Is(e, ErrSnapshotInterrupted) {
		t.Fatal("caller cancellation misclassified", e)
	}
	unrelated := errors.New("unrelated storage failure")
	if e := s.WithSnapshot(ctx, func(context.Context) error { return unrelated }); !errors.Is(e, unrelated) {
		t.Fatal("storage failure hidden", e)
	}
}

// checkAccess runs after the internal read transaction has begun. Interrupt it
// at that boundary without cancelling the caller or adding a production hook.
type metadataInterruptionContext struct {
	context.Context
	store *Store
}

func (c metadataInterruptionContext) Value(key any) any {
	if _, ok := key.(accessKey); ok {
		c.store.cancelReaders()
	}
	return c.Context.Value(key)
}

func TestAssetMetadataReportsSnapshotInterruption(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	d := newDraft(t, s, ctx, "metadata interruption")
	a, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "metadata-publication"})
	if e != nil {
		t.Fatal(e)
	}
	if got, e := s.ReadAssetMeta(ctx, a.ID, a.Revision); e != nil || got.ID != a.ID {
		t.Fatal("ordinary metadata read failed", got, e)
	}
	interrupted := metadataInterruptionContext{Context: ctx, store: s}
	if got, e := s.ReadAssetMeta(interrupted, a.ID, a.Revision); !errors.Is(e, ErrSnapshotInterrupted) || got.ID != "" || ctx.Err() != nil {
		t.Fatal("metadata interruption was not reported without a stale result", got, e, ctx.Err())
	}
}

func TestOwnerPermissionConflictAtomicAndRetained(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	stopMaintenance(s)
	principal, _, e := c.Enroll(ctx, "connection")
	if e != nil {
		t.Fatal(e)
	}
	request := contract.ManagementRequest{ID: "conflicting", Operation: "permissions", SubjectID: principal.ID, SubjectRevision: principal.Revision, Permissions: []contract.Permission{contract.ReadPermission}}
	if _, e = c.Propose(ctx, request); e != nil {
		t.Fatal(e)
	}
	if e = c.SetPermissions(ctx, principal.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission}); e != nil {
		t.Fatal(e)
	}
	e = s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `CREATE TRIGGER reject_terminal_event BEFORE INSERT ON owner_events WHEN NEW.status='superseded' BEGIN SELECT RAISE(ABORT,'injected terminal failure'); END`)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.ApplyPermissions(request.ID); e == nil {
		t.Fatal("injected commit failure ignored")
	}
	if receipt, e := c.Receipt(ctx, request.ID); e != nil || receipt.Status != "approved" {
		t.Fatal("partial terminal transition", receipt, e)
	}
	e = s.write(ctx, func(tx *sql.Tx) error { _, e := tx.ExecContext(ctx, "DROP TRIGGER reject_terminal_event"); return e })
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = c.ApplyPermissions(request.ID); e != nil {
			t.Fatal(e)
		}
	}
	if n := countTest(t, s, "SELECT count(*) FROM owner_events WHERE status='superseded'"); n != 1 {
		t.Fatal("terminal transition duplicated", n)
	}
	before, e := s.OwnerCheckpoint(ctx)
	if e != nil || before.VisibleUntil == 0 {
		t.Fatal(before, e)
	}
	rows, _, e := s.OwnerHistory(ctx, "", 100)
	if e != nil || len(rows) != 1 || rows[0].Status != "superseded" {
		t.Fatal(rows, e)
	}
	e = s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "UPDATE owner_events SET at=0; UPDATE owner_operation_times SET updated=0")
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	after, e := s.OwnerCheckpoint(ctx)
	if e != nil || after.VisibleUntil != 0 || after == before {
		t.Fatal("expiry did not change checkpoint", after, e)
	}
	for i := 0; i < 10; i++ {
		more := false
		e = s.write(ctx, func(tx *sql.Tx) error { var e error; more, e = s.expireOwnerHistory(ctx, tx); return e })
		if e != nil {
			t.Fatal(e)
		}
		if !more {
			break
		}
	}
	if n := countTest(t, s, "SELECT count(*) FROM owner_operation_times"); n != 0 {
		t.Fatal("terminal display did not expire", n)
	}
	if receipt, e := c.Receipt(ctx, request.ID); e != nil || receipt.Status != "superseded" {
		t.Fatal("display expiry erased authoritative outcome", receipt, e)
	}
}

func TestOwnerPublicationLookupRequiresOwnerAndPublicationKind(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	d := newDraft(t, s, ctx, "published result")
	if _, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "publication"}); e != nil {
		t.Fatal(e)
	}
	_, agent := workAgent(t, c, ctx)
	for _, invalid := range []context.Context{context.Background(), agent} {
		if _, e := s.OwnerPublication(invalid, "publication"); e == nil {
			t.Fatal("non-owner recovered publication")
		}
	}
	e := s.write(ctx, func(tx *sql.Tx) error {
		a, e := requireOwner(ctx, tx, false)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO operation_receipts VALUES(?,?,?,1,'ownward_create','other','[]')", a.system, a.id, "other-kind")
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if got, e := s.OwnerPublication(ctx, "other-kind"); e != nil || got.State != "unknown" || got.Asset.ID != "" {
		t.Fatal("unrelated operation impersonated publication", got, e)
	}
	// Receipt rotation can precede an attempted publication that never commits.
	// Its visibility change must still invalidate the owner's projection cursor.
	e = s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<4094)
 INSERT INTO operation_receipts SELECT 'seed','seed',cast(i AS TEXT),1,'seed','seed','[]' FROM n`)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	before, e := s.OwnerCheckpoint(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.OperationGeneration(ctx); e != nil {
		t.Fatal(e)
	}
	after, e := s.OwnerCheckpoint(ctx)
	if e != nil || before == after || before.Assets != after.Assets {
		t.Fatal("receipt expiry invisible without an asset write", before, after, e)
	}
	if got, e := s.OwnerPublication(ctx, "publication"); e != nil || got.State != "unknown" {
		t.Fatal(got, e)
	}
}
