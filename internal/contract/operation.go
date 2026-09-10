package contract

import (
	"context"
	"errors"

	"github.com/HJSunDev/ownward/internal/domain"
)

// OperationIdentity belongs to the connection, never to information content.
type OperationIdentity struct {
	ID         string `json:"id"`
	System     string `json:"system"`
	Principal  string `json:"principal"`
	Generation uint64 `json:"generation"`
	Kind       string `json:"kind"`
	Digest     string `json:"digest"`
}

type MutationOutcome struct {
	Asset AssetVersion `json:"asset,omitempty"`
	Error string       `json:"error,omitempty"`
}

type MutationReceipt struct {
	Operation OperationIdentity `json:"operation"`
	Results   []MutationOutcome `json:"results"`
}

// MutationAuthority commits changes and their replay facts in one durable record.
type MutationAuthority interface {
	OperationGeneration() uint64
	MutationReceipt(OperationIdentity) (MutationReceipt, bool, error)
	CommitMutation(MutationReceipt, []domain.Information, []uint64) error
}

var ErrOperationExpired = errors.New("操作接续窗口已过，请先核对已保存结果，不能自动重新执行")

type operationKey struct{}

func WithOperation(ctx context.Context, op OperationIdentity) context.Context {
	return context.WithValue(ctx, operationKey{}, op)
}
func Operation(ctx context.Context) (OperationIdentity, bool) {
	op, ok := ctx.Value(operationKey{}).(OperationIdentity)
	return op, ok
}
