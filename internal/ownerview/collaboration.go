package ownerview

import (
	"context"
	"errors"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
)

func (s *Service) collaborate(ctx context.Context, in contract.AgentDraftRequest) (out contract.AgentDraftResult, err error) {
	if in.WaitSeconds < 0 || in.WaitSeconds > 25 {
		return out, errors.New("每次等待须为 0 至 25 秒")
	}
	var v contract.DraftCollaboration
	if in.Action == "request" {
		if in.System == "" || in.System != s.Control.SystemID() {
			return out, errors.New("请求不属于当前资料库，请使用交接请求中的目标")
		}
		if _, e := s.Store.DraftAuthority(ctx, true); e == nil {
			draft, e := s.Store.ResolveDraftInvitation(ctx, in.Invitation)
			if e != nil {
				return out, e
			}
			out, e = s.DraftWork(ctx, contract.AgentDraftRequest{Action: "read", Draft: draft})
			out.State = "ready"
			out.Message = "沿用已有授权。使用 draft 和当前 handle 修改文稿，无须另行批准；用户明确要求后可加入资料。"
			return out, e
		} else if !errors.Is(e, boundedstore.ErrAccess) {
			return out, e
		}
		v, err = s.Store.RequestDraft(ctx, in.Invitation)
	} else {
		v, err = s.Store.DraftCollaboration(ctx, in.Request)
	}
	if err != nil {
		return out, err
	}
	deadline := time.NewTimer(time.Duration(in.WaitSeconds) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
waiting:
	for v.State == "awaiting_approval" && in.WaitSeconds > 0 {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-deadline.C:
			break waiting
		case <-ticker.C:
			v, err = s.Store.DraftCollaboration(ctx, v.ID)
			if err != nil {
				return out, err
			}
		}
	}
	out = contract.AgentDraftResult{Request: v.ID, State: v.State, Verification: v.Verification, ExpiresAt: &v.ExpiresAt}
	if v.State == "awaiting_approval" {
		out.Message = "请将 request 和核对标记交给用户信任的管理智能体，由其调用 requests 查明接入者，再按用户意愿 decide。用户也可在 Ownward 手动确认。可调用 status 有界等待；会话中断后携带同一 request 接续，不重复申请。"
	}
	if v.State == "approved" {
		out.Message = "已获准编辑这篇草稿。携带 request 调用 read 取得当前 handle，再 replace 或 append；最终由用户通过可信主智能体或页面加入资料。"
	}
	if v.State != "awaiting_approval" && v.State != "approved" {
		out.Message = "本次协作已结束；如需继续，可由可信主智能体或用户在页面重新发起。"
	}
	return out, nil
}
