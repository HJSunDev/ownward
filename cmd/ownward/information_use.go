package main

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A foreground dependency changes dispatch priority, never semantic content or
// resource qualification. Existing holders keep the same work and lease.
func (h *hostConnector) addOrganizationDemand(proxy *mcp.Server) {
	if os.Getenv("OWNWARD_INFORMATION_USE_PATHS") != "v1" || h.organization == nil {
		return
	}
	mcp.AddTool(proxy, &mcp.Tool{Name: "ownward_organize", Description: "仅当当前需求依赖某份资料的完整组织时请求接续。原文读取不需要此调用。不等待AI完成，不重开已运行工作；用ownward_status核对完成状态。"}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		ID string `json:"id"`
	}) (*mcp.CallToolResult, map[string]any, error) {
		if in.ID == "" || len(in.ID) > 256 {
			return nil, nil, errors.New("资料身份无效")
		}
		o := h.organization
		if !o.ready.Load() {
			return nil, map[string]any{"status": "unavailable", "message": "执行资源或授权未就绪；原文仍可读取，不能宣称组织已启动"}, nil
		}
		h.routeMu.RLock()
		var self contract.Principal
		err := h.controlCall(ctx, "self", h.credential(), nil, &self)
		h.routeMu.RUnlock()
		if err != nil {
			return nil, nil, err
		}
		if !slices.Contains(self.Permissions, contract.MaintainPermission) {
			return nil, nil, errors.New("当前主体没有维护权限")
		}
		result, err := o.call(ctx, "ownward_status", map[string]any{"id": in.ID})
		if err != nil || result.IsError {
			return result, nil, err
		}
		var status struct{ Organization contract.OrganizationState }
		if err = decodeTool(result, &status); err != nil {
			return nil, nil, err
		}
		if status.Organization.Status == "ready" || status.Organization.Status == "uncertain" {
			return result, nil, nil
		}
		if status.Organization.RequiredAction != "ownward_semantic_jobs" {
			return result, nil, nil
		}
		o.mu.Lock()
		if !slices.Contains(o.demand, in.ID) {
			if len(o.demand) >= 32 {
				o.mu.Unlock()
				return nil, nil, errors.New("当前依赖资料数已达上限；待办仍保留")
			}
			o.demand = append(o.demand, in.ID)
		}
		o.mu.Unlock()
		select {
		case o.wake <- struct{}{}:
		default:
		}
		return nil, map[string]any{"status": "requested", "id": in.ID, "message": "已请求优先接续同一待办；不表示已开始或已完成。使用ownward_status核对，等待计入当前任务。"}, nil
	})
}
