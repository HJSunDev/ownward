package boundedstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func publishDependentOrganizationForTest(t *testing.T, s *Store, ctx context.Context, generation string, r derived.Record) OrganizationVersion {
	t.Helper()
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.StageOrganization(ctx, generation, StringSource(b))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.PublishOrganization(ctx, v); e != nil {
		t.Fatal(e)
	}
	return v
}

func TestCompletedOrganizationReleasesClaimForDependencyCorrection(t *testing.T) {
	s, ctx, _ := organizationJobFixture(t)
	putOrganizationJob(t, s, ctx, "source", 1)
	putOrganizationJob(t, s, ctx, "consumer", 1)
	c, e := s.ClaimOrganization(ctx, "consumer", "first", 300)
	if e != nil || c == nil {
		t.Fatal(e)
	}
	guard := WithOrganizationExecution(ctx, "consumer", c.Lease)
	publishDependentOrganizationForTest(t, s, guard, c.Generation, derived.Record{AssetID: "consumer", AssetRevision: 1, Status: "ready", InputsKnown: true, InputAssets: []semantics.CandidateReference{{ID: "source", Revision: 1}}, Analysis: semantics.Analysis{Summary: "consumer depends on source"}})
	if e = s.DrainMaintenance(ctx); e != nil {
		t.Fatal(e)
	}
	var pending int
	if e = s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT count(*) FROM semantic_jobs WHERE asset='consumer'").Scan(&pending)
	}); e != nil || pending != 0 {
		t.Fatalf("completed pending=%d err=%v", pending, e)
	}
	putOrganizationJob(t, s, ctx, "source", 2)
	if e = s.DrainMaintenance(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = s.CurrentOrganization(ctx, c.Generation, "consumer"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatalf("stale organization still visible: %v", e)
	}
	if e = s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT count(*) FROM semantic_jobs WHERE asset='consumer'").Scan(&pending)
	}); e != nil || pending != 1 {
		t.Fatalf("requeued pending=%d err=%v", pending, e)
	}
	if e = s.CheckOrganizationExecution(guard, "consumer"); !errors.Is(e, ErrOrganizationLease) {
		t.Fatal("completed holder can still execute", e)
	}
	if e = s.CheckOrganizationReplay(guard, "consumer"); !errors.Is(e, ErrOrganizationLease) {
		t.Fatal("requeued work accepts completed replay", e)
	}
	ready, e := s.WaitOrganization(ctx, "consumer", 0)
	if e != nil || !ready {
		t.Fatal("new work not immediately available", ready, e)
	}
	fresh, e := s.ClaimOrganization(ctx, "consumer", "second", 300)
	if e != nil {
		t.Fatal(e)
	}
	available, e := s.WaitOrganization(ctx, "consumer", 0)
	if e != nil {
		t.Fatal(e)
	}
	oldValid := s.CheckOrganizationExecution(guard, "consumer") == nil
	t.Logf("requeued=%d, fresh_claim=%v, wait_available=%v, completed_holder_still_valid=%v, remaining_seconds=%.1f", pending, fresh != nil, available, oldValid, time.Until(c.ExpiresAt).Seconds())
	if fresh == nil {
		t.Fatal("completed execution still blocks a new dependency-invalidated organization job")
	}
}
