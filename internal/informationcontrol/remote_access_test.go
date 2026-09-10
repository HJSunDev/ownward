package informationcontrol

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/contract"
)

func accessControl(t *testing.T) (*Control, context.Context, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	a, err := authoritysubstrate.Open(dir, contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	c := New(a.Control())
	token, err := c.InitializeOwner("owner")
	if err != nil {
		t.Fatal(err)
	}
	return c, Authenticate(context.Background(), token), dir
}

func TestRemoteEnrollmentRequiresTargetProofAndSeparateManager(t *testing.T) {
	c, owner, _ := accessControl(t)
	e, err := c.Invite(owner, "invitation")
	if err != nil {
		t.Fatal(err)
	}
	proof := strings.Repeat("secret", 8)
	_, err = c.Join(e.ID, proof, "same name", []contract.Permission{contract.ReadPermission})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(e.ID, strings.Repeat("other", 8), "same name", []contract.Permission{contract.ReadPermission}); err == nil {
		t.Fatal("same display name captured a different connection")
	}
	if _, err := c.DecideEnrollment(Authenticate(context.Background(), proof), e.ID, EnrollmentMarker(e.ID, proof), true); err == nil {
		t.Fatal("claim proof gained manager permission")
	}
	if _, token, err := c.ClaimEnrollment(e.ID, proof); err != nil || token != "" {
		t.Fatal("unapproved connection got credential", err)
	}
	if _, err := c.DecideEnrollment(owner, e.ID, EnrollmentMarker(e.ID, proof), true); err != nil {
		t.Fatal(err)
	}
	first, token, err := c.ClaimEnrollment(e.ID, proof)
	if err != nil || token == "" {
		t.Fatal(err)
	}
	second, replacement, err := c.ClaimEnrollment(e.ID, proof)
	if err != nil || first.Principal != second.Principal || token == replacement {
		t.Fatal("claim resume made another identity", err)
	}
	if _, err := c.Self(Authenticate(context.Background(), token)); err == nil {
		t.Fatal("lost credential still usable after reissue")
	}
	ctx := Authenticate(context.Background(), replacement)
	if err := c.AcknowledgeEnrollment(ctx, e.ID, proof); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPermissions(owner, first.Principal, nil); err != nil {
		t.Fatal(err)
	}
	if _, token, err := c.ClaimEnrollment(e.ID, proof); err != nil || token != "" {
		t.Fatal("claim restored revoked credential", err)
	}
}

func TestFrozenMigrationYieldsToAuthorizedRevocationAtomically(t *testing.T) {
	c, owner, _ := accessControl(t)
	p, token, err := c.Enroll(owner, "reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetPermissions(owner, p.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission}); err != nil {
		t.Fatal(err)
	}
	target := contract.Location{ServiceID: "destination", SystemID: c.SystemID(), Endpoint: "https://example.test", Certificate: "test", Composition: "test"}
	if _, err := c.PrepareHandoff(owner, "migration", target); err != nil {
		t.Fatal(err)
	}
	h, err := c.FreezeHandoff(owner, "migration", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Begin(Authenticate(context.Background(), token), contract.MaintainPermission); !errors.Is(err, ErrMoving) {
		t.Fatal("frozen writes allowed", err)
	}
	r := contract.ManagementRequest{ID: "revoke", Operation: "permissions", SubjectID: p.ID}
	_, err = c.Propose(Authenticate(context.Background(), token), r)
	if err != nil {
		t.Fatal(err)
	}
	if c.State().Revision != h.Revision {
		t.Fatal("unapproved proposal invalidated snapshot")
	}
	if _, err := c.Decide(owner, r.ID, true); err != nil {
		t.Fatal(err)
	}
	if c.State().Revision != h.Revision {
		t.Fatal("confirmation mutated frozen snapshot independently")
	}
	if err := c.ApplyPermissions(r.ID); err != nil {
		t.Fatal(err)
	}
	s := c.State()
	if s.Revision != h.Revision+1 || s.Access.Handoff != nil || len(s.Access.Cancelled) != 1 {
		t.Fatal("revocation and cancellation not atomic", s.Revision)
	}
	if _, err := c.RetireHandoff(owner, h.ID, strings.Repeat("a", 64), h.Revision); err == nil {
		t.Fatal("cancelled migration retired source")
	}
	if _, _, err := c.Begin(Authenticate(context.Background(), token), contract.ReadPermission); !errors.Is(err, ErrDenied) {
		t.Fatal("revoked reader remained authorized", err)
	}
}

func TestRetirementRejectsOldCallsAndSurvivesReload(t *testing.T) {
	c, owner, dir := accessControl(t)
	target := contract.Location{ServiceID: "target", SystemID: c.SystemID(), Endpoint: "https://example.test", Certificate: "test", Composition: "test"}
	if _, err := c.PrepareHandoff(owner, "move", target); err != nil {
		t.Fatal(err)
	}
	h, err := c.FreezeHandoff(owner, "move", true)
	if err != nil {
		t.Fatal(err)
	}
	_, finish, err := c.Begin(owner, contract.ReadPermission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RetireHandoff(owner, h.ID, strings.Repeat("a", 64), h.Revision); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(finish(), ErrDenied) && !errors.Is(finish(), ErrInactive) {
		t.Fatal("old delivery survived retirement")
	}
	if _, err := c.Propose(owner, contract.ManagementRequest{ID: "forget", Operation: "forget", Targets: []contract.AssetVersion{{ID: "x", Revision: 1}}}); !errors.Is(err, ErrInactive) {
		t.Fatal("retired source accepted control decision", err)
	}
	state, err := authoritysubstrate.ReadControlAt(dir)
	if err != nil || state.Access.Handoff.Phase != "retired" {
		t.Fatal("retirement not durable", err)
	}
}
