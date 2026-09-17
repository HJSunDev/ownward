package boundedstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

func TestSelectedControlRevocationEnrollmentAndDurability(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "store.sqlite"), deploymentOptions().Options)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, e := s.OpenControlAuthority(context.Background(), contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "composition", ActiveKernelGeneration: "kernel"})
	if e != nil {
		t.Fatal(e)
	}
	c := informationcontrol.New(a)
	owner, e := c.InitializeOwner("Owner")
	if e != nil {
		t.Fatal(e)
	}
	ctx := informationcontrol.Authenticate(context.Background(), owner)
	p, token, e := c.Enroll(ctx, "Agent")
	if e != nil {
		t.Fatal(e)
	}
	if e = c.SetPermissions(ctx, p.ID, []contract.Permission{contract.ReadPermission}); e != nil {
		t.Fatal(e)
	}
	agent := informationcontrol.Authenticate(context.Background(), token)
	_, finish, e := c.Begin(agent, contract.ReadPermission)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.SetPermissions(ctx, p.ID, nil); e != nil {
		t.Fatal(e)
	}
	if e = finish(); e == nil {
		t.Fatal("revoked lease survived")
	}
	if _, e = s.BeginAccess(agent, contract.AuthenticationDigest(agent), contract.ReadPermission); e == nil {
		t.Fatal("SQL authority ignored revocation")
	}
	op, e := c.Propose(ctx, contract.ManagementRequest{ID: "grant", Operation: "permissions", SubjectID: p.ID, Permissions: []contract.Permission{contract.ReadPermission}})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.ApplyPermissions(op.Request.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Self(agent); e != nil {
		t.Fatal(e)
	}
	invitation, e := c.Invite(ctx, "invitation")
	if e != nil {
		t.Fatal(e)
	}
	proof := "test-only-enrollment-proof-with-more-than-thirty-two-characters"
	if _, e = c.Join(invitation.ID, proof, "Remote", []contract.Permission{contract.ReadPermission}); e != nil {
		t.Fatal(e)
	}
	_, marker, e := c.EnrollmentPreview(ctx, invitation.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.DecideEnrollment(ctx, invitation.ID, marker, true); e != nil {
		t.Fatal(e)
	}
	joined, newToken, e := c.ClaimEnrollment(invitation.ID, proof)
	if e != nil || newToken == "" || joined.Principal == "" {
		t.Fatal("claim failed", e)
	}
	if _, e = c.Self(informationcontrol.Authenticate(context.Background(), newToken)); e != nil {
		t.Fatal(e)
	}
	var buf bytes.Buffer
	if e = a.ExportControl(context.Background(), &buf); e != nil {
		t.Fatal(e)
	}
	var all contract.ControlState
	if e = json.Unmarshal(buf.Bytes(), &all); e != nil {
		t.Fatal(e)
	}
	if e = all.Validate(); e != nil {
		t.Fatal(e)
	}
	if len(all.InformationControl.Principals) != 3 || len(all.InformationControl.Operations) != 1 {
		t.Fatal("scoped commit lost other records")
	}
	h := a.ReadControl()
	if len(h.InformationControl.Operations) != 0 {
		t.Fatal("startup loaded operation history")
	}
}

func TestSelectedControlForgetCommitAndChangedTarget(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	legacyFixture(t, root)
	s, e := OpenDeployment(ctx, root, deploymentOptions())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	a, e := s.OpenControlAuthority(ctx, contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "c", ActiveKernelGeneration: "k"})
	if e != nil {
		t.Fatal(e)
	}
	c := informationcontrol.New(a)
	token, e := c.InitializeOwner("Owner")
	if e != nil {
		t.Fatal(e)
	}
	ctx = informationcontrol.Authenticate(ctx, token)
	for _, rev := range []uint64{3, 4} {
		id := fmt.Sprintf("forget-%d", rev)
		targets := []contract.AssetVersion{{ID: "asset", Revision: rev}}
		if _, e = c.Propose(ctx, contract.ManagementRequest{ID: id, Operation: "forget", Targets: targets}); e != nil {
			t.Fatal(e)
		}
		e = c.StartForget(id, targets)
		if rev == 3 {
			if e == nil {
				t.Fatal("accepted stale target")
			}
			receipt, e := c.Receipt(ctx, id)
			if e != nil || receipt.Status != "approved" {
				t.Fatal("failed target blocked authority", e)
			}
			if _, e = s.ReadAssetMeta(context.Background(), "asset", 0); e != nil {
				t.Fatal(e)
			}
		} else {
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.ReadAssetMeta(context.Background(), "asset", 0); !errors.Is(e, ErrNotFound) {
				t.Fatal("forgotten original remains visible", e)
			}
		}
	}
}
