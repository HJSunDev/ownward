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
	registerStream[contract.AgentDraftRequest, contract.AgentDraftResult](s.server, draftTools{s, work}, "ownward_draft_work", "在物主明确授予的单份文稿内读取、替换或追加文字。先 read 取得当前 handle，写入必须携带该 handle；不枚举其他文稿、不发布资产、不取得全库权限。", false, false)
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
