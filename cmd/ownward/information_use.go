package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 同一主体对同一资产使用稳定的领取请求标识：结果不明时重试可幂等取回同一凭据，
// 释放或到期后同一标识仍可重新领取。
func organizeRequestID(principal, asset string) string {
	sum := sha256.Sum256([]byte("organize\x00" + principal + "\x00" + asset))
	return "organize-" + hex.EncodeToString(sum[:16])
}

// 按需组织入口：领取指定资料的组织待办并把工作材料交回调用者；不唤醒后台、
// 不等待模型完成、不代行组织。互斥、权限与提交校验沿用既有领取/提交契约。
// 暴露条件为“调用者可组织”（调用时校验维护权限），与是否注册执行器无关。
func (h *hostConnector) addOrganizationDemand(proxy *mcp.Server, call func(ctx context.Context, request *mcp.CallToolRequest, name string, args any) (*mcp.CallToolResult, error)) {
	mcp.AddTool(proxy, &mcp.Tool{Name: "ownward_organize", Description: "仅当当前需求依赖某份资料的完整组织时使用：领取该待办并把组织材料交回调用者，完成后用 ownward_semantic_submit 提交（凭据填入 execution_lease）。不唤醒后台、不等待模型完成；原文读取不需要此调用。"}, func(ctx context.Context, request *mcp.CallToolRequest, in struct {
		ID string `json:"id"`
	}) (*mcp.CallToolResult, map[string]any, error) {
		if in.ID == "" || len(in.ID) > 256 {
			return nil, nil, errors.New("资料身份无效")
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
		result, err := call(ctx, request, "ownward_status", map[string]any{"id": in.ID})
		if err != nil || result.IsError {
			return result, nil, err
		}
		var status struct {
			Organization contract.OrganizationState
		}
		if err = decodeTool(result, &status); err != nil {
			return nil, nil, err
		}
		if status.Organization.Status == "ready" || status.Organization.Status == "uncertain" {
			return result, nil, nil
		}
		if status.Organization.RequiredAction != "ownward_semantic_jobs" {
			return result, nil, nil
		}
		claimResult, err := call(ctx, request, "ownward_semantic_jobs", map[string]any{"action": "claim", "asset_id": in.ID, "request_id": organizeRequestID(self.ID, in.ID)})
		if err != nil {
			return nil, nil, err
		}
		if claimResult.IsError {
			return claimResult, nil, nil
		}
		var claim contract.OrganizationJobResult
		if err = decodeTool(claimResult, &claim); err != nil {
			return nil, nil, err
		}
		if claim.Claim == nil {
			// 待办存在但当前不可领取（已有持有者或正在完成）：如实返回持有状态与接续信息。
			return nil, map[string]any{
				"status":       "held",
				"id":           in.ID,
				"organization": status.Organization,
				"message":      "组织待办已存在但当前不可领取（已有持有者或正在完成）；待办保留，可用 ownward_status 核对，待凭据释放或到期后重试。",
			}, nil
		}
		workResult, err := call(ctx, request, "ownward_semantic_work", map[string]any{"asset_ids": []string{claim.Claim.AssetID}, "lease": claim.Claim.Lease})
		if err != nil {
			return nil, nil, err
		}
		if workResult.IsError {
			return nil, map[string]any{"status": "claimed", "id": in.ID, "lease": claim.Claim, "message": "已领取但组织材料暂不可用；凭据有效期内可重试 ownward_semantic_work，或释放后重试。"}, nil
		}
		var work struct {
			Work json.RawMessage `json:"work"`
		}
		if err = decodeTool(workResult, &work); err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{
			"status": "claimed",
			"id":     in.ID,
			"lease":  claim.Claim,
			"work":   work.Work,
			"submit": "完成后用 ownward_semantic_submit 提交，execution_lease 填入每项 submission；同一工作不会被重复领取。",
		}, nil
	})
}
