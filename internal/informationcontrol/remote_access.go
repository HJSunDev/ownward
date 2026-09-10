package informationcontrol

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
)

var ErrInactive = errors.New("原位置已退出活动服务，请接续已确认的新位置")
var ErrMoving = errors.New("信息体系正在交接，普通变更暂未执行，请保留原操作接续")

// Only completing the already-authorized handoff is allowed after retirement.
func (c *Control) HandoffManager(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	return CheckHandoffManager(ctx, s, id)
}
func CheckHandoffManager(ctx context.Context, s contract.ControlState, id string) error {
	if s.Access == nil || s.Access.Handoff == nil || s.Access.Handoff.ID != id {
		return ErrDenied
	}
	s.Access = nil
	_, err := principal(ctx, s, contract.ManagePermission)
	return err
}

func (c *Control) PendingManagement(ctx context.Context) ([]contract.ManagementReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	p, err := principal(ctx, s, contract.ManagePermission)
	if err != nil {
		return nil, err
	}
	var out []contract.ManagementReceipt
	for _, op := range s.InformationControl.Operations {
		if op.Status == "awaiting_approval" {
			out = append(out, op)
		}
	}
	for _, op := range c.deferred {
		if op.Status == "awaiting_approval" {
			out = append(out, op)
		}
	}
	_ = p
	return out, nil
}

func accessState(s *contract.ControlState) *contract.AccessState {
	if s.Access == nil {
		s.Access = &contract.AccessState{}
	}
	s.Schema = contract.AccessControlStateSchema
	return s.Access
}

func frozen(s contract.ControlState) bool {
	return s.Access != nil && s.Access.Handoff != nil && s.Access.Handoff.Phase == "frozen"
}
func inactive(s contract.ControlState) bool {
	return s.Access != nil && s.Access.Handoff != nil && s.Access.Handoff.Phase == "retired"
}
func mutable(s contract.ControlState) error {
	if inactive(s) {
		return ErrInactive
	}
	if frozen(s) {
		return ErrMoving
	}
	return nil
}

func (c *Control) State() contract.ControlState { return c.authority.ReadControl() }

func (c *Control) Invite(ctx context.Context, id string) (contract.Enrollment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	p, err := principal(ctx, s, contract.ManagePermission)
	if err != nil {
		return contract.Enrollment{}, err
	}
	if err = mutable(s); err != nil {
		return contract.Enrollment{}, err
	}
	if id == "" || len(id) > 128 {
		return contract.Enrollment{}, errors.New("接入操作标识无效")
	}
	a := accessState(&s)
	now := time.Now().UTC()
	for _, e := range a.Enrollments {
		if e.ID == id {
			if e.Manager != p.ID || now.After(e.Expires) {
				return contract.Enrollment{}, ErrDenied
			}
			return publicEnrollment(e), nil
		}
	}
	a.Enrollments = slices.DeleteFunc(a.Enrollments, func(e contract.Enrollment) bool { return now.After(e.Expires) })
	if len(a.Enrollments) >= 256 {
		return contract.Enrollment{}, errors.New("待接入操作已满，请完成已有申请")
	}
	e := contract.Enrollment{ID: id, Manager: p.ID, Status: "waiting", Expires: now.Add(24 * time.Hour)}
	a.Enrollments = append(a.Enrollments, e)
	return e, c.save(s)
}

func publicEnrollment(e contract.Enrollment) contract.Enrollment { e.ProofDigest = ""; return e }
func enrollment(s *contract.ControlState, id string) (*contract.Enrollment, error) {
	if s.Access != nil {
		for i := range s.Access.Enrollments {
			e := &s.Access.Enrollments[i]
			if e.ID == id && time.Now().Before(e.Expires) {
				return e, nil
			}
		}
	}
	return nil, errors.New("接入操作不存在或已失效")
}

func (c *Control) Join(id, proof, name string, permissions []contract.Permission) (contract.Enrollment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	if err := mutable(s); err != nil {
		return contract.Enrollment{}, err
	}
	e, err := enrollment(&s, id)
	if err != nil {
		return contract.Enrollment{}, err
	}
	if len(proof) < 32 || len(proof) > 256 || strings.TrimSpace(name) == "" || len(name) > 256 {
		return contract.Enrollment{}, ErrDenied
	}
	permissions = slices.Clone(permissions)
	slices.Sort(permissions)
	permissions = slices.Compact(permissions)
	if len(permissions) == 0 {
		return contract.Enrollment{}, ErrDenied
	}
	for _, p := range permissions {
		if p != contract.ReadPermission && p != contract.MaintainPermission && p != contract.ManagePermission {
			return contract.Enrollment{}, ErrDenied
		}
	}
	if e.Status != "waiting" {
		if e.ProofDigest != digest(proof) || e.Name != name || !slices.Equal(e.Permissions, permissions) {
			return contract.Enrollment{}, ErrDenied
		}
		return publicEnrollment(*e), nil
	}
	e.ProofDigest = digest(proof)
	e.Name = name
	e.Permissions = permissions
	e.Status = "pending"
	out := publicEnrollment(*e)
	return out, c.save(s)
}

func EnrollmentMarker(id, proof string) string { return digest(id + ":" + digest(proof))[:8] }

func (c *Control) Enrollments(ctx context.Context) ([]contract.Enrollment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	p, err := principal(ctx, s, contract.ManagePermission)
	if err != nil {
		return nil, err
	}
	var out []contract.Enrollment
	if s.Access != nil {
		for _, e := range s.Access.Enrollments {
			if e.Manager == p.ID && time.Now().Before(e.Expires) {
				out = append(out, publicEnrollment(e))
			}
		}
	}
	return out, nil
}

func (c *Control) EnrollmentPreview(ctx context.Context, id string) (contract.Enrollment, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	p, err := principal(ctx, s, contract.ManagePermission)
	if err != nil {
		return contract.Enrollment{}, "", err
	}
	e, err := enrollment(&s, id)
	if err != nil {
		return contract.Enrollment{}, "", err
	}
	if e.Manager != p.ID || e.ProofDigest == "" {
		return contract.Enrollment{}, "", ErrDenied
	}
	return publicEnrollment(*e), digest(id + ":" + e.ProofDigest)[:8], nil
}

func (c *Control) DecideEnrollment(ctx context.Context, id, marker string, accept bool) (contract.Enrollment, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	p, err := principal(ctx, s, contract.ManagePermission)
	if err != nil {
		return contract.Enrollment{}, err
	}
	if err = mutable(s); err != nil {
		return contract.Enrollment{}, err
	}
	e, err := enrollment(&s, id)
	if err != nil {
		return contract.Enrollment{}, err
	}
	if e.Manager != p.ID || e.ProofDigest == "" || digest(id + ":" + e.ProofDigest)[:8] != marker {
		return contract.Enrollment{}, ErrDenied
	}
	if e.Status != "pending" {
		return publicEnrollment(*e), nil
	}
	e.Status = "declined"
	if accept {
		e.Status = "approved"
		e.ApproverRevision = p.Revision
	}
	out := publicEnrollment(*e)
	return out, c.save(s)
}

func (c *Control) ClaimEnrollment(id, proof string) (contract.Enrollment, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	if err := mutable(s); err != nil {
		return contract.Enrollment{}, "", err
	}
	e, err := enrollment(&s, id)
	if err != nil {
		return contract.Enrollment{}, "", err
	}
	if e.ProofDigest != digest(proof) {
		return contract.Enrollment{}, "", ErrDenied
	}
	if e.Status != "approved" {
		return publicEnrollment(*e), "", nil
	}
	if e.Claimed {
		return publicEnrollment(*e), "", nil
	}
	if !approvalValid(s, contract.ManagementReceipt{Approver: e.Manager, ApproverRevision: e.ApproverRevision}) {
		return contract.Enrollment{}, "", ErrDenied
	}
	token, err := randomID("")
	if err != nil {
		return contract.Enrollment{}, "", err
	}
	if e.Principal == "" {
		id, err := randomID("p_")
		if err != nil {
			return contract.Enrollment{}, "", err
		}
		e.Principal = id
		s.InformationControl.Principals = append(s.InformationControl.Principals, contract.Principal{ID: id, Name: e.Name, Permissions: slices.Clone(e.Permissions), Revision: 1, CredentialDigest: digest(token)})
	} else {
		for i := range s.InformationControl.Principals {
			p := &s.InformationControl.Principals[i]
			if p.ID == e.Principal {
				if !slices.Equal(p.Permissions, e.Permissions) {
					return contract.Enrollment{}, "", ErrDenied
				}
				p.Revision++
				p.CredentialDigest = digest(token)
			}
		}
	}
	out := publicEnrollment(*e)
	if err := c.save(s); err != nil {
		return out, "", err
	}
	return out, token, nil
}

func (c *Control) AcknowledgeEnrollment(ctx context.Context, id, proof string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	if err := mutable(s); err != nil {
		return err
	}
	p, err := principal(ctx, s, "")
	if err != nil {
		return err
	}
	e, err := enrollment(&s, id)
	if err != nil {
		return err
	}
	if e.Principal != p.ID || e.ProofDigest != digest(proof) || e.Status != "approved" {
		return ErrDenied
	}
	if e.Claimed {
		return nil
	}
	e.Claimed = true
	return c.save(s)
}

func (c *Control) PrepareHandoff(ctx context.Context, id string, target contract.Location) (contract.Handoff, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	if _, err := principal(ctx, s, contract.ManagePermission); err != nil {
		return contract.Handoff{}, err
	}
	if id == "" || target.Validate() != nil || target.SystemID != s.InformationControl.SystemID {
		return contract.Handoff{}, errors.New("迁移目标未绑定当前体系")
	}
	a := accessState(&s)
	for _, h := range a.Cancelled {
		if h.ID == id {
			return contract.Handoff{}, errors.New("已取消迁移不能重新使用")
		}
	}
	if a.Handoff != nil && a.Handoff.Phase != "active" {
		if a.Handoff.ID == id && a.Handoff.Target == target {
			return *a.Handoff, nil
		}
		return contract.Handoff{}, ErrMoving
	}
	a.Handoff = &contract.Handoff{ID: id, Target: target, Phase: "prepared", Revision: s.Revision + 1}
	out := *a.Handoff
	return out, c.save(s)
}

func (c *Control) FreezeHandoff(ctx context.Context, id string, locationSaved bool) (contract.Handoff, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	if _, err := principal(ctx, s, contract.ManagePermission); err != nil {
		return contract.Handoff{}, err
	}
	if s.Access == nil || s.Access.Handoff == nil || s.Access.Handoff.ID != id {
		return contract.Handoff{}, ErrDenied
	}
	h := s.Access.Handoff
	for _, cancelled := range s.Access.Cancelled {
		if !cancelled.Cleaned {
			return contract.Handoff{}, errors.New("请先完成已取消搬迁的副本清理")
		}
	}
	if h.Phase == "frozen" {
		return *h, nil
	}
	if h.Phase != "prepared" || !locationSaved {
		return contract.Handoff{}, ErrDenied
	}
	for _, op := range s.InformationControl.Operations {
		if op.Status == "stopping" || op.Status == "cleaning" {
			return contract.Handoff{}, errors.New("须先完成已有遗忘清理")
		}
	}
	h.Phase = "frozen"
	h.LocationSaved = true
	h.Revision = s.Revision + 1
	out := *h
	return out, c.save(s)
}

func (c *Control) RetireHandoff(ctx context.Context, id, snapshot string, revision uint64) (contract.Handoff, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	if s.Access != nil && s.Access.Handoff != nil && s.Access.Handoff.ID == id && s.Access.Handoff.Phase == "retired" && s.Access.Handoff.Snapshot == snapshot {
		if err := CheckHandoffManager(ctx, s, id); err != nil {
			return contract.Handoff{}, err
		}
		return *s.Access.Handoff, nil
	}
	if _, err := principal(ctx, s, contract.ManagePermission); err != nil {
		return contract.Handoff{}, err
	}
	if !frozen(s) || s.Access.Handoff.ID != id || s.Revision != revision || len(snapshot) != 64 {
		return contract.Handoff{}, errors.New("迁移快照已失效")
	}
	h := s.Access.Handoff
	h.Phase = "retired"
	h.Snapshot = snapshot
	h.Revision = s.Revision + 1
	out := *h
	return out, c.save(s)
}

func cancelHandoff(s *contract.ControlState) {
	if s.Access != nil && s.Access.Handoff != nil && s.Access.Handoff.Phase != "active" {
		s.Access.Cancelled = append(s.Access.Cancelled, *s.Access.Handoff)
		s.Access.Handoff = nil
	}
}

func (c *Control) MarkHandoffClean(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	if s.Access == nil {
		return nil
	}
	for i := range s.Access.Cancelled {
		if s.Access.Cancelled[i].ID == id {
			if s.Access.Cancelled[i].Cleaned {
				return nil
			}
			s.Access.Cancelled[i].Cleaned = true
			return c.save(s)
		}
	}
	return nil
}

func (c *Control) CancelHandoff(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.authority.ReadControl()
	if _, err := principal(ctx, s, contract.ManagePermission); err != nil {
		return err
	}
	if s.Access == nil || s.Access.Handoff == nil {
		return nil
	}
	if s.Access.Handoff.ID != id {
		return ErrDenied
	}
	cancelHandoff(&s)
	return c.save(s)
}
