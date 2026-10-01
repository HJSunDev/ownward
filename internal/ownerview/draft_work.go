package ownerview

import (
	"context"
	"errors"
	"fmt"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
)

func (s *Service) collaborationInstruction(inv contract.DraftInvitation) string {
	text := fmt.Sprintf("请协助编辑 Ownward 文稿。使用已有连接调用 ownward_draft_work，action=request，system=%s，invitation=%s。若返回 ready，沿已有授权使用 draft 和 handle；若 awaiting_approval，展示 request 与核对标记，由我信任的管理智能体通过 requests/decide 处理，也可在 Ownward 手动确认。获准后携带 request 读取当前稿并提交修改。此次仅修改草稿；加入资料须有我的明确要求且具备长期管理与维护授权。", s.Control.SystemID(), inv.ID)
	if s.DraftInstruction != nil {
		text = s.DraftInstruction(inv, text)
	}
	return text
}

func authorAction(action string) bool {
	switch action {
	case "list", "create", "publish", "publication", "discard", "invite":
		return true
	}
	return false
}

func managerAction(action string) bool {
	switch action {
	case "requests", "decide", "end", "cancel":
		return true
	}
	return false
}

func (s *Service) DraftWork(ctx context.Context, in contract.AgentDraftRequest) (out contract.AgentDraftResult, err error) {
	if in.Action == "request" || in.Action == "status" {
		return s.collaborate(ctx, in)
	}
	if managerAction(in.Action) {
		return s.manageDraft(ctx, in)
	}
	if len(in.Text) > contract.OwnerRequestBytes {
		return out, errors.New("单次文本超过提交上限")
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if in.Limit < 1 || in.Limit > 20 {
		return out, errors.New("每页须为 1 至 20 项")
	}
	if in.Request != "" {
		v, e := s.Store.DraftCollaboration(ctx, in.Request)
		if e != nil {
			return out, e
		}
		if v.State != "approved" {
			return out, errors.New("此协作尚未获准或已结束")
		}
		in.Draft, in.Grant = v.DraftID, v.Grant
	}
	var authority string
	var e error
	if in.Grant == "" || authorAction(in.Action) {
		authority, e = s.Store.DraftAuthority(ctx, true)
		if e != nil {
			return out, e
		}
		ctx = boundedstore.BindDraftAuthority(ctx, authority)
	}
	binding := contract.AuthenticationDigest(ctx) + ":" + in.Grant
	if in.Action == "publication" {
		return s.draftPublication(ctx, in.Operation, authority)
	}
	var h handle
	if in.Handle != "" {
		h, e = s.open(in.Handle)
		if e != nil || h.Type != "agent-draft" || h.ID != in.Draft || h.Binding != binding || h.Position != authority {
			return out, contract.ErrOwnerRefresh
		}
	}
	// Publication uses the original handle before loading the now-consumed
	// draft, so an uncertain response can be recovered with the same operation.
	if in.Action == "publish" || in.Action == "discard" {
		if in.Handle == "" {
			return out, contract.ErrOwnerRefresh
		}
		if in.Action == "publish" {
			if in.Operation == "" {
				return out, errors.New("发布须提供稳定的操作标识，重试沿用同一标识")
			}
			_, e := s.Store.PublishDraft(ctx, h.ID, h.Revision, contract.OperationIdentity{ID: in.Operation})
			if e != nil {
				return out, e
			}
			return s.draftPublication(ctx, in.Operation, authority)
		} else {
			if e = s.Store.DiscardDraft(ctx, h.ID, h.Revision); e != nil {
				return out, e
			}
		}
		out.State = "completed"
		out.Handle = s.seal(handle{Type: "agent-authority", Position: authority, Binding: binding})
		return out, nil
	}
	var d contract.Draft
	switch in.Action {
	case "list":
		checkpoint, e := s.Store.DraftCheckpoint(ctx)
		if e != nil {
			return out, e
		}
		page, e := s.Store.ListDrafts(ctx, in.After, in.Limit)
		if e != nil {
			return out, e
		}
		out.Drafts, out.Next = page.Items, page.Next
		out.Handle = s.seal(handle{Type: "agent-projection", Position: authority, Binding: binding, System: checkpoint})
		return out, nil
	case "create":
		input := contract.DraftInput{Target: in.Target}
		if in.Target.ID == "" || in.Text != "" {
			input.Content = boundedstore.StringSource(in.Text)
		}
		d, e = s.Store.CreateDraft(ctx, input)
	default:
		if in.Draft == "" {
			return out, errors.New("缺少文稿或已批准的协作请求")
		}
		d, e = s.Store.DraftMetadata(ctx, in.Draft, in.Grant)
		if e != nil {
			return out, e
		}
		if in.Handle != "" && h.Revision != d.Revision {
			return out, contract.ErrOwnerRefresh
		}
		switch in.Action {
		case "read":
			if in.Offset != 0 && in.Handle == "" {
				return out, contract.ErrOwnerRefresh
			}
			var v contract.OwnerText
			v, e = s.Store.OwnerText(ctx, d.ID, d.Revision, "draft_content", in.Offset, in.Grant)
			out.Content = &v
		case "replace", "append":
			if in.Handle == "" {
				return out, contract.ErrOwnerRefresh
			}
			d, e = s.Store.WriteDraft(ctx, contract.DraftWrite{ID: d.ID, ExpectedRevision: d.Revision, GrantID: in.Grant, Append: in.Action == "append", Content: boundedstore.StringSource(in.Text)})
		case "invite":
			if in.Handle == "" {
				return out, contract.ErrOwnerRefresh
			}
			var inv contract.DraftInvitation
			inv, e = s.Store.InviteDraft(ctx, d.ID, d.Revision)
			out.Invitation = &inv
			if e == nil {
				out.Instruction = s.collaborationInstruction(inv)
			}
		default:
			return out, errors.New("未知的文稿操作")
		}
	}
	if e != nil {
		return out, e
	}
	current, e := s.Store.DraftMetadata(ctx, d.ID, in.Grant)
	if e != nil {
		return out, e
	}
	if current.Revision != d.Revision {
		return out, contract.ErrOwnerRefresh
	}
	out.Draft = d.ID
	out.Handle = s.seal(handle{Type: "agent-draft", ID: d.ID, Revision: d.Revision, Binding: binding, Position: authority})
	return out, nil
}

func (s *Service) draftPublication(ctx context.Context, operation, authority string) (out contract.AgentDraftResult, err error) {
	v, e := s.Store.OwnerPublication(ctx, operation)
	if e != nil {
		return out, e
	}
	out.State = v.State
	if v.Asset.ID != "" {
		out.Asset = &v.Asset
	}
	out.Handle = s.seal(handle{Type: "agent-publication", ID: operation, Position: authority, Binding: contract.AuthenticationDigest(ctx) + ":", System: scopeBinding(v)})
	return out, nil
}

func (s *Service) manageDraft(ctx context.Context, in contract.AgentDraftRequest) (out contract.AgentDraftResult, err error) {
	authority, e := s.Store.DraftAuthority(ctx, false)
	if e != nil {
		return out, e
	}
	ctx = boundedstore.BindDraftAuthority(ctx, authority)
	binding := contract.AuthenticationDigest(ctx) + ":"
	if in.Action == "requests" {
		checkpoint, e := s.Store.DraftCheckpoint(ctx)
		if e != nil {
			return out, e
		}
		if in.Limit == 0 {
			in.Limit = 20
		}
		if in.Limit < 1 || in.Limit > 20 {
			return out, errors.New("每页须为 1 至 20 项")
		}
		var rows []contract.DraftCollaboration
		if in.Request != "" {
			v, e := s.Store.DraftCollaboration(ctx, in.Request)
			if e != nil {
				return out, e
			}
			rows = append(rows, v)
		} else {
			rows, out.Next, e = s.Store.DraftCollaborations(ctx, in.Draft, in.After, in.Limit, "", "")
			if e != nil {
				return out, e
			}
		}
		for _, v := range rows {
			p, e := s.Store.DraftRequestSubject(ctx, v.ID)
			if e != nil {
				return out, e
			}
			out.Requests = append(out.Requests, contract.AgentDraftCollaboration{Request: v.ID, Draft: v.DraftID, State: v.State, Subject: p.Name, Distinction: connectionDistinction(p.Order), Verification: v.Verification, ExpiresAt: v.ExpiresAt, Handle: s.seal(handle{Type: "agent-request", ID: v.ID, Revision: v.Revision, Binding: binding, Position: authority})})
		}
		out.Handle = s.seal(handle{Type: "agent-projection", Position: authority, Binding: binding, System: checkpoint})
		return out, nil
	} else if in.Action == "cancel" {
		if in.Invitation == "" {
			return out, errors.New("缺少要取消的协作邀请")
		}
		e = s.Control.ChangeOwnerWork(ctx, func() error { return s.Store.CancelDraftInvitation(ctx, in.Invitation) })
	} else {
		h, err := s.open(in.Handle)
		if err != nil || h.Type != "agent-request" || h.ID != in.Request || h.Position != authority || h.Binding != binding {
			return out, contract.ErrOwnerRefresh
		}
		e = s.Control.ChangeOwnerWork(ctx, func() error {
			if in.Action == "end" {
				return s.Store.EndDraftCollaboration(ctx, h.ID)
			}
			v, e := s.Store.DecideDraftCollaboration(ctx, h.ID, h.Revision, in.Accept)
			out.State = v.State
			return e
		})
	}
	if e != nil {
		return out, e
	}
	if out.State == "" {
		out.State = "completed"
	}
	out.Handle = s.seal(handle{Type: "agent-authority", Position: authority, Binding: binding})
	return out, nil
}

func (s *Service) CheckDraftWork(ctx context.Context, in contract.AgentDraftRequest, value string) error {
	if (in.Action == "request" || in.Action == "status") && value == "" {
		return ctx.Err()
	}
	h, e := s.open(value)
	if e != nil {
		return e
	}
	if h.Position != "" {
		ctx = boundedstore.BindDraftAuthority(ctx, h.Position)
		if _, e = s.Store.DraftAuthority(ctx, !managerAction(in.Action)); e != nil {
			return e
		}
	}
	if h.Type == "agent-publication" {
		if h.Position == "" || h.Binding != contract.AuthenticationDigest(ctx)+":" {
			return boundedstore.ErrAccess
		}
		v, e := s.Store.OwnerPublication(ctx, h.ID)
		if e != nil {
			return e
		}
		if scopeBinding(v) != h.System {
			return contract.ErrOwnerRefresh
		}
		return ctx.Err()
	}
	if h.Type == "agent-authority" || h.Type == "agent-projection" {
		if h.Position == "" || h.Binding != contract.AuthenticationDigest(ctx)+":" {
			return boundedstore.ErrAccess
		}
		if h.Type == "agent-projection" {
			checkpoint, e := s.Store.DraftCheckpoint(ctx)
			if e != nil {
				return e
			}
			if checkpoint != h.System {
				return contract.ErrOwnerRefresh
			}
		}
		return ctx.Err()
	}
	if in.Request != "" {
		v, e := s.Store.DraftCollaboration(ctx, in.Request)
		if e != nil {
			return e
		}
		if v.State != "approved" {
			return boundedstore.ErrAccess
		}
		in.Draft, in.Grant = v.DraftID, v.Grant
	}
	if h.Type != "agent-draft" || (in.Draft != "" && h.ID != in.Draft) || h.Binding != contract.AuthenticationDigest(ctx)+":"+in.Grant {
		return contract.ErrOwnerRefresh
	}
	d, e := s.Store.DraftMetadata(ctx, h.ID, in.Grant)
	if e != nil {
		return e
	}
	if d.Revision != h.Revision {
		return contract.ErrOwnerRefresh
	}
	return ctx.Err()
}
