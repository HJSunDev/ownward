package contract

import (
	"context"

	"github.com/HJSunDev/ownward/internal/semantics"
)

// SemanticCapability keeps open-content understanding separate from the
// kernel and vector space. The current collaborative product realizes this
// boundary through semantic_work / semantic_submit rather than an in-process
// model provider; a later assembly may bind an in-process implementation.
type SemanticCapability interface {
	Identity() semantics.Capability
	Analyze(context.Context, semantics.Work) (semantics.Submission, error)
}
