package boundedstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func organizationJobFixture(t *testing.T) (*Store, context.Context, contract.Principal) {
	t.Helper()
	s := openTest(t, filepath.Join(t.TempDir(), "ownward.sqlite"))
	p := contract.Principal{ID: "principal", Revision: 1, CredentialDigest: strings.Repeat("a", 64), Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission, contract.ManagePermission}}
	if e := s.PublishAccess(context.Background(), AccessHeader{System: "system", Revision: 1}, 0, []contract.Principal{p}); e != nil {
		t.Fatal(e)
	}
	ctx, e := s.BeginAccess(context.Background(), p.CredentialDigest, contract.MaintainPermission)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.InitializeGeneration(ctx, "test-space"); e != nil {
		t.Fatal(e)
	}
	return s, ctx, p
}

func putOrganizationJob(t *testing.T, s *Store, ctx context.Context, id string, revision uint64) {
	t.Helper()
	op := operation(id + time.Now().String())
	v := stageTest(t, s, op, id, "original "+id, revision)
	v.DeferOrganization = true
	if e := s.Publish(ctx, receipt(op, v), []AssetWrite{v}); e != nil {
		t.Fatal(e)
	}
}

func TestOrganizationLeaseExclusiveRenewExpiryAndRestart(t *testing.T) {
	s, ctx, p := organizationJobFixture(t)
	putOrganizationJob(t, s, ctx, "one", 1)
	var wg sync.WaitGroup
	results := make(chan *contract.OrganizationLease, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, e := s.ClaimOrganization(ctx, "one", string(rune('a'+i)), 60)
			results <- v
			failures <- e
		}(i)
	}
	wg.Wait()
	close(results)
	close(failures)
	var claim *contract.OrganizationLease
	n := 0
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	for v := range results {
		if v != nil {
			n++
			claim = v
		}
	}
	if n != 1 {
		t.Fatalf("%d simultaneous owners", n)
	}
	if _, e := s.ChangeOrganizationLease(ctx, "one", "wrong", 60, false); !errors.Is(e, ErrOrganizationLease) {
		t.Fatal("wrong token renewed", e)
	}
	renew, e := s.ChangeOrganizationLease(ctx, "one", claim.Lease, 120, false)
	if e != nil || !renew.ExpiresAt.After(claim.ExpiresAt) {
		t.Fatal("renew", e)
	}
	path := s.path
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s = openTest(t, path)
	ctx, e = s.BeginAccess(context.Background(), p.CredentialDigest, contract.MaintainPermission)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CheckOrganizationExecution(WithOrganizationExecution(ctx, "one", claim.Lease), "one"); e != nil {
		t.Fatal("restart lost lease", e)
	}
	if readTest(t, s, "one") != "original one" {
		t.Fatal("restart lost original")
	}
	if e = s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "UPDATE organization_execution SET expires=1 WHERE asset='one'")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckOrganizationExecution(WithOrganizationExecution(ctx, "one", claim.Lease), "one"); !errors.Is(e, ErrOrganizationLease) {
		t.Fatal("expired lease accepted", e)
	}
	next, e := s.ClaimOrganization(ctx, "one", "restart", 60)
	if e != nil || next == nil || next.Lease == claim.Lease {
		t.Fatal("expiry recovery", e)
	}
}

func TestOrganizationWaitReleaseRevocationCorrectionAndForget(t *testing.T) {
	s, ctx, p := organizationJobFixture(t)
	done := make(chan error, 1)
	go func() {
		ok, e := s.WaitOrganization(ctx, "one", 3)
		if e == nil && !ok {
			e = errors.New("lost publication wakeup")
		}
		done <- e
	}()
	putOrganizationJob(t, s, ctx, "one", 1)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimOrganization(ctx, "one", "first", 60)
	if e != nil || c == nil {
		t.Fatal(e)
	}
	go func() {
		ok, e := s.WaitOrganization(ctx, "one", 3)
		if e == nil && !ok {
			e = errors.New("lost release wakeup")
		}
		done <- e
	}()
	if _, e = s.ChangeOrganizationLease(ctx, "one", c.Lease, 60, true); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	c, e = s.ClaimOrganization(ctx, "one", "second", 60)
	if e != nil || c == nil {
		t.Fatal(e)
	}
	putOrganizationJob(t, s, ctx, "one", 2)
	if e = s.CheckOrganizationExecution(WithOrganizationExecution(ctx, "one", c.Lease), "one"); !errors.Is(e, ErrOrganizationLease) {
		t.Fatal("correction retained old executor", e)
	}
	c, e = s.ClaimOrganization(ctx, "one", "third", 60)
	if e != nil || c == nil {
		t.Fatal(e)
	}
	p.Revision = 2
	p.Permissions = nil
	if e = s.PublishAccess(context.Background(), AccessHeader{System: "system", Revision: 2}, 1, []contract.Principal{p}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ChangeOrganizationLease(ctx, "one", c.Lease, 60, true); e == nil {
		t.Fatal("revoked lease released")
	}
	p.Revision = 3
	p.Permissions = []contract.Permission{contract.ReadPermission, contract.MaintainPermission, contract.ManagePermission}
	if e = s.PublishAccess(context.Background(), AccessHeader{System: "system", Revision: 3}, 2, []contract.Principal{p}); e != nil {
		t.Fatal(e)
	}
	ctx, e = s.BeginAccess(context.Background(), p.CredentialDigest, contract.MaintainPermission)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CheckOrganizationExecution(WithOrganizationExecution(ctx, "one", c.Lease), "one"); !errors.Is(e, ErrOrganizationLease) {
		t.Fatal("reauthorization revived lease", e)
	}
	c, e = s.ClaimOrganization(ctx, "one", "fourth", 60)
	if e != nil || c == nil {
		t.Fatal("revoked owner blocked fresh claim", e)
	}
	manage, e := s.BeginAccess(context.Background(), p.CredentialDigest, contract.ManagePermission)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.StageForget(manage, "forget", []ForgetTarget{{ID: "one", Revision: 2}}); e != nil {
		t.Fatal(e)
	}
	if e = s.CommitForget(manage, "forget"); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckOrganizationExecution(WithOrganizationExecution(ctx, "one", c.Lease), "one"); e == nil {
		t.Fatal("forgotten executor survived")
	}
}

func TestOrganizationJobUpgradePreservesLegacyData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ownward.sqlite")
	s := openTest(t, path)
	op := operation("old")
	v := stageTest(t, s, op, "legacy", "original legacy", 1)
	if e := s.Publish(context.Background(), receipt(op, v), []AssetWrite{v}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.writer.ExecContext(context.Background(), "DROP TABLE organization_execution; UPDATE store_meta SET value=3 WHERE key='format'"); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	s = openTest(t, path)
	if readTest(t, s, "legacy") != "original legacy" {
		t.Fatal("upgrade altered original")
	}
	jobs, e := s.PendingAssets(context.Background(), 20)
	if e != nil || len(jobs) != 1 || jobs[0] != "legacy" {
		t.Fatal("upgrade changed legacy pending", jobs, e)
	}
	if _, found, e := s.MutationReceipt(context.Background(), op); e != nil || !found {
		t.Fatal("upgrade lost receipt", e)
	}
	var version int
	if e = s.view(context.Background(), func(q queryer) error {
		return q.QueryRowContext(context.Background(), "SELECT value FROM store_meta WHERE key='format'").Scan(&version)
	}); e != nil || version != schemaVersion {
		t.Fatal("upgrade marker", version, e)
	}
}

func TestOrganizationWaitExpiryWithoutWriterAndCancellation(t *testing.T) {
	s, ctx, _ := organizationJobFixture(t)
	putOrganizationJob(t, s, ctx, "one", 1)
	if c, e := s.ClaimOrganization(ctx, "one", "short", 1); e != nil || c == nil {
		t.Fatal(e)
	}
	if ok, e := s.WaitOrganization(ctx, "one", 3); e != nil || !ok {
		t.Fatal("expiry did not wake waiter", e)
	}
	if c, e := s.ClaimOrganization(ctx, "one", "next", 60); e != nil || c == nil {
		t.Fatal(e)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := s.WaitOrganization(cancelCtx, "one", 30); e == nil {
		t.Fatal("cancel ignored")
	}
}

func TestOrganizationPublicationRechecksLeaseAndGeneration(t *testing.T) {
	s, ctx, _ := organizationJobFixture(t)
	putOrganizationJob(t, s, ctx, "one", 1)
	c, e := s.ClaimOrganization(ctx, "one", "first", 60)
	if e != nil || c == nil {
		t.Fatal(e)
	}
	guard := WithOrganizationExecution(ctx, "one", c.Lease)
	r := derived.Record{AssetID: "one", AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "original one"}}
	data, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	staged, e := s.StageOrganization(guard, c.Generation, StringSource(data))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ChangeOrganizationLease(ctx, "one", c.Lease, 60, true); e != nil {
		t.Fatal(e)
	}
	next, e := s.ClaimOrganization(ctx, "one", "second", 60)
	if e != nil || next == nil {
		t.Fatal(e)
	}
	if e = s.PublishOrganization(guard, staged); !errors.Is(e, ErrOrganizationLease) {
		t.Fatal("publication accepted revoked holder", e)
	}
	if _, e = s.CurrentOrganization(ctx, c.Generation, "one"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("rejected organization became visible", e)
	}
	if e = s.CreateGeneration(ctx, "replacement", "test-space"); e != nil {
		t.Fatal(e)
	}
	replacement, e := s.StageOrganization(ctx, "replacement", StringSource(data))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.InstallRebuildOrganization(ctx, replacement); e != nil {
		t.Fatal(e)
	}
	if e = s.ActivateGeneration(ctx, "replacement", c.Generation); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckOrganizationExecution(WithOrganizationExecution(ctx, "one", next.Lease), "one"); !errors.Is(e, ErrOrganizationLease) {
		t.Fatal("old generation holder survived", e)
	}
	current, e := s.ClaimOrganization(ctx, "one", "third", 60)
	if e != nil || current == nil || current.Generation != "replacement" {
		t.Fatal("new generation could not recover work", e)
	}
}

func TestOrganizationLeaseCannotBeUsedByAnotherPrincipal(t *testing.T) {
	s, ctx, _ := organizationJobFixture(t)
	putOrganizationJob(t, s, ctx, "one", 1)
	c, e := s.ClaimOrganization(ctx, "one", "first", 60)
	if e != nil || c == nil {
		t.Fatal(e)
	}
	p := contract.Principal{ID: "other", Revision: 1, CredentialDigest: strings.Repeat("b", 64), Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission}}
	if e = s.PublishAccess(context.Background(), AccessHeader{System: "system", Revision: 2}, 1, []contract.Principal{p}); e != nil {
		t.Fatal(e)
	}
	other, e := s.BeginAccess(context.Background(), p.CredentialDigest, contract.MaintainPermission)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CheckOrganizationExecution(WithOrganizationExecution(other, "one", c.Lease), "one"); !errors.Is(e, ErrOrganizationLease) {
		t.Fatal("cross-principal execution accepted", e)
	}
	if _, e = s.ChangeOrganizationLease(other, "one", c.Lease, 60, true); !errors.Is(e, ErrOrganizationLease) {
		t.Fatal("cross-principal release accepted", e)
	}
	if v, e := s.ClaimOrganization(other, "one", "first", 60); e != nil || v != nil {
		t.Fatal("cross-principal claim recovered private token", e)
	}
}

func TestOrganizationBackgroundLimitFairnessAndRevokedReservation(t *testing.T) {
	s, ctx, p := organizationJobFixture(t)
	putOrganizationJob(t, s, ctx, "a", 1)
	putOrganizationJob(t, s, ctx, "b", 1)
	first, e := s.ClaimOrganizationAfter(ctx, "", "first", 60, "", true)
	if e != nil || first == nil || first.AssetID != "a" {
		t.Fatal(first, e)
	}
	if duplicate, e := s.ClaimOrganizationAfter(ctx, "b", "parallel", 60, "", true); e != nil || duplicate != nil {
		t.Fatal("background cap bypassed", duplicate, e)
	}
	if duplicate, e := s.ClaimOrganization(ctx, "a", "foreground", 60); e != nil || duplicate != nil {
		t.Fatal("foreground duplicated running work", duplicate, e)
	}
	if _, e = s.ChangeOrganizationLease(ctx, "a", first.Lease, 60, true); e != nil {
		t.Fatal(e)
	}
	next, e := s.ClaimOrganizationAfter(ctx, "", "next", 60, "a", true)
	if e != nil || next == nil || next.AssetID != "b" {
		t.Fatal("failed source starves later job", next, e)
	}
	p.Revision++
	if e = s.PublishAccess(context.Background(), AccessHeader{System: "system", Revision: 2}, 1, []contract.Principal{p}); e != nil {
		t.Fatal(e)
	}
	fresh, e := s.BeginAccess(context.Background(), p.CredentialDigest, contract.MaintainPermission)
	if e != nil {
		t.Fatal(e)
	}
	reclaimed, e := s.ClaimOrganizationAfter(fresh, "a", "reconnect", 60, "", true)
	if e != nil || reclaimed == nil {
		t.Fatal("stale principal occupies global background reservation", e)
	}
	if e = s.CheckOrganizationExecution(WithOrganizationExecution(fresh, "b", next.Lease), "b"); !errors.Is(e, ErrOrganizationLease) {
		t.Fatal("revoked work remains valid", e)
	}
}

func TestOrganizationUpgradeV4RetainsPendingWork(t *testing.T) {
	s, ctx, p := organizationJobFixture(t)
	putOrganizationJob(t, s, ctx, "retained", 1)
	if e := s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "ALTER TABLE organization_execution DROP COLUMN background; UPDATE store_meta SET value=4 WHERE key='format'")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	path := s.path
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	s = openTest(t, path)
	ctx, e := s.BeginAccess(context.Background(), p.CredentialDigest, contract.MaintainPermission)
	if e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimOrganizationAfter(ctx, "retained", "upgraded", 60, "", true)
	if e != nil || c == nil || readTest(t, s, "retained") != "original retained" {
		t.Fatal("upgrade lost work", c, e)
	}
}
