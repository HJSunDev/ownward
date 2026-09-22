package ownerview

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
)

func (s *Service) decisionHandle(cp boundedstore.OwnerCheckpoint, kind, id string, revision uint64, binding any) string {
	return s.seal(handle{Type: kind, ID: id, Revision: revision, System: cp.System, OwnerRevision: cp.OwnerRevision, Binding: scopeBinding(binding)})
}

func (s *Service) decisions(ctx context.Context, cp boundedstore.OwnerCheckpoint, after string, limit int, pending bool) (out []contract.OwnerDecision, next string, err error) {
	if !pending {
		return s.history(ctx, cp, after, limit)
	}
	// One ordered projection, three existing authoritative record types. The
	// continuation carries the source position, never a duplicate approval.
	if after == "" || strings.HasPrefix(after, "m:") {
		rows, position, e := s.Store.OwnerOperations(ctx, strings.TrimPrefix(after, "m:"), limit, pending)
		if e != nil {
			return nil, "", e
		}
		for _, op := range rows {
			op, e = s.Control.Receipt(ctx, op.Request.ID)
			if e != nil {
				return nil, "", e
			}
			v := contract.OwnerDecision{Handle: s.seal(handle{Type: "operation", ID: op.Request.ID, System: cp.System, OwnerRevision: cp.OwnerRevision, Binding: op.Decision}), Kind: op.Request.Operation, State: op.Status, Permissions: op.Request.Permissions}
			if op.Request.Operation == "permissions" {
				p, e := s.Store.OwnerPrincipal(ctx, op.Request.SubjectID)
				if e != nil {
					return nil, "", e
				}
				v.Subject = p.Name
				v.Distinction = connectionDistinction(p.Order)
				v.Consequence = "调整这个接入者可访问的资料与能力，持续到再次调整。"
			} else {
				v.Consequence = "遗忘所选完整资料，并清理体系内的相关副本；外部交付与独立旧备份不在清理范围。"
			}
			for _, target := range op.Request.Targets {
				v.Targets = append(v.Targets, s.object(cp, "asset", target.ID, target.Revision))
			}
			out = append(out, v)
		}
		if position != "" {
			return out, "m:" + position, nil
		}
		after = "e:"
	}
	access, e := s.Control.OwnerAccess(ctx)
	if e != nil {
		return nil, "", e
	}
	if strings.HasPrefix(after, "e:") {
		position := strings.TrimPrefix(after, "e:")
		sort.Slice(access.Enrollments, func(i, j int) bool { return access.Enrollments[i].ID < access.Enrollments[j].ID })
		for _, v := range access.Enrollments {
			if v.ID <= position || v.Status != "pending" || !time.Now().Before(v.Expires) {
				continue
			}
			if len(out) == limit {
				return out, "e:" + position, nil
			}
			preview, marker, e := s.Control.EnrollmentPreview(ctx, v.ID)
			if e != nil {
				return nil, "", e
			}
			out = append(out, contract.OwnerDecision{Handle: s.decisionHandle(cp, "enrollment", v.ID, 0, enrollmentBinding(preview, marker)), Kind: "enrollment", State: "awaiting_approval", Subject: v.Name, Verification: marker, Permissions: v.Permissions, Consequence: "核对目标显示的标记后，让这个接入者以所列能力访问资料，权限持续到你收回。"})
			position = v.ID
		}
		after = "h:"
	}
	if after == "h:" && access.Handoff != nil && access.Handoff.Phase == "prepared" {
		h := access.Handoff
		if h.ApprovalStatus == "" || h.ApprovalStatus == "awaiting_approval" {
			if len(out) == limit {
				return out, "h:", nil
			}
			out = append(out, contract.OwnerDecision{Handle: s.decisionHandle(cp, "handoff", h.ID, h.Revision, h.Target), Kind: "handoff", State: "awaiting_approval", Subject: h.Target.Endpoint, Consequence: "把资料和连接关系迁往这个目的地；交接期间暂停修改，接管时短暂不可用。批准后由原迁移任务继续。"})
		}
	}
	return out, "", nil
}

func (s *Service) history(ctx context.Context, cp boundedstore.OwnerCheckpoint, after string, limit int) ([]contract.OwnerDecision, string, error) {
	rows, next, e := s.Store.OwnerHistory(ctx, after, limit)
	if e != nil {
		return nil, "", e
	}
	out := make([]contract.OwnerDecision, 0, len(rows))
	for _, row := range rows {
		v := contract.OwnerDecision{Handle: s.object(cp, "history", row.Key, 0), Kind: row.Kind, State: row.Status, At: &row.At}
		if row.Access != nil {
			v.Subject, v.Permissions = row.Access.Subject, row.Access.Permissions
			v.Consequence = "这是当时的决定记录；当前接入权限与迁移状态以体系现状为准。"
		} else if row.Operation != nil {
			op := row.Operation
			v.Kind, v.Permissions = op.Request.Operation, op.Request.Permissions
			if op.Request.SubjectID != "" {
				p, e := s.Store.OwnerPrincipal(ctx, op.Request.SubjectID)
				if e != nil {
					return nil, "", e
				}
				v.Subject, v.Distinction = p.Name, connectionDistinction(p.Order)
			}
			for _, target := range op.Request.Targets {
				v.Targets = append(v.Targets, s.object(cp, "asset", target.ID, target.Revision))
			}
			v.Consequence = "这是已处理的管理决定；记录不会再次执行该操作。"
			if op.Status == "superseded" {
				v.Consequence = "接入权限已被其他决定改变，本次调整未执行；如仍需调整，请刷新后重新确认。"
				if op.Request.Operation == "forget" {
					v.Consequence = "遗忘目标已变化或不可用，本次遗忘未执行；请刷新后重新确认。"
				}
			}
		}
		out = append(out, v)
	}
	return out, next, nil
}

func (s *Service) decide(ctx context.Context, cp boundedstore.OwnerCheckpoint, in contract.OwnerAction, out *contract.OwnerResult) error {
	h, e := s.open(in.Handle)
	if e != nil || h.System != cp.System || h.OwnerRevision != cp.OwnerRevision {
		return contract.ErrOwnerRefresh
	}
	switch h.Type {
	case "operation":
		op, e := s.Management.Receipt(ctx, h.ID)
		if e != nil {
			return e
		}
		if !op.Terminal() && op.Status == "awaiting_approval" && h.Binding != op.Decision {
			return contract.ErrOwnerRefresh
		}
		// The opaque presentation must be checked again by the authority, not
		// refreshed from this receipt between the read and the commit.
		op, e = s.Management.DecideVersion(ctx, h.ID, h.Binding, in.Accept)
		out.State = op.Status
		return e
	case "enrollment":
		v, marker, e := s.Control.EnrollmentPreview(ctx, h.ID)
		if e != nil {
			return e
		}
		if v.Status != "pending" {
			out.State = v.Status
			return nil
		}
		if h.Binding != scopeBinding(enrollmentBinding(v, marker)) {
			return contract.ErrOwnerRefresh
		}
		v, e = s.Control.DecideEnrollmentVersion(ctx, h.ID, marker, v.Decision, in.Accept)
		out.State = v.Status
		return e
	case "handoff":
		v, e := s.Control.HandoffStatus(ctx, h.ID)
		if e != nil {
			return e
		}
		if h.Binding != scopeBinding(v.Target) {
			return contract.ErrOwnerRefresh
		}
		v, e = s.Management.DecideHandoff(ctx, h.ID, h.Revision, in.Accept)
		out.State = v.ApprovalStatus
		return e
	default:
		return errors.New("不是可批准的事项")
	}
}

func enrollmentBinding(e contract.Enrollment, marker string) any {
	return struct {
		Enrollment contract.Enrollment
		Marker     string
	}{e, marker}
}
