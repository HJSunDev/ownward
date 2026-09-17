package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func (s *StorageServer) AddManagementTools(m contract.InformationManagement) {
	v := streamingManagement{s, m}
	registerStream[struct{}, struct {
		Connections []contract.Principal `json:"connections"`
	}](s.server, v, "ownward_connections", "查看接入者与其允许的行为；需要管理授权。", true, false)
	registerStream[contract.ManagementRequest, contract.ManagementReceipt](s.server, v, "ownward_manage", "按用户明确需求调整授权或遗忘完整资产；批准由可信宿主办理，重试沿用操作 id。", false, true)
	registerStream[struct {
		ID string `json:"id"`
	}, contract.ManagementReceipt](s.server, v, "ownward_management_status", "取得已有管理操作的批准、拒绝或清理结果。", true, false)
}

type streamingManagement struct {
	s *StorageServer
	m contract.InformationManagement
}

func (v streamingManagement) ExecuteStream(ctx context.Context, request contract.StreamRequest) (*contract.StreamResult, error) {
	r, e := request.Arguments.Open(ctx)
	if e != nil {
		return nil, e
	}
	d, e := streamjson.Parse(ctx, v.s.dir, r, v.s.budget, v.s.diskBytes)
	r.Close()
	if e != nil {
		return nil, e
	}
	defer d.Close()
	var result any
	switch request.Operation {
	case "ownward_connections":
		visitor, ok := v.m.(contract.PrincipalVisitor)
		if !ok {
			return nil, errors.New("缺少流式接入列表")
		}
		authorizer, ok := v.m.(interface {
			ManagementAuthorization(context.Context) (func() error, error)
		})
		if !ok {
			return nil, errors.New("缺少管理交付许可")
		}
		finish, e := authorizer.ManagementAuthorization(ctx)
		if e != nil {
			return nil, e
		}
		doc, e := streamjson.Build(ctx, v.s.dir, v.s.budget, v.s.diskBytes, func(w io.Writer) error {
			if _, e := io.WriteString(w, `{"connections":[`); e != nil {
				return e
			}
			first := true
			e := visitor.VisitPrincipals(ctx, func(p contract.Principal) error {
				if !first {
					if _, e := io.WriteString(w, ","); e != nil {
						return e
					}
				}
				first = false
				return json.NewEncoder(w).Encode(p)
			})
			if e != nil {
				return e
			}
			_, e = io.WriteString(w, `]}`)
			return e
		})
		if e != nil {
			return nil, e
		}
		return &contract.StreamResult{Value: streamjson.RawSource{Node: doc.RootContext(context.WithoutCancel(ctx))}, Close: doc.Close, Check: func(delivery context.Context) error {
			if e := delivery.Err(); e != nil {
				return e
			}
			return finish()
		}}, nil
	case "ownward_manage":
		var input contract.ManagementRequest
		if e = d.Root().DecodeSmall(&input, 256*1024); e != nil {
			return nil, e
		}
		result, e = v.m.Manage(ctx, input)
	case "ownward_management_status":
		var input struct {
			ID string `json:"id"`
		}
		if e = d.Root().DecodeSmall(&input, 1024); e != nil {
			return nil, e
		}
		result, e = v.m.Receipt(ctx, input.ID)
	default:
		return nil, errors.New("未知管理操作")
	}
	if e != nil {
		return nil, e
	}
	doc, e := streamjson.Build(ctx, v.s.dir, v.s.budget, v.s.diskBytes, func(w io.Writer) error { return json.NewEncoder(w).Encode(result) })
	if e != nil {
		return nil, e
	}
	return &contract.StreamResult{Value: streamjson.RawSource{Node: doc.RootContext(context.WithoutCancel(ctx))}, Close: doc.Close, Check: func(c context.Context) error { return c.Err() }}, nil
}
