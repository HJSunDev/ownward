package informationcontrol

import (
	"context"
	"errors"
	"github.com/HJSunDev/ownward/internal/contract"
)

// Owner access is narrower than ManagePermission: a delegated manager never
// becomes the owner of the window or of private drafts.
func (c *Control) Owner(ctx context.Context) (contract.Principal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.selected(ctx, contract.ControlSelection{})
	p, e := principal(ctx, s, contract.ManagePermission)
	if e != nil {
		return contract.Principal{}, e
	}
	if p.ID != s.InformationControl.OwnerID {
		return contract.Principal{}, ErrDenied
	}
	p.CredentialDigest = ""
	return p, nil
}

func (c *Control) OwnerAccess(ctx context.Context) (contract.AccessState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.selected(ctx, contract.ControlSelection{Enrollments: true})
	p, e := principal(ctx, s, contract.ManagePermission)
	if e != nil {
		return contract.AccessState{}, e
	}
	if p.ID != s.InformationControl.OwnerID {
		return contract.AccessState{}, ErrDenied
	}
	if s.Access == nil {
		return contract.AccessState{}, nil
	}
	out := *s.Access
	if out.Handoff != nil {
		h := visibleHandoff(s, *out.Handoff)
		out.Handoff = &h
	}
	out.Enrollments = append([]contract.Enrollment(nil), s.Access.Enrollments...)
	for i := range out.Enrollments {
		out.Enrollments[i] = visibleEnrollment(s, out.Enrollments[i])
	}
	return out, nil
}

func (c *Control) HandoffStatus(ctx context.Context, id string) (contract.Handoff, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.selected(ctx, contract.ControlSelection{Handoff: id})
	if _, e := principal(ctx, s, contract.ManagePermission); e != nil {
		return contract.Handoff{}, e
	}
	return handoffStatus(s, id)
}

var ErrHandoffNotFound = errors.New("迁移操作不存在")

func handoffStatus(s contract.ControlState, id string) (contract.Handoff, error) {
	if s.Access != nil {
		for _, h := range s.Access.Cancelled {
			if h.ID == id {
				h.Phase = "cancelled"
				if h.ApprovalStatus != "declined" {
					h.ApprovalStatus = "cancelled"
				}
				return h, nil
			}
		}
		if h := s.Access.Handoff; h != nil && h.ID == id {
			return visibleHandoff(s, *h), nil
		}
	}
	return contract.Handoff{}, ErrHandoffNotFound
}

func visibleHandoff(s contract.ControlState, h contract.Handoff) contract.Handoff {
	if h.Phase == "prepared" && h.ApprovalStatus == "approved" && !approvalValid(s, contract.ManagementReceipt{Approver: h.Approver, ApproverRevision: h.ApproverRevision}) {
		h.ApprovalStatus = "awaiting_approval"
		// The current control revision binds reapproval to the identity state
		// that invalidated the old decision. Old forms cannot approve it again.
		h.Revision = s.Revision
	}
	return h
}

// The authority implements atomic grant removal + frozen-handoff cancellation.
// Notify its existing observers only after commit; no second decision is made.
func (c *Control) RevokeDraftGrant(ctx context.Context, work contract.OwnerWork, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.selected(ctx, contract.ControlSelection{})
	p, e := principal(ctx, s, contract.ManagePermission)
	if e != nil {
		return e
	}
	if p.ID != s.InformationControl.OwnerID {
		return ErrDenied
	}
	if e = work.RevokeDraftGrant(ctx, id); e != nil {
		return e
	}
	if c.changed != nil {
		close(c.changed)
	}
	c.changed = make(chan struct{})
	return nil
}

// The first decision wins on the original durable handoff. A late host form
// cannot override a window decision. Legacy prepared records require approval;
// an already frozen/retired handoff retains its historical continuation path.
func (c *Control) DecideHandoff(ctx context.Context, id string, revision uint64, accept bool) (contract.Handoff, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.selected(ctx, contract.ControlSelection{Handoff: id})
	p, e := principal(ctx, s, contract.ManagePermission)
	if e != nil {
		return contract.Handoff{}, e
	}
	visible, e := handoffStatus(s, id)
	if e != nil {
		return contract.Handoff{}, e
	}
	if visible.Phase == "cancelled" {
		return visible, nil
	}
	h := s.Access.Handoff
	if h.Phase != "prepared" || visible.ApprovalStatus == "approved" || h.ApprovalStatus == "declined" {
		return *h, nil
	}
	if visible.Revision != revision {
		return contract.Handoff{}, contract.ErrOwnerRefresh
	}
	h.ApprovalStatus = "declined"
	if accept {
		h.ApprovalStatus = "approved"
	}
	h.Approver = p.ID
	h.ApproverRevision = p.Revision
	h.Revision = s.Revision + 1
	out := *h
	if !accept {
		cancelHandoff(&s)
		out.Phase = "cancelled"
	}
	return out, c.save(s)
}
