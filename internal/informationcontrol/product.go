package informationcontrol

import (
	"context"
	"errors"
	"sync"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

// Product 是全部协议共用的授权边界；内核内部的提交沿用同一请求许可。
type Product struct {
	kernel         contract.ProductCapability
	control        *Control
	workMu         sync.Mutex
	wake           chan struct{}
	stop           chan struct{}
	done           chan struct{}
	closeOnce      sync.Once
	cleanupMu      sync.RWMutex
	relatedCleanup func() error
}

func NewProduct(kernel contract.ProductCapability, control *Control) *Product {
	p := &Product{kernel: kernel, control: control, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go p.cleanLoop()
	p.signal()
	return p
}
func authorized[T any](ctx context.Context, c *Control, permission contract.Permission, run func(context.Context) (T, error)) (T, error) {
	var zero T
	bound, finish, err := c.Begin(ctx, permission)
	if err != nil {
		return zero, err
	}
	value, runErr := run(bound)
	if err := finish(); err != nil {
		return zero, err
	}
	return value, runErr
}
func (p *Product) Rules(ctx context.Context) string { return p.kernel.Rules(ctx) }

func (p *Product) SetRelatedCleanup(clean func() error) {
	p.cleanupMu.Lock()
	p.relatedCleanup = clean
	p.cleanupMu.Unlock()
	p.signal()
}
func (p *Product) cleanRelated() error {
	p.cleanupMu.RLock()
	fn := p.relatedCleanup
	p.cleanupMu.RUnlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (p *Product) Create(ctx context.Context, input contract.CreateInput) (contract.MutationResult, error) {
	return authorized(ctx, p.control, contract.MaintainPermission, func(bound context.Context) (contract.MutationResult, error) {
		return p.kernel.Create(bound, input)
	})
}

func (p *Product) CreateBatch(ctx context.Context, input []contract.CreateInput) ([]contract.MutationBatchResult, error) {
	return authorized(ctx, p.control, contract.MaintainPermission, func(bound context.Context) ([]contract.MutationBatchResult, error) {
		return p.kernel.CreateBatch(bound, input)
	})
}

func (p *Product) Update(ctx context.Context, input contract.UpdateInput) (contract.MutationResult, error) {
	return authorized(ctx, p.control, contract.MaintainPermission, func(bound context.Context) (contract.MutationResult, error) {
		return p.kernel.Update(bound, input)
	})
}

func (p *Product) Read(ctx context.Context, id string) (domain.Information, error) {
	return authorized(ctx, p.control, contract.ReadPermission, func(bound context.Context) (domain.Information, error) {
		return p.kernel.Read(bound, id)
	})
}

func (p *Product) ReadEvidence(ctx context.Context, id string) (domain.Evidence, error) {
	return authorized(ctx, p.control, contract.ReadPermission, func(bound context.Context) (domain.Evidence, error) {
		return p.kernel.ReadEvidence(bound, id)
	})
}

func (p *Product) SearchEvidence(ctx context.Context, input contract.EvidenceSearchInput) ([]domain.EvidenceReference, error) {
	return authorized(ctx, p.control, contract.ReadPermission, func(bound context.Context) ([]domain.EvidenceReference, error) {
		return p.kernel.SearchEvidence(bound, input)
	})
}

func (p *Product) Search(ctx context.Context, input contract.SearchInput) ([]contract.SearchResult, error) {
	return authorized(ctx, p.control, contract.ReadPermission, func(bound context.Context) ([]contract.SearchResult, error) {
		return p.kernel.Search(bound, input)
	})
}

func (p *Product) Navigate(ctx context.Context, ids, types []string, depth, limit int) (contract.NavigationResult, error) {
	return authorized(ctx, p.control, contract.ReadPermission, func(bound context.Context) (contract.NavigationResult, error) {
		return p.kernel.Navigate(bound, ids, types, depth, limit)
	})
}

func (p *Product) SemanticWork(ctx context.Context, limit int) ([]semantics.Work, error) {
	return authorized(ctx, p.control, contract.MaintainPermission, func(bound context.Context) ([]semantics.Work, error) {
		return p.kernel.SemanticWork(bound, limit)
	})
}

func (p *Product) SemanticWorkFor(ctx context.Context, ids []string) ([]semantics.Work, error) {
	return authorized(ctx, p.control, contract.MaintainPermission, func(bound context.Context) ([]semantics.Work, error) {
		return p.kernel.SemanticWorkFor(bound, ids)
	})
}

func (p *Product) SubmitSemantic(ctx context.Context, input semantics.Submission) (contract.OrganizationState, error) {
	return authorized(ctx, p.control, contract.MaintainPermission, func(bound context.Context) (contract.OrganizationState, error) {
		return p.kernel.SubmitSemantic(bound, input)
	})
}

func (p *Product) SubmitSemanticBatch(ctx context.Context, input []semantics.Submission) ([]contract.SemanticSubmissionResult, error) {
	return authorized(ctx, p.control, contract.MaintainPermission, func(bound context.Context) ([]contract.SemanticSubmissionResult, error) {
		return p.kernel.SubmitSemanticBatch(bound, input)
	})
}

func (p *Product) Organization(string) (contract.OrganizationState, error) {
	return contract.OrganizationState{}, errors.New("状态查询需要认证上下文")
}
func (p *Product) OrganizationFor(ctx context.Context, id string) (contract.OrganizationState, error) {
	return authorized(ctx, p.control, contract.ReadPermission, func(context.Context) (contract.OrganizationState, error) { return p.kernel.Organization(id) })
}
func (p *Product) SemanticStatus() map[string]int { return nil }
func (p *Product) Principals(ctx context.Context) ([]contract.Principal, error) {
	return p.control.Principals(ctx)
}
