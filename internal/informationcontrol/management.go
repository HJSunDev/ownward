package informationcontrol

import (
	"context"
	"encoding/json"
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
		if len(r.Targets) > contract.MaxForgetTargets {
			return errors.New("单次遗忘最多包含 64 条资料")
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
	state := c.managementState(ctx, contract.ControlSelection{Principal: request.SubjectID, Operation: request.ID})
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
		return visibleManagement(state, op), nil
	}
	if request.Operation == "permissions" && request.SubjectID == state.InformationControl.OwnerID {
		return contract.ManagementReceipt{}, errors.New("不能撤销所有者的恢复权")
	}
	if request.SubjectRevision != 0 {
		found := false
		for _, subject := range state.InformationControl.Principals {
			if subject.ID == request.SubjectID && subject.Revision == request.SubjectRevision {
				found = true
			}
		}
		if !found {
			return contract.ManagementReceipt{}, contract.ErrOwnerRefresh
		}
	}
	op := contract.ManagementReceipt{Request: request, Requester: p.ID, Status: "awaiting_approval"}
	if slices.Contains(p.Permissions, contract.ManagePermission) {
		recordManagementApproval(state, &op, p)
	}
	state.InformationControl.Operations = append(state.InformationControl.Operations, op)
	if frozen(state) {
		// A frozen snapshot cannot accept a separate queue/decision commit.
		// Only an immediately authorized safety action can join its atomic
		// revocation/deletion commit. Do not report volatile work as accepted.
		if op.Status != "approved" || !frozenSafetyAction(state, op) {
			return contract.ManagementReceipt{}, ErrMoving
		}
		if c.deferred == nil {
			c.deferred = map[string]contract.ManagementReceipt{}
		}
		if previous, ok := c.deferred[request.ID]; ok {
			if previous.Requester != p.ID || !reflect.DeepEqual(previous.Request, request) {
				return contract.ManagementReceipt{}, ErrDenied
			}
			return visibleManagement(state, previous), nil
		}
		c.deferred[request.ID] = op
		return visibleManagement(state, op), nil
	}
	return visibleManagement(state, op), c.save(state)
}

// Decide 接受可信管理通道的决定，普通调用无法在请求中伪造批准者。
func (c *Control) Decide(ctx context.Context, id string, accept bool) (contract.ManagementReceipt, error) {
	return c.decideManagement(ctx, id, "", accept, false)
}

// DecideVersion checks the originally presented decision inside the same
// authority transaction as approval. Polling cannot renew a stale form.
func (c *Control) DecideVersion(ctx context.Context, id, decision string, accept bool) (contract.ManagementReceipt, error) {
	return c.decideManagement(ctx, id, decision, accept, true)
}

func (c *Control) decideManagement(ctx context.Context, id, decision string, accept, versioned bool) (contract.ManagementReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.managementState(ctx, contract.ControlSelection{Operation: id})
	p, err := principal(ctx, state, contract.ManagePermission)
	if err != nil {
		return contract.ManagementReceipt{}, err
	}
	for i := range state.InformationControl.Operations {
		op := &state.InformationControl.Operations[i]
		if op.Request.ID != id {
			continue
		}
		if !managementNeedsApproval(state, *op) {
			return visibleManagement(state, *op), nil
		}
		if (versioned && (decision == "" || decision != visibleManagement(state, *op).Decision)) || (!versioned && op.Status != "awaiting_approval") {
			return contract.ManagementReceipt{}, contract.ErrOwnerRefresh
		}
		if frozen(state) && (!accept || !frozenSafetyAction(state, *op)) {
			return contract.ManagementReceipt{}, ErrMoving
		}
		op.Status = "declined"
		if accept {
			recordManagementApproval(state, op, p)
		}
		if frozen(state) {
			if c.deferred == nil {
				c.deferred = map[string]contract.ManagementReceipt{}
			}
			c.deferred[id] = *op
			return visibleManagement(state, *op), nil
		}
		return visibleManagement(state, *op), c.save(state)
	}
	return contract.ManagementReceipt{}, errors.New("管理请求不存在")
}

func recordManagementApproval(state contract.ControlState, op *contract.ManagementReceipt, approver contract.Principal) {
	op.Status, op.Approver, op.ApproverRevision = "approved", approver.ID, approver.Revision
	if op.Request.Operation == "permissions" && op.Request.SubjectRevision == 0 {
		for _, p := range state.InformationControl.Principals {
			if p.ID == op.Request.SubjectID {
				op.ApprovedSubjectRevision = p.Revision
			}
		}
	}
}

func managementNeedsApproval(state contract.ControlState, op contract.ManagementReceipt) bool {
	return op.Status == "awaiting_approval" || (op.Status == "approved" &&
		(!approvalValid(state, op) || (op.Request.Operation == "permissions" && op.Request.SubjectRevision == 0 && op.ApprovedSubjectRevision == 0)))
}

func approvalValid(state contract.ControlState, op contract.ManagementReceipt) bool {
	if state.InformationControl == nil {
		return false
	}
	for _, p := range state.InformationControl.Principals {
		if p.ID == op.Approver && p.Revision == op.ApproverRevision && slices.Contains(p.Permissions, contract.ManagePermission) {
			return true
		}
	}
	return false
}

// Derive effective approval and the presentation identity without modifying
// the historical approval. Subject versions already fixed by the request
// still terminate as superseded through the normal execution path.
func visibleManagement(state contract.ControlState, op contract.ManagementReceipt) contract.ManagementReceipt {
	op.Decision = ""
	var approver, subject uint64
	for _, p := range state.InformationControl.Principals {
		if p.ID == op.Approver {
			approver = p.Revision
		}
		if p.ID == op.Request.SubjectID && op.Request.SubjectRevision == 0 {
			subject = p.Revision
		}
	}
	b, _ := json.Marshal(struct {
		Request                                                     contract.ManagementRequest
		Requester, Status, Approver                                 string
		ApprovalRevision, ApprovedSubject, CurrentApprover, Subject uint64
	}{op.Request, op.Requester, op.Status, op.Approver, op.ApproverRevision, op.ApprovedSubjectRevision, approver, subject})
	op.Decision = digest(string(b))
	if managementNeedsApproval(state, op) {
		op.Status = "awaiting_approval"
	}
	return op
}

func frozenSafetyAction(state contract.ControlState, op contract.ManagementReceipt) bool {
	if op.Request.Operation == "forget" {
		return true // scope is checked by the atomic stop-use barrier
	}
	for _, p := range state.InformationControl.Principals {
		if p.ID != op.Request.SubjectID {
			continue
		}
		if op.Request.SubjectRevision != 0 && op.Request.SubjectRevision != p.Revision {
			return false
		}
		for _, permission := range op.Request.Permissions {
			if !slices.Contains(p.Permissions, permission) {
				return false
			}
		}
		return true
	}
	return false
}

func (c *Control) ApplyPermissions(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.managementState(context.Background(), contract.ControlSelection{Operation: id})
	if err := managementReadable(state); err != nil {
		return err
	}
	if inactive(state) {
		return ErrInactive
	}
	for i := range state.InformationControl.Operations {
		op := &state.InformationControl.Operations[i]
		if op.Request.ID != id {
			continue
		}
		if op.Terminal() {
			return nil
		}
		if op.Status != "approved" || op.Request.Operation != "permissions" || managementNeedsApproval(state, *op) {
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
			expected := op.Request.SubjectRevision
			if expected == 0 {
				expected = op.ApprovedSubjectRevision
			}
			if p.Revision != expected {
				// A different decision already changed this connection. End this
				// request without applying stale rights or leaving an approved
				// operation that can neither execute nor be declined.
				if frozen(state) {
					return ErrMoving
				}
				op.Status = "superseded"
				op.Error = "接入权限已变化，本次调整未执行；请刷新后重新确认。"
				return c.save(state)
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
	state := c.managementState(context.Background(), contract.ControlSelection{Operation: id})
	if err := managementReadable(state); err != nil {
		return err
	}
	if inactive(state) {
		return ErrInactive
	}
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

// A definitive scope conflict is observed before deletion commits. Re-read
// the authority after that failed attempt rather than saving its mutated local
// stopping state: the deletion revision and frozen handoff must remain intact.
func (c *Control) supersedeForget(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.managementState(context.Background(), contract.ControlSelection{Operation: id})
	if state.ReadError != nil {
		return state.ReadError
	}
	if inactive(state) {
		return ErrInactive
	}
	if state.InformationControl == nil {
		return ErrDenied
	}
	for i := range state.InformationControl.Operations {
		op := &state.InformationControl.Operations[i]
		if op.Request.ID != id {
			continue
		}
		if op.Terminal() {
			return nil
		}
		if op.Status != "approved" || op.Request.Operation != "forget" || !approvalValid(state, *op) {
			return ErrDenied
		}
		if frozen(state) {
			return ErrMoving
		}
		op.Status = "superseded"
		op.Error = "遗忘目标已变化或不可用，本次遗忘未执行；请刷新后重新确认。"
		return c.save(state)
	}
	return errors.New("管理请求不存在")
}

func (c *Control) mark(id, status string, failure error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.managementState(context.Background(), contract.ControlSelection{Operation: id})
	if err := managementReadable(state); err != nil {
		return err
	}
	for i := range state.InformationControl.Operations {
		op := &state.InformationControl.Operations[i]
		if op.Request.ID != id {
			continue
		}
		if op.Terminal() {
			return nil
		}
		if op.Request.Operation != "forget" || (op.Status != "stopping" && op.Status != "cleaning") ||
			(status != "stopping" && status != "cleaning" && status != "completed") ||
			(op.Status == "cleaning" && status == "stopping") || (op.Status == "stopping" && status == "completed") {
			return ErrDenied
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
	// Receipts and both pending views describe durable authority only. A
	// safety action's frozen commit material is not an accepted queue item.
	state := c.selected(ctx, contract.ControlSelection{Operation: id})
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
		return visibleManagement(state, op), nil
	}
	return contract.ManagementReceipt{}, errors.New("管理请求不存在")
}

func (c *Control) pending() ([]contract.ManagementReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.selected(context.Background(), contract.ControlSelection{Pending: "cleaning"})
	if state.ReadError != nil {
		return nil, state.ReadError
	}
	if state.InformationControl == nil {
		return nil, nil
	}
	var result []contract.ManagementReceipt
	for _, op := range state.InformationControl.Operations {
		if op.Status == "stopping" || op.Status == "cleaning" {
			result = append(result, op)
		}
	}
	return result, nil
}

func (c *Control) operation(id string) (contract.ManagementReceipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.managementState(context.Background(), contract.ControlSelection{Operation: id})
	if err := managementReadable(state); err != nil {
		return contract.ManagementReceipt{}, err
	}
	if state.InformationControl != nil {
		for _, op := range state.InformationControl.Operations {
			if op.Request.ID == id {
				return op, nil
			}
		}
	}
	return contract.ManagementReceipt{}, errors.New("管理请求不存在")
}

func managementReadable(state contract.ControlState) error {
	if state.ReadError != nil {
		return state.ReadError
	}
	if state.InformationControl == nil {
		return ErrDenied
	}
	return nil
}

// Only immediately authorized frozen safety actions use this short-lived
// commit material. It is not a queue and is never reported as durable work.
func (c *Control) managementState(ctx context.Context, selection contract.ControlSelection) contract.ControlState {
	if op, ok := c.deferred[selection.Operation]; ok {
		selection.RelatedPrincipals = []string{op.Requester, op.Approver, op.Request.SubjectID}
	}
	state := c.selected(ctx, selection)
	c.mergeDeferred(&state, selection.Operation)
	return state
}

func (c *Control) releaseDeferred(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.deferred, id)
}

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
