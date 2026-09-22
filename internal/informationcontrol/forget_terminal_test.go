package informationcontrol

import (
	"context"
	"errors"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
)

type terminalGuardAuthority struct {
	state contract.ControlState
	wrote bool
}

func TestManagementLifecyclePreservesAuthorityReadFailures(t *testing.T) {
	failure := errors.New("injected management authority failure")
	a := &terminalGuardAuthority{state: contract.ControlState{ReadError: failure}}
	c := New(a)
	checks := map[string]func() error{
		"execute_permissions": func() error { return c.ApplyPermissions("op") },
		"stop_forget":         func() error { return c.StartForget("op", nil) },
		"mark_cleanup":        func() error { return c.mark("op", "cleaning", nil) },
		"read_operation":      func() error { _, err := c.operation("op"); return err },
		"scan_cleanup":        func() error { _, err := c.pending(); return err },
		"receipt":             func() error { _, err := c.Receipt(context.Background(), "op"); return err },
		"pending_approval":    func() error { _, err := c.PendingManagement(context.Background()); return err },
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); !errors.Is(err, failure) {
				t.Fatal("real failure was hidden", err)
			}
		})
	}
	if a.wrote {
		t.Fatal("read failure caused a write")
	}
}

func TestManagementProjectionDoesNotReopenCommittedDeletion(t *testing.T) {
	s := contract.ControlState{InformationControl: &contract.InformationControlState{Principals: []contract.Principal{{ID: "owner", Revision: 2, Permissions: []contract.Permission{contract.ManagePermission}}}}}
	for _, status := range []string{"approved", "stopping", "cleaning", "completed", "declined", "superseded"} {
		op := contract.ManagementReceipt{Request: contract.ManagementRequest{ID: "op"}, Status: status, Approver: "owner", ApproverRevision: 1}
		visible := visibleManagement(s, op)
		want := status
		if status == "approved" {
			want = "awaiting_approval"
		}
		if visible.Status != want || op.Status != status {
			t.Fatal("projection changed committed facts", op, visible)
		}
	}
}

func TestFailedManagementCallCannotDiscardAnotherSafetyCommit(t *testing.T) {
	for _, action := range []string{"propose", "decide"} {
		t.Run(action, func(t *testing.T) {
			c, owner, _ := accessControl(t)
			subject, _, err := c.Enroll(owner, "reader")
			if err != nil {
				t.Fatal(err)
			}
			if err = c.SetPermissions(owner, subject.ID, []contract.Permission{contract.ReadPermission}); err != nil {
				t.Fatal(err)
			}
			target := contract.Location{SystemID: c.SystemID(), ServiceID: "target", Endpoint: "https://target.test", Certificate: "test", Composition: "test"}
			h, err := c.PrepareHandoff(owner, "move", target)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.DecideHandoff(owner, h.ID, h.Revision, true); err != nil {
				t.Fatal(err)
			}
			if _, err = c.FreezeHandoff(owner, h.ID, true); err != nil {
				t.Fatal(err)
			}
			r := contract.ManagementRequest{ID: "safety-commit", Operation: "permissions", SubjectID: subject.ID}
			if _, err = c.Propose(owner, r); err != nil {
				t.Fatal(err)
			}
			product := NewProduct(nil, c)
			defer product.Close()
			if action == "propose" {
				_, err = product.Manage(context.Background(), r)
			} else {
				_, err = product.DecideVersion(context.Background(), r.ID, "invalid", false)
			}
			if err == nil {
				t.Fatal("unauthenticated action succeeded")
			}
			if err = c.ApplyPermissions(r.ID); err != nil {
				t.Fatal("failed call discarded the authorized commit", err)
			}
			current, err := c.Principal(owner, subject.ID)
			if err != nil || len(current.Permissions) != 0 || c.State().Access.Handoff != nil {
				t.Fatal(current, err)
			}
		})
	}
}

func (a *terminalGuardAuthority) ReadControl() contract.ControlState { return a.state }
func (a *terminalGuardAuthority) CompareAndSwapControl(uint64, contract.ControlState) (contract.ControlState, error) {
	a.wrote = true
	return contract.ControlState{}, errors.New("unexpected commit")
}

func TestForgetTerminalGuardPreservesFailuresAndCommittedDeletion(t *testing.T) {
	readFailure := errors.New("injected authority read failure")
	for _, mode := range []string{"read_failure", "uninitialized", "stopping", "cleaning", "completed", "approval_invalid"} {
		t.Run(mode, func(t *testing.T) {
			a := &terminalGuardAuthority{state: contract.ControlState{InformationControl: &contract.InformationControlState{
				OwnerID: "owner", Principals: []contract.Principal{{ID: "owner", Revision: 1, Permissions: []contract.Permission{contract.ManagePermission}}},
				Operations: []contract.ManagementReceipt{{Request: contract.ManagementRequest{ID: "op", Operation: "forget"}, Status: mode, Approver: "owner", ApproverRevision: 1}},
			}}}
			if mode == "read_failure" {
				a.state = contract.ControlState{ReadError: readFailure}
			} else if mode == "uninitialized" {
				a.state.InformationControl = nil
			} else if mode == "approval_invalid" {
				a.state.InformationControl.Operations[0].Status = "approved"
				a.state.InformationControl.Principals[0].Revision++
			}
			err := New(a).supersedeForget("op")
			if a.wrote {
				t.Fatal("terminal close modified a guarded state")
			}
			if mode == "completed" {
				if err != nil {
					t.Fatal("already terminal request is not idempotent", err)
				}
			} else if mode == "read_failure" {
				if !errors.Is(err, readFailure) {
					t.Fatal("authority failure was hidden", err)
				}
			} else if !errors.Is(err, ErrDenied) {
				t.Fatal("unsafe state accepted", err)
			}
		})
	}
}
