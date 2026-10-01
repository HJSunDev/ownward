package mcpserver

import (
	"context"
	"encoding/json"
	"io"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

type draftTools struct {
	server *StorageServer
	work   contract.AgentDraftWork
}

func (s *StorageServer) AddDraftTools(work contract.AgentDraftWork) {
	registerStream[contract.AgentDraftRequest, contract.AgentDraftResult](s.server, draftTools{s, work}, "ownward_draft_work", "在当前智能体会话处理文稿，页面仅为可选手动入口。已有 read+maintain+manage 授权的主智能体可 list/create/read/replace/append；按用户明确要求 publish 或 discard，无须逐稿批准。read 返回当前 handle，写入须用 handle；publish 还须稳定 operation，结果不确定时沿同一输入重试，重连后可用 publication+operation 查询原结果。create 结果不确定时先 list 核对，勿重复新建。临时协作：主智能体 invite 后交给协作者 request（带 invitation 与 system）；管理智能体 requests 查看真实接入者和核对标记，携带请求 handle decide，或 end/cancel 收回，在原任务办理。已有主智能体请求返回 ready，沿 draft+handle 使用既有授权；普通协作者经批准后仅凭 request read/replace/append，不能 list/publish/授权。status 可有界等待（≤25 秒），中断沿同一 request 接续。", false, false)
}
func (v draftTools) ExecuteStream(ctx context.Context, r contract.StreamRequest) (*contract.StreamResult, error) {
	input, e := r.Arguments.Open(ctx)
	if e != nil {
		return nil, e
	}
	d, e := streamjson.Parse(ctx, v.server.dir, input, v.server.budget, v.server.diskBytes)
	input.Close()
	if e != nil {
		return nil, e
	}
	defer d.Close()
	var in contract.AgentDraftRequest
	if e = d.Root().DecodeSmall(&in, contract.OwnerRequestBytes); e != nil {
		return nil, e
	}
	out, e := v.work.DraftWork(ctx, in)
	if e != nil {
		return nil, e
	}
	doc, e := streamjson.Build(ctx, v.server.dir, v.server.budget, v.server.diskBytes, func(w io.Writer) error { return json.NewEncoder(w).Encode(out) })
	if e != nil {
		return nil, e
	}
	return &contract.StreamResult{Value: streamjson.RawSource{Node: doc.RootContext(context.WithoutCancel(ctx))}, Close: doc.Close, Check: func(c context.Context) error { return v.work.CheckDraftWork(c, in, out.Handle) }}, nil
}
