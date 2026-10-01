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
	// One ordered projection of authoritative decisions. The
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
				v.Consequence = "更改这个应用的访问权限，直到你再次调整。"
			} else {
				v.Consequence = "删除所选资料及其在 Ownward 中的副本。已导出的备份和其他应用保存的副本不受影响。"
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
			out = append(out, contract.OwnerDecision{Handle: s.decisionHandle(cp, "enrollment", v.ID, 0, enrollmentBinding(preview, marker)), Kind: "enrollment", State: "awaiting_approval", Subject: v.Name, Verification: marker, Permissions: v.Permissions, Consequence: "请确认下方验证码与申请连接的应用显示的一致。允许后，它将获得所列权限，直到你收回。"})
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
			out = append(out, contract.OwnerDecision{Handle: s.decisionHandle(cp, "handoff", h.ID, h.Revision, h.Target), Kind: "handoff", State: "awaiting_approval", Subject: h.Target.Endpoint, Consequence: "将资料库迁移到此地址，已连接的应用会一同迁移。迁移期间暂停修改，切换时会短暂中断访问。"})
		}
	}
	if after == "h:" || strings.HasPrefix(after, "d:") {
		draftAfter := strings.TrimPrefix(strings.TrimPrefix(after, "h:"), "d:")
		rows, position, e := s.Store.DraftCollaborations(ctx, "", draftAfter, max(1, limit-len(out)), "awaiting_approval", "")
		if e != nil {
			return nil, "", e
		}
		for _, v := range rows {
			if len(out) == limit {
				return out, "d:" + draftAfter, nil
			}
			p, e := s.Store.OwnerPrincipal(ctx, v.Principal)
			if e != nil {
				return nil, "", e
			}
			d, e := s.Store.DraftMetadata(ctx, v.DraftID, "")
			if e != nil {
				return nil, "", e
			}
			draft := s.draft(cp, d)
			out = append(out, contract.OwnerDecision{Handle: s.object(cp, "collaboration", v.ID, v.Revision), Kind: "draft_collaboration", State: v.State, Subject: p.Name, Distinction: connectionDistinction(p.Order), Verification: v.Verification, Draft: &draft, Consequence: "允许这个智能体在一小时内查看和编辑下面这篇草稿；不能代你加入资料。请核对智能体对话中的标记。"})
		}
		if position != "" {
			return out, "d:" + position, nil
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
			v.Consequence = "此记录保留当时的处理结果，权限和迁移状态可能已有后续变化。"
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
			v.Consequence = "此操作已处理。"
			if op.Status == "superseded" {
				v.Consequence = "访问权限已有变化，本次调整未执行。请重新打开权限设置后确认。"
				if op.Request.Operation == "forget" {
					v.Consequence = "资料已有变化或已不可用，本次删除未执行。请重新查看资料后确认。"
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
	case "collaboration":
		v, e := s.Store.DecideDraftCollaboration(ctx, h.ID, h.Revision, in.Accept)
		out.State = v.State
		return e
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
