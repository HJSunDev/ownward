//go:build ownward_migration

// Package derivedcandidate binds one exact candidate binary to the generic
// offline derived-state lifecycle. It is not imported by the product runtime.
package derivedcandidate

import (
	"context"
	"errors"

	"github.com/HJSunDev/ownward/internal/capabilitylifecycle"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	compositionv1 "github.com/HJSunDev/ownward/manifests/compositions/v1"
)

type Collaborative struct {
	Vector contract.VectorCapability
}

var _ capabilitylifecycle.DerivedBuilder = (*Collaborative)(nil)

func (builder *Collaborative) RuntimeIdentity() capabilitylifecycle.DerivedRuntimeIdentity {
	if builder == nil {
		return capabilitylifecycle.DerivedRuntimeIdentity{}
	}
	manifest, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		return capabilitylifecycle.DerivedRuntimeIdentity{}
	}
	runtime, err := capabilitylifecycle.InspectDerivedRuntime(manifest)
	if err != nil {
		return capabilitylifecycle.DerivedRuntimeIdentity{}
	}
	return runtime
}

func (builder *Collaborative) Build(ctx context.Context, root, generation string, snapshot []domain.Information) (*derived.Store, error) {
	if err := builder.validate(); err != nil {
		return nil, err
	}
	return core.BuildIsolatedCollaborativeGeneration(ctx, builder.Vector, root, generation, snapshot)
}

func (builder *Collaborative) CatchUp(ctx context.Context, store *derived.Store, snapshot []domain.Information, scope contract.ChangeScope) error {
	if err := builder.validate(); err != nil {
		return err
	}
	return core.CatchUpIsolatedCollaborativeGeneration(ctx, builder.Vector, store, snapshot, scope)
}

func (builder *Collaborative) validate() error {
	if builder == nil || builder.Vector == nil {
		return errors.New("collaborative 派生候选构建器未完整绑定")
	}
	runtime := builder.RuntimeIdentity()
	space := builder.Vector.Space()
	if builder.Vector.Name() != runtime.VectorCapability || space.ID != runtime.VectorSpace || space.Dimensions != runtime.VectorDimensions {
		return errors.New("collaborative 派生候选向量能力或空间身份错绑")
	}
	return nil
}
