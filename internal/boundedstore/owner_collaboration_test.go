package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

func TestCollaborationActivePagesExcludeExpiredAndRevokedAuthority(t *testing.T) {
	for _, change := range []string{"expired", "requester", "approver"} {
		t.Run(change, func(t *testing.T) {
			s, c, owner := ownerFixture(t)
			manager, token, e := c.Enroll(owner, "Manager")
			if e != nil {
				t.Fatal(e)
			}
			if e = c.SetPermissions(owner, manager.ID, []contract.Permission{contract.ManagePermission}); e != nil {
				t.Fatal(e)
			}
			main := informationcontrol.Authenticate(context.Background(), token)
			person, token, e := c.Enroll(owner, "Helper")
			if e != nil {
				t.Fatal(e)
			}
			helper := informationcontrol.Authenticate(context.Background(), token)
			d := newDraft(t, s, owner, "draft")
			inv, e := s.InviteDraft(owner, d.ID, d.Revision)
			if e != nil {
				t.Fatal(e)
			}
			r, e := s.RequestDraft(helper, inv.ID)
			if e != nil {
				t.Fatal(e)
			}
			if change == "approver" {
				if _, e = s.DecideDraftCollaboration(main, r.ID, r.Revision, true); e != nil {
					t.Fatal(e)
				}
			}
			before, _, e := s.DraftCollaborations(owner, d.ID, "", 1, "active", "")
			if e != nil || len(before) != 1 {
				t.Fatal("fixture", e)
			}
			switch change {
			case "expired":
				e = s.write(owner, func(tx *sql.Tx) error {
					_, e := tx.Exec("UPDATE owner_draft_requests SET expires=0 WHERE id=?", r.ID)
					return e
				})
			case "requester":
				e = c.SetPermissions(owner, person.ID, []contract.Permission{contract.ReadPermission})
			case "approver":
				e = c.SetPermissions(owner, manager.ID, nil)
			}
			if e != nil {
				t.Fatal(e)
			}
			for _, state := range []string{"active", "awaiting_approval"} {
				rows, next, e := s.DraftCollaborations(owner, d.ID, "", 1, state, "")
				if e != nil || len(rows) != 0 || next != "" {
					t.Fatalf("%s includes invalid authority: %d %v", state, len(rows), e)
				}
			}
		})
	}
}

func TestDelegationIssuerCheckedDuringStreamAndCommit(t *testing.T) {
	s, c, owner := ownerFixture(t)
	manager, token, e := c.Enroll(owner, "Manager")
	if e != nil {
		t.Fatal(e)
	}
	if e = c.SetPermissions(owner, manager.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission, contract.ManagePermission}); e != nil {
		t.Fatal(e)
	}
	main := informationcontrol.Authenticate(context.Background(), token)
	_, token, e = c.Enroll(owner, "Collaborator")
	if e != nil {
		t.Fatal(e)
	}
	helper := informationcontrol.Authenticate(context.Background(), token)
	d := newDraft(t, s, main, "private draft")
	inv, e := s.InviteDraft(main, d.ID, d.Revision)
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.RequestDraft(helper, inv.ID)
	if e != nil {
		t.Fatal(e)
	}
	r, e = s.DecideDraftCollaboration(main, r.ID, r.Revision, true)
	if e != nil {
		t.Fatal(e)
	}
	_, stream, e := s.ReadDraft(helper, d.ID, r.Grant)
	if e != nil {
		t.Fatal(e)
	}
	defer stream.Close()
	// Revocation happens after the read was opened and while replacement input
	// is being staged. Neither the stream nor the commit can use the old issuer.
	_, e = s.WriteDraft(helper, contract.DraftWrite{ID: d.ID, ExpectedRevision: d.Revision, GrantID: r.Grant, Content: contentFunc(func(context.Context) (io.ReadCloser, error) {
		if e := c.SetPermissions(owner, manager.ID, nil); e != nil {
			return nil, e
		}
		return StringSource("must not commit").Open(context.Background())
	})})
	if !errors.Is(e, ErrAccess) {
		t.Fatal("write accepted after issuer revoke", e)
	}
	if _, e = io.ReadAll(stream); !errors.Is(e, ErrAccess) {
		t.Fatal("stream accepted after issuer revoke", e)
	}
	if got := draftText(t, s, owner, d.ID, ""); got != "private draft" {
		t.Fatal(got)
	}
}

func TestDraftCollaborationApprovalIsolationAndRevocation(t *testing.T) {
	s, c, owner := ownerFixture(t)
	d := newDraft(t, s, owner, "Original")
	inv, e := s.InviteDraft(owner, d.ID, d.Revision)
	if e != nil {
		t.Fatal(e)
	}
	retry, e := s.InviteDraft(owner, d.ID, d.Revision)
	if e != nil || retry.ID != inv.ID {
		t.Fatalf("invitation retry: %+v %v", retry, e)
	}
	p, token, e := c.Enroll(owner, "Writer")
	if e != nil {
		t.Fatal(e)
	}
	a := informationcontrol.Authenticate(context.Background(), token)
	_, otherToken, e := c.Enroll(owner, "Other")
	if e != nil {
		t.Fatal(e)
	}
	other := informationcontrol.Authenticate(context.Background(), otherToken)
	v, e := s.RequestDraft(a, inv.ID)
	if e != nil || v.State != "awaiting_approval" {
		t.Fatalf("request: %+v %v", v, e)
	}
	repeated, e := s.RequestDraft(a, inv.ID)
	if e != nil || v.ID != repeated.ID {
		t.Fatal("duplicate request", e)
	}
	if _, e = s.DraftMetadata(a, d.ID, inv.ID); !errors.Is(e, ErrAccess) {
		t.Fatal("invitation grants access", e)
	}
	if _, e = s.DraftCollaboration(other, v.ID); !errors.Is(e, ErrAccess) {
		t.Fatal("other subject saw request", e)
	}
	if _, e = s.DecideDraftCollaboration(a, v.ID, v.Revision, true); !errors.Is(e, ErrAccess) {
		t.Fatal("agent approved itself", e)
	}
	if _, e = s.DecideDraftCollaboration(owner, v.ID, v.Revision+1, true); !errors.Is(e, contract.ErrOwnerRefresh) {
		t.Fatal("stale decision", e)
	}
	v, e = s.DecideDraftCollaboration(owner, v.ID, v.Revision, true)
	if e != nil || v.State != "approved" {
		t.Fatal(v, e)
	}
	grant := v.Grant
	if got := draftText(t, s, a, d.ID, grant); got != "Original" {
		t.Fatal(got)
	}
	if _, e = s.DraftMetadata(other, d.ID, grant); !errors.Is(e, ErrAccess) {
		t.Fatal("grant transferred", e)
	}
	if self, e := c.Self(a); e != nil || len(self.Permissions) != 0 {
		t.Fatal("whole library permission granted", self, e)
	}
	if _, e = s.WriteDraft(a, contract.DraftWrite{ID: d.ID, ExpectedRevision: d.Revision, GrantID: grant, Append: true, Content: StringSource(" edited")}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.PublishDraft(a, d.ID, d.Revision+1, contract.OperationIdentity{ID: "agent-publish"}); !errors.Is(e, ErrAccess) {
		t.Fatal("agent published", e)
	}
	if e = s.RevokeDraftGrant(owner, grant); e != nil {
		t.Fatal(e)
	}
	v, e = s.RequestDraft(a, inv.ID)
	if e != nil || v.State != "ended" || v.Grant != "" {
		t.Fatal("revoked request revived", v, e)
	}
	if _, e = s.WriteDraft(a, contract.DraftWrite{ID: d.ID, ExpectedRevision: d.Revision + 1, GrantID: grant, Content: StringSource("bad")}); !errors.Is(e, ErrAccess) {
		t.Fatal(e)
	}
	if got := draftText(t, s, owner, d.ID, ""); got != "Original edited" {
		t.Fatal(got)
	}
	if p.ID == "" {
		t.Fatal("missing principal")
	}
}

func TestDraftCollaborationExpiryIdentityAndLifecycle(t *testing.T) {
	for _, action := range []string{"decline", "cancel", "cancel_invitation", "expire", "pending_expire", "principal", "owner", "restore", "discard", "publish", "forget"} {
		t.Run(action, func(t *testing.T) {
			s, c, owner := ownerFixture(t)
			d := newDraft(t, s, owner, "private")
			if action == "forget" {
				asset, e := s.PublishDraft(owner, d.ID, d.Revision, contract.OperationIdentity{ID: "source-before-collaboration"})
				if e != nil {
					t.Fatal(e)
				}
				d, e = s.CreateDraft(owner, contract.DraftInput{Target: asset})
				if e != nil {
					t.Fatal(e)
				}
			}
			inv, e := s.InviteDraft(owner, d.ID, d.Revision)
			if e != nil {
				t.Fatal(e)
			}
			p, token, e := c.Enroll(owner, "Writer")
			if e != nil {
				t.Fatal(e)
			}
			a := informationcontrol.Authenticate(context.Background(), token)
			v, e := s.RequestDraft(a, inv.ID)
			if e != nil {
				t.Fatal(e)
			}
			rev := v.Revision
			if action != "decline" && action != "principal" && action != "owner" && action != "pending_expire" {
				v, e = s.DecideDraftCollaboration(owner, v.ID, rev, true)
				if e != nil {
					t.Fatal(e)
				}
			}
			switch action {
			case "decline":
				_, e = s.DecideDraftCollaboration(owner, v.ID, rev, false)
			case "cancel":
				e = s.EndDraftCollaboration(owner, v.ID)
			case "cancel_invitation":
				e = s.CancelDraftInvitation(owner, inv.ID)
			case "expire", "pending_expire":
				_, e = s.writer.ExecContext(owner, "UPDATE owner_draft_requests SET expires=? WHERE id=?", time.Now().Add(-time.Second).UnixMilli(), v.ID)
			case "principal":
				e = c.SetPermissions(owner, p.ID, []contract.Permission{contract.ReadPermission})
			case "owner":
				_, e = c.RecoverOwner()
			case "restore":
				e = s.invalidateRestoreCredentials(owner)
			case "discard":
				e = s.DiscardDraft(owner, d.ID, d.Revision)
			case "publish":
				_, e = s.PublishDraft(owner, d.ID, d.Revision, contract.OperationIdentity{ID: "publish-collaboration"})
			case "forget":
				e = s.StageForget(owner, "forget-collaboration", []ForgetTarget{{d.Target.ID, d.Target.Revision}})
				if e == nil {
					e = s.CommitForget(owner, "forget-collaboration")
				}
			}
			if e != nil {
				t.Fatal(e)
			}
			got, e := s.DraftCollaboration(a, v.ID)
			if e == nil && (got.State == "approved" || got.State == "awaiting_approval") {
				t.Fatal("remained usable", got)
			}
			if action == "decline" || action == "cancel" {
				late, e := s.DecideDraftCollaboration(owner, v.ID, rev, true)
				if e != nil || late.State == "approved" {
					t.Fatal("late decision restored access", late, e)
				}
				fresh, e := s.InviteDraft(owner, d.ID, d.Revision)
				if e != nil || fresh.ID == inv.ID {
					t.Fatal("cannot restart", fresh, e)
				}
			}
		})
	}
}
