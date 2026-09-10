package mcpserver

import (
	"context"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) addManagementTools() {
	management, ok := s.service.(contract.InformationManagement)
	if !ok {
		return
	}
	mcp.AddTool(s.server, &mcp.Tool{Name: "ownward_connections", Description: "查看接入者与其允许的行为；需要管理授权。", Annotations: closedWorldAnnotations(true, false, true)}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct {
		Connections []contract.Principal `json:"connections"`
	}, error) { values, err := management.Principals(ctx); return nil, struct {
		Connections []contract.Principal `json:"connections"`
	}{values}, err })
	mcp.AddTool(s.server, &mcp.Tool{Name: "ownward_manage", Description: "按用户明确需求调整或撤销接入权限，或遗忘完整资产。operation 为 permissions 或 forget；遗忘须提交已核对的资产及版本。相同操作重试沿用 id，批准由可信宿主办理，不能用参数声明批准。", Annotations: closedWorldAnnotations(false, true, true)}, func(ctx context.Context, _ *mcp.CallToolRequest, input contract.ManagementRequest) (*mcp.CallToolResult, contract.ManagementReceipt, error) {
		value, err := management.Manage(ctx, input)
		return nil, value, err
	})
	mcp.AddTool(s.server, &mcp.Tool{Name: "ownward_management_status", Description: "接续已有管理操作，取得批准、拒绝或清理完成结果，不重复发起操作。", Annotations: closedWorldAnnotations(true, false, true)}, func(ctx context.Context, _ *mcp.CallToolRequest, input struct {
		ID string `json:"id"`
	}) (*mcp.CallToolResult, contract.ManagementReceipt, error) { value, err := management.Receipt(ctx, input.ID); return nil, value, err })
}
