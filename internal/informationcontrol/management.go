package informationcontrol

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/HJSunDev/ownward/internal/contract"
)

func validateRequest(r contract.ManagementRequest) error {
	if strings.TrimSpace(r.ID) == "" || len(r.ID) > 128 {
		return errors.New("操作标识无效")
	}
	switch r.Operation {
	case "permissions":
		if r.SubjectID == "" || len(r.Targets) != 0 {
			return errors.New("授权目标无效")
		}
		seen := map[contract.Permission]bool{}
		for _, p := range r.Permissions {
			if (p != contract.ReadPermission && p != contract.MaintainPermission && p != contract.ManagePermission) || seen[p] {
				return errors.New("授权能力无效或重复")
			}
			seen[p] = true
		}
		if seen[contract.MaintainPermission] && !seen[contract.ReadPermission] {
			return errors.New("维护资料需要读取权限")
		}
	case "forget":
		if r.SubjectID != "" || len(r.Permissions) != 0 {
			return errors.New("遗忘请求包含无关授权参数")
		}
		return (contract.ChangeScope{Schema: contract.AssetChangeScopeSchema, Assets: r.Targets}).Validate()
	default:
		return errors.New("不支持的管理操作")
	}
	return nil
}

func (c *Control) Propose(ctx context.Context, request contract.ManagementRequest) (contract.ManagementReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := validateRequest(request); err != nil {
		return contract.ManagementReceipt{}, err
	}
	// JSON 会省略空集合；同一决定不因 nil/空切片或排列方式不同而变化。
	request.Permissions = slices.Clone(request.Permissions)
	slices.Sort(request.Permissions)
	if len(request.Permissions) == 0 {
		request.Permissions = nil
	}
	request.Targets = slices.Clone(request.Targets)
	slices.SortFunc(request.Targets, func(a, b contract.AssetVersion) int { return strings.Compare(a.ID, b.ID) })
	if len(request.Targets) == 0 {
		request.Targets = nil
	}
	state := c.authority.ReadControl()
	p, err := principal(ctx, state, "")
	if err != nil {
		return contract.ManagementReceipt{}, err
	}
	for _, op := range state.InformationControl.Operations {
		if op.Request.ID != request.ID {
			continue
		}
		if op.Requester != p.ID || !reflect.DeepEqual(op.Request, request) {
			return contract.ManagementReceipt{}, errors.New("同一操作标识不能更换请求者或内容")
		}
		return op, nil
	}
	op := contract.ManagementReceipt{Request: request, Requester: p.ID, Status: "awaiting_approval"}
	if slices.Contains(p.Permissions, contract.ManagePermission) {
		op.Status = "approved"
		op.Approver = p.ID
		op.ApproverRevision = p.Revision
	}
	state.InformationControl.Operations = append(state.InformationControl.Operations, op)
	if frozen(state) {
		if c.deferred == nil {
			c.deferred = map[string]contract.ManagementReceipt{}
		}
		if previous, ok := c.deferred[request.ID]; ok {
			if previous.Requester != p.ID || !reflect.DeepEqual(previous.Request, request) {
				return contract.ManagementReceipt{}, ErrDenied
			}
			return previous, nil
		}
		c.deferred[request.ID] = op
		return op, nil
	}
	return op, c.save(state)
}

// Decide 接受可信管理通道的决定，普通调用无法在请求中伪造批准者。
func (c *Control) Decide(ctx context.Context, id string, accept bool) (contract.ManagementReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	c.mergeDeferred(&state, id)
	p, err := principal(ctx, state, contract.ManagePermission)
	if err != nil {
		return contract.ManagementReceipt{}, err
	}
	for i := range state.InformationControl.Operations {
		op := &state.InformationControl.Operations[i]
		if op.Request.ID != id {
			continue
		}
		if op.Status != "awaiting_approval" && !(op.Status == "approved" && !approvalValid(state, *op)) {
			return *op, nil
		}
		op.Status = "declined"
		if accept {
			op.Status = "approved"
			op.Approver = p.ID
			op.ApproverRevision = p.Revision
		}
		if frozen(state) {
			if c.deferred == nil {
				c.deferred = map[string]contract.ManagementReceipt{}
			}
			c.deferred[id] = *op
			return *op, nil
		}
		return *op, c.save(state)
	}
	return contract.ManagementReceipt{}, errors.New("管理请求不存在")
}

func approvalValid(state contract.ControlState, op contract.ManagementReceipt) bool {
	for _, p := range state.InformationControl.Principals {
		if p.ID == op.Approver && p.Revision == op.ApproverRevision && slices.Contains(p.Permissions, contract.ManagePermission) {
			return true
		}
	}
	return false
}

func (c *Control) ApplyPermissions(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	if inactive(state) {
		return ErrInactive
	}
	c.mergeDeferred(&state, id)
	for i := range state.InformationControl.Operations {
		op := &state.InformationControl.Operations[i]
		if op.Request.ID != id {
			continue
		}
		if op.Status == "completed" {
			return nil
		}
		if op.Status != "approved" || op.Request.Operation != "permissions" || !approvalValid(state, *op) {
			return ErrDenied
		}
		if op.Request.SubjectID == state.InformationControl.OwnerID {
			return errors.New("不能撤销所有者的恢复权")
		}
		for j := range state.InformationControl.Principals {
			p := &state.InformationControl.Principals[j]
			if p.ID != op.Request.SubjectID {
				continue
			}
			if frozen(state) {
				for _, permission := range op.Request.Permissions {
					if !slices.Contains(p.Permissions, permission) {
						return ErrMoving
					}
				}
				cancelHandoff(&state)
			}
			p.Permissions = slices.Clone(op.Request.Permissions)
			p.Revision++
			op.Status = "completed"
			return c.save(state)
		}
		return errors.New("接入者不存在")
	}
	return errors.New("管理请求不存在")
}

// StartForget 与内核的版本核对处于同一短提交边界；已提交的删除不因随后撤销而回滚。
func (c *Control) StartForget(id string, affected []contract.AssetVersion) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	if inactive(state) {
		return ErrInactive
	}
	c.mergeDeferred(&state, id)
	for i := range state.InformationControl.Operations {
		op := &state.InformationControl.Operations[i]
		if op.Request.ID != id {
			continue
		}
		if op.Status == "stopping" || op.Status == "cleaning" || op.Status == "completed" {
			return nil
		}
		if op.Status != "approved" || op.Request.Operation != "forget" || !approvalValid(state, *op) {
			return ErrDenied
		}
		op.Affected = slices.Clone(affected)
		if frozen(state) {
			cancelHandoff(&state)
		}
		op.Status = "stopping"
		state.InformationControl.DeletionRevision++
		return c.save(state)
	}
	return errors.New("管理请求不存在")
}

func (c *Control) mark(id, status string, failure error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	for i := range state.InformationControl.Operations {
		op := &state.InformationControl.Operations[i]
		if op.Request.ID != id {
			continue
		}
		op.Status = status
		op.Error = ""
		if failure != nil {
			op.Error = "本地存储清理未完成，服务将重试"
			if errors.Is(failure, os.ErrPermission) {
				op.Error = "存储权限阻止清理，请恢复该目录的写入权限"
			}
			if errors.Is(failure, os.ErrNotExist) {
				op.Error = "受控存储文件缺失，清理保留待恢复状态"
			}
		}
		return c.save(state)
	}
	return errors.New("管理请求不存在")
}

func (c *Control) Receipt(ctx context.Context, id string) (contract.ManagementReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	c.mergeDeferred(&state, id)
	p, err := principal(ctx, state, "")
	if err != nil {
		return contract.ManagementReceipt{}, err
	}
	for _, op := range state.InformationControl.Operations {
		if op.Request.ID != id {
			continue
		}
		if p.ID != op.Requester && !slices.Contains(p.Permissions, contract.ManagePermission) {
			return contract.ManagementReceipt{}, ErrDenied
		}
		// 管理回执只有身份与状态，没有原文或凭据；普通连接不取得依赖清理详情。
		if !slices.Contains(p.Permissions, contract.ManagePermission) {
			op.Affected = nil
		}
		return op, nil
	}
	return contract.ManagementReceipt{}, errors.New("管理请求不存在")
}

func (c *Control) pending() []contract.ManagementReceipt {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	if state.InformationControl == nil {
		return nil
	}
	var result []contract.ManagementReceipt
	for _, op := range state.InformationControl.Operations {
		if op.Status == "stopping" || op.Status == "cleaning" {
			result = append(result, op)
		}
	}
	return result
}

func (c *Control) operation(id string) (contract.ManagementReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	c.mergeDeferred(&state, id)
	if state.InformationControl != nil {
		for _, op := range state.InformationControl.Operations {
			if op.Request.ID == id {
				return op, nil
			}
		}
	}
	return contract.ManagementReceipt{}, errors.New("管理请求不存在")
}

// Unapproved work cannot invalidate a frozen snapshot; the connector durably
// retains the request until its authorized decision can join a control commit.
func (c *Control) mergeDeferred(s *contract.ControlState, id string) {
	op, ok := c.deferred[id]
	if !ok || s.InformationControl == nil {
		return
	}
	for i, current := range s.InformationControl.Operations {
		if current.Request.ID == id {
			if current.Status == "awaiting_approval" || current.Status == "approved" {
				s.InformationControl.Operations[i] = op
			}
			return
		}
	}
	s.InformationControl.Operations = append(s.InformationControl.Operations, op)
}
