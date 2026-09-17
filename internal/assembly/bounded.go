package assembly

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func openBoundedWith(request Request, manifest composition.Manifest, resource resources) (*Runtime, error) {
	request, e := validateRequest(request, resource)
	if e != nil {
		return nil, e
	}
	verification, e := verifyComposition(manifest, request.ProductSemantics)
	if e != nil {
		return nil, e
	}
	if e = validateDeclaredCapabilities(manifest, request); e != nil {
		return nil, e
	}
	initial, e := authorityInitial(manifest)
	if e != nil {
		return nil, e
	}
	resourcebudget.LimitRuntime(20 * resourcebudget.MiB)
	budget, e := resourcebudget.New(20*resourcebudget.MiB, 4*resourcebudget.MiB)
	if e != nil {
		return nil, e
	}
	options := boundedstore.Options{Budget: budget}
	var vector contract.VectorCapability
	if request.ProductSemantics == Collaborative {
		vector, e = resource.openVector(request.VectorBundleDir, manifest)
		if e != nil {
			return nil, e
		}
		if vector == nil {
			return nil, errors.New("缺少已声明的向量能力")
		}
	}
	owned := false
	defer func() {
		if !owned && vector != nil {
			vector.Close()
		}
	}()
	if request.RestoreBackup != "" {
		native, e := boundedstore.IsDeploymentArchive(request.RestoreBackup)
		if e != nil {
			return nil, e
		}
		if native {
			_, e = boundedstore.RestoreArchive(context.Background(), request.RestoreBackup, request.DataDir, options)
		} else {
			e = resource.restore(request.RestoreBackup, request.DataDir, initial)
		}
		if e != nil {
			return nil, e
		}
	}
	initialize := func(ctx context.Context, s *boundedstore.Store, _ string) error {
		if _, e := s.OpenControlAuthority(ctx, initial); e != nil {
			return e
		}
		if vector != nil {
			return s.InitializeDeploymentSpace(ctx, vector.Space().ID)
		}
		return nil
	}
	space := ""
	if vector != nil {
		space = vector.Space().ID
	}
	store, e := boundedstore.OpenDeployment(context.Background(), request.DataDir, boundedstore.DeploymentOptions{Options: options, VectorSpace: space, Initialize: initialize})
	if e != nil {
		return nil, e
	}
	defer func() {
		if !owned {
			store.Close()
		}
	}()
	a, e := store.OpenControlAuthority(context.Background(), initial)
	if e != nil {
		return nil, e
	}
	state := a.ReadControl()
	if state.ReadError != nil {
		return nil, state.ReadError
	}
	if state.Access != nil && state.Access.Handoff != nil && state.Access.Handoff.Phase == "retired" {
		return nil, informationcontrol.ErrInactive
	}
	scratch := filepath.Join(request.DataDir, "scratch")
	if e = os.MkdirAll(scratch, 0700); e != nil {
		return nil, e
	}
	if e = resourcebudget.RecoverScratch(scratch); e != nil {
		return nil, e
	}
	kernel := &core.StreamingAssets{Store: store, Budget: budget, Scratch: scratch, DiskBytes: 256 * resourcebudget.MiB, Embedder: vector}
	c := informationcontrol.New(a)
	product := informationcontrol.NewProduct(kernel, c)
	r := &Runtime{streaming: kernel, product: product, userControl: c, verification: verification, semantics: request.ProductSemantics, vector: vector}
	r.backup = func(path string) error { return store.ExportArchive(context.Background(), path) }
	owned = true
	return r, nil
}

func (r *Runtime) Streaming() *core.StreamingAssets { return r.streaming }
func (r *Runtime) UnderlyingKernel() contract.ProductCapability {
	if r.streaming != nil {
		return r.streaming
	}
	return r.service
}
