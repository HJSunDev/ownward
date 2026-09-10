package informationcontrol

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/HJSunDev/ownward/internal/contract"
)

var ErrDenied = errors.New("该连接未获准执行此操作")

// Control 不另存状态；所有决定提交到现有权威控制修订。
type Control struct {
	mu        sync.Mutex
	authority contract.ControlAuthority
	deferred  map[string]contract.ManagementReceipt
	changed   chan struct{}
}

type credentialKey struct{}

// Authenticate 仅由可信连接器调用，凭据不来自工具参数。
func Authenticate(ctx context.Context, credential string) context.Context {
	return context.WithValue(ctx, credentialKey{}, digest(credential))
}

func New(authority contract.ControlAuthority) *Control { return &Control{authority: authority} }

func (c *Control) SystemID() string {
	state := c.authority.ReadControl()
	if state.InformationControl == nil {
		return ""
	}
	return state.InformationControl.SystemID
}

func randomID(prefix string) (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func digest(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// InitializeOwner 只能由部署适配的受保护初始化入口调用，不注册为普通工具。
func (c *Control) InitializeOwner(name string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	if state.InformationControl != nil {
		return "", errors.New("已有所有者，不能重新初始化")
	}
	if strings.TrimSpace(name) == "" {
		return "", errors.New("所有者名称不能为空")
	}
	system, err := randomID("ow_")
	if err != nil {
		return "", err
	}
	owner, err := randomID("p_")
	if err != nil {
		return "", err
	}
	token, err := randomID("")
	if err != nil {
		return "", err
	}
	state.Schema = contract.UserControlStateSchema
	state.InformationControl = &contract.InformationControlState{SystemID: system, OwnerID: owner, Principals: []contract.Principal{{ID: owner, Name: name, Revision: 1, CredentialDigest: digest(token), Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission, contract.ManagePermission}}}}
	if err := c.save(state); err != nil {
		return "", err
	}
	return token, nil
}

// RecoverOwner 的调用资格来自独立所有者通道，不能用失效凭据自证。
func (c *Control) RecoverOwner() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	if err := mutable(state); err != nil {
		return "", err
	}
	if state.InformationControl == nil {
		return "", errors.New("尚未初始化所有者")
	}
	token, err := randomID("")
	if err != nil {
		return "", err
	}
	for i := range state.InformationControl.Principals {
		p := &state.InformationControl.Principals[i]
		if p.ID == state.InformationControl.OwnerID {
			p.CredentialDigest = digest(token)
			p.Revision++
		}
	}
	if err := c.save(state); err != nil {
		return "", err
	}
	return token, nil
}

func (c *Control) Enroll(ctx context.Context, name string) (contract.Principal, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	if err := mutable(state); err != nil {
		return contract.Principal{}, "", err
	}
	if _, err := principal(ctx, state, contract.ManagePermission); err != nil {
		return contract.Principal{}, "", err
	}
	if strings.TrimSpace(name) == "" {
		return contract.Principal{}, "", errors.New("接入者名称不能为空")
	}
	id, err := randomID("p_")
	if err != nil {
		return contract.Principal{}, "", err
	}
	token, err := randomID("")
	if err != nil {
		return contract.Principal{}, "", err
	}
	p := contract.Principal{ID: id, Name: name, Revision: 1, CredentialDigest: digest(token)}
	state.InformationControl.Principals = append(state.InformationControl.Principals, p)
	if err := c.save(state); err != nil {
		return contract.Principal{}, "", err
	}
	p.CredentialDigest = ""
	return p, token, nil
}

func principal(ctx context.Context, state contract.ControlState, permission contract.Permission) (contract.Principal, error) {
	if inactive(state) {
		return contract.Principal{}, ErrInactive
	}
	hash, _ := ctx.Value(credentialKey{}).(string)
	if hash == "" || state.InformationControl == nil {
		return contract.Principal{}, ErrDenied
	}
	for _, p := range state.InformationControl.Principals {
		if p.CredentialDigest != hash {
			continue
		}
		if permission == "" || slices.Contains(p.Permissions, permission) {
			return p, nil
		}
	}
	return contract.Principal{}, ErrDenied
}

func (c *Control) save(state contract.ControlState) error {
	expected := state.Revision
	state.Revision++
	_, err := c.authority.CompareAndSwapControl(expected, state)
	if err == nil {
		if c.changed != nil {
			close(c.changed)
		}
		c.changed = make(chan struct{})
	}
	return err
}

func (c *Control) Changed() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.changed == nil {
		c.changed = make(chan struct{})
	}
	return c.changed
}

// Begin 将请求开始时的许可绑定到后续提交与交付；撤销再授权不能复活旧请求。
func (c *Control) Begin(ctx context.Context, permission contract.Permission) (context.Context, func() error, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	p, err := principal(ctx, state, permission)
	if err != nil {
		return ctx, nil, err
	}
	if permission == contract.MaintainPermission && frozen(state) {
		return ctx, nil, ErrMoving
	}
	if op, ok := contract.Operation(ctx); ok {
		op.System = state.InformationControl.SystemID
		op.Principal = p.ID
		ctx = contract.WithOperation(ctx, op)
	}
	for _, op := range state.InformationControl.Operations {
		if op.Status == "stopping" {
			return ctx, nil, errors.New("遗忘屏障正在恢复，请稍后继续")
		}
	}
	epoch := state.InformationControl.DeletionRevision
	ctx = contract.WithInformationSystem(ctx, state.InformationControl.SystemID)
	check := func() error {
		current := c.authority.ReadControl()
		if permission == contract.MaintainPermission && frozen(current) {
			return ErrMoving
		}
		now, err := principal(ctx, current, permission)
		if err != nil || now.ID != p.ID || now.Revision != p.Revision {
			return ErrDenied
		}
		if current.InformationControl.DeletionRevision != epoch {
			return errors.New("资料已发生遗忘，请重新取得当前信息")
		}
		return ctx.Err()
	}
	finish := func() error { c.mu.Lock(); defer c.mu.Unlock(); return check() }
	guard := func(write func() error) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if err := check(); err != nil {
			return err
		}
		return write()
	}
	return contract.WithCommitGuard(ctx, guard), finish, nil
}

func (c *Control) Principals(ctx context.Context) ([]contract.Principal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	if _, err := principal(ctx, state, contract.ManagePermission); err != nil {
		return nil, err
	}
	values := state.InformationControl.Principals
	for i := range values {
		values[i].CredentialDigest = ""
	}
	return values, nil
}

func (c *Control) Self(ctx context.Context) (contract.Principal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, err := principal(ctx, c.authority.ReadControl(), "")
	p.CredentialDigest = ""
	return p, err
}

// Reissue 由可信管理连接恢复既有主体，不用名称重新创建身份或恢复历史权限。
func (c *Control) Reissue(ctx context.Context, id string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	if err := mutable(state); err != nil {
		return "", err
	}
	if _, err := principal(ctx, state, contract.ManagePermission); err != nil {
		return "", err
	}
	if id == state.InformationControl.OwnerID {
		return "", ErrDenied
	}
	for i := range state.InformationControl.Principals {
		p := &state.InformationControl.Principals[i]
		if p.ID != id {
			continue
		}
		token, err := randomID("")
		if err != nil {
			return "", err
		}
		p.CredentialDigest = digest(token)
		p.Revision++
		p.Permissions = nil
		return token, c.save(state)
	}
	return "", errors.New("接入主体不在当前快照中")
}

func (c *Control) SetPermissions(ctx context.Context, id string, permissions []contract.Permission) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.authority.ReadControl()
	if _, err := principal(ctx, state, contract.ManagePermission); err != nil {
		return err
	}
	if id == state.InformationControl.OwnerID {
		return errors.New("不能撤销所有者的恢复权")
	}
	for i := range state.InformationControl.Principals {
		p := &state.InformationControl.Principals[i]
		if p.ID != id {
			continue
		}
		if frozen(state) {
			for _, permission := range permissions {
				if !slices.Contains(p.Permissions, permission) {
					return ErrMoving
				}
			}
			cancelHandoff(&state)
		}
		p.Permissions = slices.Clone(permissions)
		p.Revision++
		return c.save(state)
	}
	return fmt.Errorf("接入者不存在")
}
