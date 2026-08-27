package contract

import "context"

// KernelLifecycle is the lifecycle currently exposed by the kernel. Candidate
// generation preparation, promotion, and rollback remain later migration work.
type KernelLifecycle interface {
	Maintain(context.Context, bool) (map[string]int, error)
	Close() error
}
