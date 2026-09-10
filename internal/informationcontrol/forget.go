package informationcontrol

import (
	"context"
	"errors"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
)

type forgetKernel interface {
	StopUsing([]contract.AssetVersion, []contract.AssetVersion, string, func([]contract.AssetVersion) error) error
	CleanForgotten() error
}

func (p *Product) Manage(ctx context.Context, request contract.ManagementRequest) (contract.ManagementReceipt, error) {
	op, err := p.control.Propose(ctx, request)
	if err != nil {
		return contract.ManagementReceipt{}, err
	}
	if op.Status == "approved" {
		if err := p.execute(op); err != nil {
			return op, err
		}
	}
	return p.Receipt(ctx, request.ID)
}

// Decide 由宿主的独立可信管理通道调用，不向普通 MCP 调用者注册批准工具。
func (p *Product) Decide(ctx context.Context, id string, accept bool) (contract.ManagementReceipt, error) {
	op, err := p.control.Decide(ctx, id, accept)
	if err != nil {
		return contract.ManagementReceipt{}, err
	}
	if op.Status == "approved" {
		if err := p.execute(op); err != nil {
			return op, err
		}
	}
	return p.Receipt(ctx, id)
}

func (p *Product) Receipt(ctx context.Context, id string) (contract.ManagementReceipt, error) {
	return p.control.Receipt(ctx, id)
}

func (p *Product) execute(op contract.ManagementReceipt) error {
	p.workMu.Lock()
	defer p.workMu.Unlock()
	var err error
	op, err = p.control.operation(op.Request.ID)
	if err != nil {
		return err
	}
	if op.Status == "completed" || op.Status == "cleaning" {
		return nil
	}
	if op.Request.Operation == "permissions" {
		return p.control.ApplyPermissions(op.Request.ID)
	}
	kernel, ok := p.kernel.(forgetKernel)
	if !ok {
		return errors.New("当前内核未提供遗忘能力")
	}
	var recovered []contract.AssetVersion
	if op.Status == "stopping" {
		recovered = op.Affected
	}
	err = kernel.StopUsing(op.Request.Targets, recovered, op.Request.ID, func(affected []contract.AssetVersion) error { return p.control.StartForget(op.Request.ID, affected) })
	if err == nil {
		err = p.control.mark(op.Request.ID, "cleaning", nil)
	} else if current, readErr := p.control.operation(op.Request.ID); readErr == nil && current.Status == "stopping" {
		_ = p.control.mark(op.Request.ID, "stopping", err)
	}
	p.signal()
	return err
}

func (p *Product) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Product) cleanLoop() {
	defer close(p.done)
	delay := time.Second
	for {
		select {
		case <-p.stop:
			return
		case <-p.wake:
		}
		failed := false
		for _, op := range p.control.pending() {
			select {
			case <-p.stop:
				return
			default:
			}
			if op.Status == "stopping" {
				if err := p.execute(op); err != nil {
					failed = true
					continue
				}
			}
			p.workMu.Lock()
			kernel, ok := p.kernel.(forgetKernel)
			var err error
			if !ok {
				err = errors.New("当前内核未提供清理能力")
			} else {
				err = kernel.CleanForgotten()
			}
			if err == nil {
				err = p.control.mark(op.Request.ID, "completed", nil)
			}
			p.workMu.Unlock()
			if err != nil {
				failed = true
				_ = p.control.mark(op.Request.ID, "cleaning", err)
			}
		}
		if failed {
			timer := time.NewTimer(delay)
			select {
			case <-p.stop:
				timer.Stop()
				return
			case <-timer.C:
				p.signal()
			}
			delay = min(delay*2, 30*time.Second)
		} else {
			delay = time.Second
		}
	}
}

func (p *Product) Close() { p.closeOnce.Do(func() { close(p.stop); <-p.done }) }
