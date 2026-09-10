package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type connectorRecord struct {
	Credential    string   `json:"credential"`
	Principal     string   `json:"principal"`
	Pending       []string `json:"pending,omitempty"`
	Connected     bool     `json:"connected,omitempty"`
	LastDelivered []string `json:"last_delivered,omitempty"`
}

type hostConnector struct {
	mu            sync.Mutex
	initMu        sync.Mutex
	descriptor    *sharedMCPDescriptor
	vault         localowner.Vault
	system        string
	profile       string
	record        connectorRecord
	recoveryScope string
}

func newHostConnector(ctx context.Context, descriptor *sharedMCPDescriptor, dataDir string) (*hostConnector, error) {
	vault, err := localowner.Default()
	if err != nil {
		return nil, err
	}
	h := &hostConnector{descriptor: descriptor, vault: vault, recoveryScope: ownerRecoveryScope(dataDir)}
	var identity struct {
		System string `json:"system_id"`
	}
	if err := h.controlCall(ctx, "identity", "", nil, &identity); err != nil {
		return nil, err
	}
	h.system = identity.System
	return h, nil
}

func (h *hostConnector) credential() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.record.Credential
}

func (h *hostConnector) save() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := json.Marshal(h.record)
	if err != nil {
		return err
	}
	return h.vault.Save(h.system, h.profile, string(data))
}

func (h *hostConnector) owner() (string, error) {
	credential, err := h.vault.Load(h.system, "owner")
	if err != nil {
		return "", errors.New("所有者管理连接需要由受保护入口恢复，请求已保留")
	}
	return credential, nil
}

func (h *hostConnector) initialize(ctx context.Context, request *mcp.CallToolRequest) error {
	h.initMu.Lock()
	defer h.initMu.Unlock()
	if h.credential() != "" {
		return nil
	}
	if h.system == "" {
		if err := h.restoreOwner(ctx, request.Session); err != nil {
			return err
		}
	}
	params := request.Session.InitializeParams()
	if params == nil || params.ClientInfo == nil || params.ClientInfo.Name == "" {
		return errors.New("宿主未提供接入名称")
	}
	h.profile = "agent:" + params.ClientInfo.Name
	if data, err := h.vault.Load(h.system, h.profile); err == nil {
		var record connectorRecord
		if json.Unmarshal([]byte(data), &record) != nil {
			return errors.New("连接接续状态损坏")
		}
		h.mu.Lock()
		h.record = record
		// 若上次回执已生成但宿主在收到前断开，重连仍可接回；操作本身保持幂等。
		for _, id := range record.LastDelivered {
			if !slices.Contains(h.record.Pending, id) {
				h.record.Pending = append(h.record.Pending, id)
			}
		}
		h.mu.Unlock()
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	owner, err := h.ensureOwner(ctx, request.Session)
	if err != nil {
		return err
	}
	var enrollment struct {
		Principal  contract.Principal `json:"principal"`
		Credential string             `json:"credential"`
	}
	if err := h.controlCall(ctx, "enroll", owner, map[string]string{"name": params.ClientInfo.Name}, &enrollment); err != nil {
		return err
	}
	h.mu.Lock()
	h.record = connectorRecord{Credential: enrollment.Credential, Principal: enrollment.Principal.ID}
	h.mu.Unlock()
	return h.save()
}

func (h *hostConnector) call(ctx context.Context, request *mcp.CallToolRequest, session *mcp.ClientSession) (*mcp.CallToolResult, error) {
	if request.Params.Name == "ownward_rules" {
		return session.CallTool(ctx, &mcp.CallToolParams{Name: request.Params.Name, Arguments: request.Params.Arguments})
	}
	if err := h.initialize(ctx, request); err != nil {
		return nil, err
	}
	var self contract.Principal
	if err := h.controlCall(ctx, "self", h.credential(), nil, &self); err != nil {
		var denied *controlError
		if !errors.As(err, &denied) || denied.Status != http.StatusForbidden {
			return nil, err
		}
		owner, recoverErr := h.ensureOwner(ctx, request.Session)
		if recoverErr != nil {
			return nil, recoverErr
		}
		accepted, recoverErr := hostConfirm(ctx, request.Session, "恢复这个智能体与当前信息体系的连接吗？旧连接将失效，使用范围需要重新确认。")
		if recoverErr != nil {
			return nil, recoverErr
		}
		if !accepted {
			return nil, errors.New("用户未批准恢复连接")
		}
		var restored struct {
			Credential string `json:"credential"`
		}
		h.mu.Lock()
		principal := h.record.Principal
		h.mu.Unlock()
		if err := h.controlCall(ctx, "reissue", owner, map[string]string{"id": principal}, &restored); err != nil {
			return nil, err
		}
		h.mu.Lock()
		h.record.Credential = restored.Credential
		h.record.Connected = false
		h.mu.Unlock()
		if err := h.save(); err != nil {
			return nil, err
		}
		if err := h.controlCall(ctx, "self", h.credential(), nil, &self); err != nil {
			return nil, err
		}
	}
	needed := contract.ReadPermission
	h.mu.Lock()
	newlyConnected := !h.record.Connected && len(self.Permissions) > 0
	if newlyConnected {
		h.record.Connected = true
	}
	h.mu.Unlock()
	if newlyConnected {
		if err := h.save(); err != nil {
			return nil, err
		}
	}
	switch request.Params.Name {
	case "ownward_create", "ownward_create_batch", "ownward_update", "ownward_semantic_work", "ownward_semantic_submit", "ownward_semantic_submit_batch":
		needed = contract.MaintainPermission
	}
	if !slices.Contains(self.Permissions, needed) && request.Params.Name != "ownward_manage" && request.Params.Name != "ownward_management_status" && request.Params.Name != "ownward_connections" {
		h.mu.Lock()
		connected := h.record.Connected
		h.mu.Unlock()
		if connected && len(self.Permissions) == 0 {
			return nil, errors.New("这个连接的访问权限已被收回")
		}
		id := fmt.Sprintf("connect-%s-%d", self.ID, time.Now().UnixNano())
		permissions := []contract.Permission{contract.ReadPermission}
		if needed == contract.MaintainPermission {
			permissions = append(permissions, needed)
		}
		proposal := contract.ManagementRequest{ID: id, Operation: "permissions", SubjectID: self.ID, Permissions: permissions}
		// 确认期间断线后沿用原申请，不制造另一条授权决定。
		h.mu.Lock()
		pending := slices.Clone(h.record.Pending)
		h.mu.Unlock()
		for _, previous := range pending {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_management_status", Arguments: map[string]string{"id": previous}})
			if err != nil || result.IsError {
				continue
			}
			var op contract.ManagementReceipt
			if decodeTool(result, &op) == nil && op.Status == "awaiting_approval" && op.Request.Operation == "permissions" && op.Request.SubjectID == self.ID && slices.Contains(op.Request.Permissions, needed) {
				proposal = op.Request
				id = op.Request.ID
				break
			}
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_manage", Arguments: proposal})
		if err != nil {
			return nil, err
		}
		if result.IsError {
			return result, nil
		}
		if _, err := h.confirm(ctx, request, id); err != nil {
			return nil, err
		}
		h.mu.Lock()
		h.record.Connected = true
		h.mu.Unlock()
		if err := h.save(); err != nil {
			return nil, err
		}
	}
	if request.Params.Name == "ownward_connections" && !slices.Contains(self.Permissions, contract.ManagePermission) {
		// 一次性查看由可信所有者连接执行，不改变当前接入者权限。
		accepted, err := hostConfirm(ctx, request.Session, "允许当前任务查看已接入的智能体及其权限吗？")
		if err != nil {
			return nil, err
		}
		if !accepted {
			return nil, errors.New("用户未批准查看接入者")
		}
		return h.ownerTool(ctx, request.Params.Name, request.Params.Arguments)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: request.Params.Name, Arguments: request.Params.Arguments})
	if err != nil || result.IsError {
		return result, err
	}
	if request.Params.Name == "ownward_manage" || request.Params.Name == "ownward_management_status" {
		var op contract.ManagementReceipt
		if err := decodeTool(result, &op); err != nil {
			return nil, err
		}
		if op.Status == "awaiting_approval" {
			op, err = h.confirm(ctx, request, op.Request.ID)
			if err != nil {
				return nil, err
			}
			result = receiptResult(op)
		}
		if op.Status == "cleaning" || op.Status == "stopping" {
			h.mu.Lock()
			if !slices.Contains(h.record.Pending, op.Request.ID) {
				h.record.Pending = append(h.record.Pending, op.Request.ID)
			}
			h.mu.Unlock()
			if err := h.save(); err != nil {
				return nil, err
			}
		}
	}
	if err := h.appendReceipts(ctx, session, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (h *hostConnector) confirm(ctx context.Context, request *mcp.CallToolRequest, id string) (contract.ManagementReceipt, error) {
	if err := h.remember(id); err != nil {
		return contract.ManagementReceipt{}, err
	}
	owner, err := h.ensureOwner(ctx, request.Session)
	if err != nil {
		return contract.ManagementReceipt{}, err
	}
	var preview struct {
		Message string `json:"message"`
	}
	if err := h.controlCall(ctx, "preview", owner, map[string]string{"id": id}, &preview); err != nil {
		return contract.ManagementReceipt{}, err
	}
	accept, err := hostConfirm(ctx, request.Session, preview.Message)
	if err != nil {
		return contract.ManagementReceipt{}, err
	}
	var op contract.ManagementReceipt
	if err := h.controlCall(ctx, "decide", owner, struct {
		ID     string `json:"id"`
		Accept bool   `json:"accept"`
	}{id, accept}, &op); err != nil {
		return op, err
	}
	if op.Status == "declined" {
		return op, errors.New("用户未批准该操作")
	}
	return op, nil
}

func hostConfirm(ctx context.Context, session *mcp.ServerSession, message string) (bool, error) {
	result, err := session.Elicit(ctx, &mcp.ElicitParams{Mode: "form", Message: message, RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"confirm": map[string]any{"type": "boolean", "title": "确认以上操作"}}, "required": []string{"confirm"}}})
	if err != nil {
		return false, fmt.Errorf("宿主尚未完成可信确认，原请求保持未执行: %w", err)
	}
	confirmed, _ := result.Content["confirm"].(bool)
	return result.Action == "accept" && confirmed, nil
}

func (h *hostConnector) controlCall(ctx context.Context, path, credential string, input, output any) error {
	method := http.MethodGet
	var data []byte
	var err error
	if input != nil {
		method = http.MethodPost
		data, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, h.descriptor.Endpoint+controlPrefix+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+h.descriptor.BearerToken)
	request.Header.Set(principalHeader, credential)
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("管理连接未完成: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &controlError{Status: response.StatusCode}
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return errors.New("管理入口返回格式无效")
	}
	return nil
}

type controlError struct{ Status int }

func (e *controlError) Error() string {
	return fmt.Sprintf("管理入口拒绝操作（HTTP %d）", e.Status)
}

func (h *hostConnector) remember(id string) error {
	h.mu.Lock()
	if !slices.Contains(h.record.Pending, id) {
		h.record.Pending = append(h.record.Pending, id)
	}
	h.mu.Unlock()
	return h.save()
}

func (h *hostConnector) ensureOwner(ctx context.Context, session *mcp.ServerSession) (string, error) {
	credential, err := h.owner()
	if err == nil {
		var p contract.Principal
		err = h.controlCall(ctx, "self", credential, nil, &p)
		if err == nil && slices.Contains(p.Permissions, contract.ManagePermission) {
			return credential, nil
		}
		var denied *controlError
		if err != nil && (!errors.As(err, &denied) || denied.Status != 403) {
			return "", err
		}
	}
	if err := h.restoreOwner(ctx, session); err != nil {
		return "", err
	}
	return h.owner()
}

func (h *hostConnector) restoreOwner(ctx context.Context, session *mcp.ServerSession) error {
	message := "使用当前受保护的系统账户恢复你的 Ownward 管理连接吗？资料和接入者身份保留，旧管理凭据失效，然后继续原任务。"
	if h.system == "" {
		message = "在当前系统账户下建立属于你的 Ownward 信息体系，并继续原任务吗？"
	}
	accepted, err := hostConfirm(ctx, session, message)
	if err != nil {
		return err
	}
	if !accepted {
		return errors.New("用户未批准建立管理连接")
	}
	proof, err := h.vault.Load(h.recoveryScope, "owner-recovery")
	if err != nil {
		return errors.New("当前系统账户无法打开受保护的所有者恢复入口")
	}
	var result struct {
		System string `json:"system_id"`
	}
	if err := h.controlCall(ctx, "recover", proof, struct{}{}, &result); err != nil {
		return err
	}
	h.system = result.System
	return nil
}

func decodeTool(result *mcp.CallToolResult, out any) error {
	if result.StructuredContent != nil {
		data, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return json.Unmarshal([]byte(text.Text), out)
		}
	}
	return errors.New("管理工具未返回结构化回执")
}

func receiptResult(op contract.ManagementReceipt) *mcp.CallToolResult {
	data, _ := json.Marshal(op)
	return &mcp.CallToolResult{StructuredContent: op, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
}

func (h *hostConnector) ownerTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	owner, err := h.owner()
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "ownward-owner-channel", Version: version}, nil)
	httpClient := &http.Client{Transport: bearerTransport{token: h.descriptor.BearerToken, base: http.DefaultTransport, credential: func() string { return owner }}}
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.descriptor.Endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

func (h *hostConnector) appendReceipts(ctx context.Context, session *mcp.ClientSession, result *mcp.CallToolResult) error {
	h.mu.Lock()
	pending := slices.Clone(h.record.Pending)
	h.mu.Unlock()
	var delivered []string
	for _, id := range pending {
		value, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_management_status", Arguments: map[string]string{"id": id}})
		if err != nil || value.IsError {
			continue
		}
		var op contract.ManagementReceipt
		if decodeTool(value, &op) != nil {
			continue
		}
		if op.Status == "completed" {
			result.Content = append(result.Content, receiptResult(op).Content...)
			delivered = append(delivered, id)
		} else if op.Status == "awaiting_approval" || op.Status == "declined" || op.Error != "" {
			result.Content = append(result.Content, receiptResult(op).Content...)
		}
	}
	if len(delivered) > 0 {
		h.mu.Lock()
		h.record.LastDelivered = slices.Clone(delivered)
		h.record.Pending = slices.DeleteFunc(h.record.Pending, func(id string) bool { return slices.Contains(delivered, id) })
		h.mu.Unlock()
		return h.save()
	}
	return nil
}
