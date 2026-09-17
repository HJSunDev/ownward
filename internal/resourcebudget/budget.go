package resourcebudget

import (
	"context"
	"errors"
	"sync"
)

const MiB int64 = 1024 * 1024

type contextKey struct{}

func WithContext(ctx context.Context, budget *Budget) context.Context {
	return context.WithValue(ctx, contextKey{}, budget)
}
func FromContext(ctx context.Context, fallback *Budget) *Budget {
	if b, ok := ctx.Value(contextKey{}).(*Budget); ok {
		return b
	}
	return fallback
}

// Budget 在分配工作区之前约束并发总量；控制操作保留独立额度。
type Budget struct {
	mu                              sync.Mutex
	limit, reserved, used, ordinary int64
	changed                         chan struct{}
}

func New(limit, reserved int64) (*Budget, error) {
	if limit <= 0 || reserved < 0 || reserved >= limit {
		return nil, errors.New("工作区预算无效")
	}
	return &Budget{limit: limit, reserved: reserved, changed: make(chan struct{})}, nil
}

func (b *Budget) Acquire(ctx context.Context, bytes int64, control bool) (func(), error) {
	maximum := b.limit
	if !control {
		maximum -= b.reserved
	}
	if bytes <= 0 || bytes > maximum {
		return nil, errors.New("操作工作区超过可用预算")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if b.used+bytes <= b.limit && (control || b.ordinary+bytes <= b.limit-b.reserved) {
			b.used += bytes
			if !control {
				b.ordinary += bytes
			}
			b.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					b.mu.Lock()
					b.used -= bytes
					if !control {
						b.ordinary -= bytes
					}
					close(b.changed)
					b.changed = make(chan struct{})
					b.mu.Unlock()
				})
			}, nil
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (b *Budget) Used() int64 { b.mu.Lock(); defer b.mu.Unlock(); return b.used }

// TryAcquire admits optional buffers without waiting on a caller's own work.
func (b *Budget) TryAcquire(bytes int64) (func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes <= 0 || bytes > b.limit-b.used || bytes > b.limit-b.reserved-b.ordinary {
		return nil, false
	}
	b.used += bytes
	b.ordinary += bytes
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.used -= bytes
			b.ordinary -= bytes
			close(b.changed)
			b.changed = make(chan struct{})
			b.mu.Unlock()
		})
	}, true
}
