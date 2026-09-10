package contract

import (
	"context"
	"errors"
)

type Permission string

const (
	ReadPermission     Permission = "read"
	MaintainPermission Permission = "maintain"
	ManagePermission   Permission = "manage"
)

type Principal struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Permissions      []Permission `json:"permissions"`
	Revision         uint64       `json:"revision"`
	CredentialDigest string       `json:"credential_digest,omitempty"`
}

type ManagementRequest struct {
	ID          string         `json:"id"`
	Operation   string         `json:"operation"`
	SubjectID   string         `json:"subject_id,omitempty"`
	Permissions []Permission   `json:"permissions,omitempty"`
	Targets     []AssetVersion `json:"targets,omitempty"`
}

type ManagementReceipt struct {
	Request          ManagementRequest `json:"request"`
	Requester        string            `json:"requester"`
	Approver         string            `json:"approver,omitempty"`
	ApproverRevision uint64            `json:"approver_revision,omitempty"`
	Status           string            `json:"status"`
	Affected         []AssetVersion    `json:"affected,omitempty"`
	Error            string            `json:"error,omitempty"`
}

type InformationControlState struct {
	SystemID         string              `json:"system_id"`
	OwnerID          string              `json:"owner_id"`
	Principals       []Principal         `json:"principals"`
	Operations       []ManagementReceipt `json:"operations,omitempty"`
	DeletionRevision uint64              `json:"deletion_revision"`
}

func (s InformationControlState) Validate() error {
	if s.SystemID == "" || s.OwnerID == "" {
		return errors.New("信息体系所有权无效")
	}
	seen := map[string]bool{}
	owner := false
	for _, p := range s.Principals {
		if p.ID == "" || p.Name == "" || p.Revision == 0 || seen[p.ID] {
			return errors.New("接入主体无效或重复")
		}
		seen[p.ID] = true
		for _, permission := range p.Permissions {
			if permission != ReadPermission && permission != MaintainPermission && permission != ManagePermission {
				return errors.New("授权能力无效")
			}
			if p.ID == s.OwnerID && permission == ManagePermission {
				owner = true
			}
		}
	}
	if !owner {
		return errors.New("所有者缺少管理权限")
	}
	operations := map[string]bool{}
	for _, op := range s.Operations {
		if op.Request.ID == "" || operations[op.Request.ID] || !seen[op.Requester] {
			return errors.New("管理操作身份无效或重复")
		}
		operations[op.Request.ID] = true
		switch op.Status {
		case "awaiting_approval", "approved", "declined", "stopping", "cleaning", "completed":
		default:
			return errors.New("管理操作状态无效")
		}
		switch op.Request.Operation {
		case "permissions":
			if !seen[op.Request.SubjectID] || len(op.Request.Targets) != 0 {
				return errors.New("授权目标无效")
			}
			if op.Status == "stopping" || op.Status == "cleaning" {
				return errors.New("授权操作不能进入遗忘状态")
			}
		case "forget":
			if err := (ChangeScope{Schema: AssetChangeScopeSchema, Assets: op.Request.Targets}).Validate(); err != nil {
				return err
			}
		default:
			return errors.New("管理操作类型无效")
		}
		if op.Status == "approved" || op.Status == "stopping" || op.Status == "cleaning" || op.Status == "completed" {
			if !seen[op.Approver] || op.ApproverRevision == 0 {
				return errors.New("管理操作缺少批准依据")
			}
		}
	}
	return nil
}

// CommitGuard 只覆盖确定性的本地提交，不得包含模型或网络调用。
type CommitGuard func(func() error) error
type commitGuardKey struct{}

func WithCommitGuard(ctx context.Context, guard CommitGuard) context.Context {
	return context.WithValue(ctx, commitGuardKey{}, guard)
}

func Commit(ctx context.Context, write func() error) error {
	if guard, ok := ctx.Value(commitGuardKey{}).(CommitGuard); ok {
		return guard(write)
	}
	return write()
}

type InformationManagement interface {
	Principals(context.Context) ([]Principal, error)
	Manage(context.Context, ManagementRequest) (ManagementReceipt, error)
	Receipt(context.Context, string) (ManagementReceipt, error)
}
