package contract

import "context"

// ProductRules is deliberately separate from a kernel implementation.
type ProductRules interface {
	Rules(context.Context) string
}
