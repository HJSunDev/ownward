package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HJSunDev/ownward/internal/codexplugin"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type organizationHost struct {
	host         *hostConnector
	profile      codexplugin.OrganizationProfile
	call         func(context.Context, string, any) (*mcp.CallToolResult, error)
	refreshRoute func(context.Context) error
	tool         *mcp.Tool
	journal      *organizationJournal
	root         string
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}
	wake         chan struct{}
	ready        atomic.Bool
	broken       atomic.Bool
	mu           sync.Mutex
	active       map[string]bool
	demand       []string
	overflow     bool
	status       string
}

func (h *hostConnector) attachOrganization(ctx context.Context, initialize *mcp.InitializeResult, tools []*mcp.Tool, call func(context.Context, string, any) (*mcp.CallToolResult, error), refreshRoute ...func(context.Context) error) func() {
	path := os.Getenv("OWNWARD_ORGANIZATION_PROFILE")
	if path == "" || initialize == nil || initialize.Capabilities == nil {
		return func() {}
	}
	var capability struct {
		Version int    `json:"version"`
		Mode    string `json:"mode"`
	}
	capabilityJSON, _ := json.Marshal(initialize.Capabilities.Experimental["ownward.deferred-organization"])
	if json.Unmarshal(capabilityJSON, &capability) != nil || capability.Version != 1 || capability.Mode != contract.DeferredOrganizationV1 {
		return func() {}
	}
	p, e := codexplugin.ReadOrganizationProfile(path)
	if e != nil {
		return func() {}
	}
	var submit *mcp.Tool
	for _, t := range tools {
		if t.Name == "ownward_semantic_submit" {
			submit = t
		}
	}
	if submit == nil {
		return func() {}
	}
	root, e := os.UserCacheDir()
	if e != nil {
		return func() {}
	}
	root = filepath.Join(root, "Ownward", "organization")
	j, e := openOrganizationJournal(root)
	if e != nil {
		return func() {}
	}
	child, cancel := context.WithCancel(ctx)
	o := &organizationHost{host: h, profile: p, tool: submit, call: call, root: root, journal: j, ctx: child, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), active: map[string]bool{}, status: "等待宿主就绪"}
	if len(refreshRoute) > 0 {
		o.refreshRoute = refreshRoute[0]
	}
	h.organization = o
	go o.loop()
	var stopped sync.Once
	return func() { stopped.Do(func() { cancel(); <-o.done; j.close() }) }
}

func (o *organizationHost) event(session, event string) {
	o.mu.Lock()
	if event == "UserPromptSubmit" {
		if o.active[session] || len(o.active) < 128 {
			o.active[session] = true
		} else {
			o.status = "宿主会话容量已满"
			o.ready.Store(false)
			o.overflow = true
		}
	}
	if event == "Stop" || event == "Interrupt" || event == "SessionEnd" || event == "SessionClear" {
		delete(o.active, session)
	}
	active := len(o.active) > 0 || o.overflow
	// Existing admitted work owns isolated capacity and is not restarted for an
	// unrelated request; foreground activity prevents dispatching another task.
	o.mu.Unlock()
	ctx, cancel := context.WithTimeout(o.ctx, time.Second)
	defer cancel()
	if o.journal.heartbeat(ctx, active) != nil {
		o.broken.Store(true)
		o.ready.Store(false)
	}
	select {
	case o.wake <- struct{}{}:
	default:
	}
}
func (o *organizationHost) setStatus(s string) { o.mu.Lock(); o.status = s; o.mu.Unlock() }
func (o *organizationHost) state() string      { o.mu.Lock(); defer o.mu.Unlock(); return o.status }
func (o *organizationHost) pause(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-o.ctx.Done():
		return false
	case <-o.wake:
		return true
	case <-t.C:
		return true
	}
}
func (o *organizationHost) jobs(ctx context.Context, in contract.OrganizationJobRequest) (contract.OrganizationJobResult, error) {
	var out contract.OrganizationJobResult
	r, e := o.call(ctx, "ownward_semantic_jobs", in)
	if e == nil {
		if r.IsError {
			e = errors.New("组织待办访问被拒绝")
		} else {
			e = decodeTool(r, &out)
		}
	}
	return out, e
}

func (o *organizationHost) loop() {
	defer close(o.done)
	// Heartbeat records foreground activity across all participating local connectors.
	heartDone := make(chan struct{})
	go func() {
		defer close(heartDone)
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-o.ctx.Done():
				return
			case <-t.C:
				o.mu.Lock()
				active := len(o.active) > 0 || o.overflow
				o.mu.Unlock()
				if o.journal.heartbeat(o.ctx, active) != nil {
					o.broken.Store(true)
					o.ready.Store(false)
				}
			}
		}
	}()
	defer func() { o.cancel(); <-heartDone }()
	cursor := ""
	claimRequest := ""
	claimAsset, claimAfter := "", ""
	probed := false
	for o.ctx.Err() == nil {
		o.mu.Lock()
		overflow := o.overflow
		o.mu.Unlock()
		if overflow || o.broken.Load() {
			o.ready.Store(false)
			if !o.pause(5 * time.Second) {
				return
			}
			continue
		}
		if o.host.credential() == "" {
			if !o.pause(time.Second) {
				return
			}
			continue
		}
		var self contract.Principal
		var authErr error
		// 只在领取前刷新可信路由；旧地址失效不能挡住已记录的接替地址。
		if o.refreshRoute != nil {
			authErr = o.refreshRoute(o.ctx)
		}
		if authErr == nil {
			o.host.routeMu.RLock()
			authErr = o.host.controlCall(o.ctx, "self", o.host.credential(), nil, &self)
			o.host.routeMu.RUnlock()
		}
		if authErr != nil {
			o.ready.Store(false)
			o.setStatus("组织待办保留；授权或连接不可用")
			if !o.pause(5 * time.Second) {
				return
			}
			continue
		}
		permitted := false
		for _, v := range self.Permissions {
			permitted = permitted || v == contract.MaintainPermission
		}
		if !permitted {
			o.ready.Store(false)
			o.setStatus("组织待办保留；无维护权限")
			if !o.pause(5 * time.Second) {
				return
			}
			continue
		}
		if !probed {
			probeLock, e := acquireServiceStartupLock(filepath.Join(o.root, "machine.lock"), 0)
			if e != nil {
				if !o.pause(time.Second) {
					return
				}
				continue
			}
			e = codexplugin.ProbeOrganization(o.ctx, o.profile, o.tool)
			probeLock.release()
			if e != nil {
				o.ready.Store(false)
				o.setStatus("组织执行器未就绪；保留原写入路径")
				if !o.pause(30 * time.Second) {
					return
				}
				continue
			}
			probed = true
		}
		if e := codexplugin.OrganizationCapacity(o.profile); e != nil {
			o.ready.Store(false)
			o.setStatus("本机资源不足；待办保留，新增资料沿用原路径")
			if !o.pause(5 * time.Second) {
				return
			}
			continue
		}
		o.ready.Store(true)
		active, e := o.journal.foreground(o.ctx)
		o.mu.Lock()
		demand := ""
		if len(o.demand) > 0 {
			demand = o.demand[0]
		}
		o.mu.Unlock()
		if e != nil || (active && demand == "") {
			if e != nil {
				o.broken.Store(true)
				o.ready.Store(false)
				o.setStatus("宿主执行元数据不可用；保留原路径与组织待办")
			}
			if !o.pause(time.Second) {
				return
			}
			continue
		}
		// A single OS-backed reservation covers the whole process tree and model
		// use on this machine, regardless of connector count or selected pool.
		lock, e := acquireServiceStartupLock(filepath.Join(o.root, "machine.lock"), 0)
		if e != nil {
			if !o.pause(time.Second) {
				return
			}
			continue
		}
		if claimRequest == "" {
			claimRequest = connectionID()
			claimAsset, claimAfter = demand, cursor
			if claimAsset != "" {
				claimAfter = ""
			}
		}
		out, e := o.jobs(o.ctx, contract.OrganizationJobRequest{Action: "claim", RequestID: claimRequest, AssetID: claimAsset, AfterAssetID: claimAfter, Background: true, LeaseSeconds: 60})
		if e == nil {
			claimRequest = ""
		}
		if e != nil || out.Claim == nil {
			lock.release()
			if e == nil && claimAsset != "" {
				status, statusErr := o.call(o.ctx, "ownward_status", map[string]any{"id": claimAsset})
				var value struct{ Organization contract.OrganizationState }
				if statusErr == nil && (status.IsError || (decodeTool(status, &value) == nil && (value.Organization.Status == "ready" || value.Organization.Status == "uncertain"))) {
					o.mu.Lock()
					if len(o.demand) > 0 && o.demand[0] == claimAsset {
						o.demand = o.demand[1:]
					}
					o.mu.Unlock()
				}
			}
			if e == nil && cursor == "" {
				o.jobs(o.ctx, contract.OrganizationJobRequest{Action: "wait", WaitSeconds: 5})
			}
			cursor = ""
			o.setStatus("等待可执行组织待办")
			if !o.pause(5 * time.Second) {
				return
			}
			continue
		}
		lease := *out.Claim
		// Resolve an ambiguous earlier claim with its original identity, then
		// yield unrelated work to a newly arrived foreground dependency.
		if active && claimAsset == "" && lease.AssetID != demand {
			o.jobs(o.ctx, contract.OrganizationJobRequest{Action: "release", AssetID: lease.AssetID, Lease: lease.Lease})
			lock.release()
			continue
		}
		cursor = lease.AssetID
		retry := o.run(lease, self)
		lock.release()
		if claimAsset != "" {
			// 领取不代表完成；仍可恢复的前台依赖保留，轮转避免挡住其他依赖。
			o.mu.Lock()
			if len(o.demand) > 0 && o.demand[0] == claimAsset {
				o.demand = o.demand[1:]
				if retry {
					o.demand = append(o.demand, claimAsset)
				}
			}
			o.mu.Unlock()
			if retry && !o.pause(time.Second) {
				return
			}
		}
	}
}

func (o *organizationHost) run(lease contract.OrganizationLease, self contract.Principal) bool {
	ctx, cancel := context.WithCancel(o.ctx)
	defer cancel()
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, e := o.jobs(ctx, contract.OrganizationJobRequest{Action: "renew", AssetID: lease.AssetID, Lease: lease.Lease, LeaseSeconds: 60}); e != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-renewDone
		release, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		o.jobs(release, contract.OrganizationJobRequest{Action: "release", AssetID: lease.AssetID, Lease: lease.Lease})
	}()
	o.host.mu.Lock()
	scope := o.host.system + ":" + self.ID
	o.host.mu.Unlock()
	preparation := fmt.Sprintf("prepare:%s:%d:%s:%d", lease.AssetID, lease.Revision, lease.Generation, self.Revision)
	allowed, _, e := o.journal.begin(ctx, scope, preparation, o.profile)
	if e != nil || !allowed {
		if e != nil {
			o.broken.Store(true)
			o.ready.Store(false)
		}
		o.setStatus("组织准备预算已用尽；待办保留并继续其他资料")
		return false
	}
	work, e := o.work(ctx, lease)
	if e != nil {
		o.journal.save(ctx, scope, preparation, "preparation_failed", 0, codexplugin.OrganizationUsage{})
		o.setStatus("组织材料暂不可用；待办保留")
		return true
	}
	if _, e = o.journal.db.ExecContext(ctx, "DELETE FROM executions WHERE scope=? AND work=?", scope, preparation); e != nil {
		o.broken.Store(true)
		o.ready.Store(false)
		return false
	}
	if string(work) == "[]" {
		o.setStatus("已复用内核接受的组织结果")
		return false
	}
	hash := sha256.Sum256(work)
	key := hex.EncodeToString(hash[:])
	allowed, prior, e := o.journal.begin(ctx, scope, key, o.profile)
	if e != nil || !allowed {
		if e != nil {
			o.broken.Store(true)
			o.ready.Store(false)
		}
		o.setStatus("组织恢复预算已用尽；待办保留并继续其他资料")
		return false
	}
	p := o.profile
	p.MaxTokens -= prior
	o.setStatus("正在组织；原文仍可读取")
	accepted := false
	usage, e := codexplugin.RunOrganization(ctx, p, work, o.tool, func(ctx context.Context, args json.RawMessage) (*mcp.CallToolResult, bool, error) {
		current, e := o.work(ctx, lease)
		if e != nil {
			return nil, false, e
		}
		if sha256.Sum256(current) != hash {
			return nil, false, errors.New("组织来源已变化，重新领取有效材料")
		}
		var input map[string]json.RawMessage
		if e = json.Unmarshal(args, &input); e != nil {
			return nil, false, e
		}
		var sub map[string]json.RawMessage
		if e = json.Unmarshal(input["submission"], &sub); e != nil {
			return nil, false, e
		}
		var id string
		if json.Unmarshal(sub["asset_id"], &id) != nil || id != lease.AssetID {
			return nil, false, errors.New("组织提交越出当前资产")
		}
		// The host carries execution identity; it does not supply semantic judgments.
		sub["execution_lease"], _ = json.Marshal(lease.Lease)
		input["submission"], _ = json.Marshal(sub)
		r, e := o.call(ctx, "ownward_semantic_submit", input)
		if e == nil && !r.IsError {
			var result struct{ Organization contract.OrganizationState }
			if decodeTool(r, &result) == nil {
				// 成功提交的 pending 表示语义已接受、向量待补；不再要求模型响应。
				accepted = result.Organization.Status == "ready" || result.Organization.Status == "uncertain" || result.Organization.Status == "pending"
			}
		}
		return r, accepted, e
	}, func(u codexplugin.OrganizationUsage) error {
		return o.journal.save(ctx, scope, key, "running", prior, u)
	})
	status := "incomplete"
	if accepted {
		status = "accepted"
	} else if e != nil {
		status = "failed"
	}
	save, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if o.journal.save(save, scope, key, status, prior, usage) != nil {
		o.broken.Store(true)
		o.ready.Store(false)
	}
	if accepted {
		o.setStatus("组织结果已被内核接受")
	} else {
		o.setStatus("本项组织未完成；保留待办与累计用量，继续其他资料")
	}
	return !o.broken.Load()
}

func (o *organizationHost) work(ctx context.Context, lease contract.OrganizationLease) (json.RawMessage, error) {
	r, e := o.call(ctx, "ownward_semantic_work", map[string]any{"asset_ids": []string{lease.AssetID}, "lease": lease.Lease})
	if e != nil {
		return nil, e
	}
	if r.IsError {
		return nil, errors.New("组织来源不可用")
	}
	var out struct {
		Work json.RawMessage `json:"work"`
	}
	if e = decodeTool(r, &out); e != nil {
		return nil, e
	}
	if len(out.Work) == 0 || len(out.Work) > 8<<20 {
		return nil, errors.New("组织材料超出宿主单任务资源边界")
	}
	return out.Work, nil
}
