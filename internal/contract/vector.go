package contract

import (
	"context"

	"github.com/HJSunDev/ownward/internal/embedding"
)

// VectorCapability is the existing document/query vector port. The space
// identity is a declared direct dependency of every consumer.
type VectorCapability interface {
	Name() string
	Space() embedding.Space
	EmbedDocuments(context.Context, []string) ([][]float32, error)
	EmbedQuery(context.Context, string) ([]float32, error)
	Close() error
}
