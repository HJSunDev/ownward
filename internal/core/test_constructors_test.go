package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/authorityport"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
)

// These helpers keep core behavior fixtures concise. They are compiled only
// into tests and still use the same stable ports as product assembly.
type ownedTestService struct {
	*Service
	store *assetlog.Store
}

func (service *ownedTestService) Close() error {
	first := service.Service.Close()
	if service.store != nil {
		if err := service.store.Close(); first == nil {
			first = err
		}
		service.store = nil
	}
	return first
}

func newTestBasic(t *testing.T, store *assetlog.Store) *ownedTestService {
	t.Helper()
	authority, _ := authorityport.Bind(store)
	service, _ := NewWithAuthority(authority)
	return &ownedTestService{Service: service, store: store}
}

func newTestOrganized(t *testing.T, store *assetlog.Store, state *derived.Store, provider semantics.Provider) (*ownedTestService, error) {
	t.Helper()
	authority, err := authorityport.Bind(store)
	if err != nil {
		return nil, err
	}
	semantic, vector := adaptProvider(provider)
	service, err := NewOrganizedWithCapabilities(authority, state, semantic, vector)
	if err != nil {
		return nil, err
	}
	return &ownedTestService{Service: service, store: store}, nil
}

func adaptProvider(provider semantics.Provider) (contract.SemanticCapability, contract.VectorCapability) {
	if provider == nil {
		provider = semantics.Heuristic{}
	}
	return testSemanticCapability{provider: provider}, testVectorCapability{provider: provider}
}

type testSemanticCapability struct{ provider semantics.Provider }

func (value testSemanticCapability) Identity() semantics.Capability {
	return semantics.Capability{ID: value.provider.Name(), Version: "test-provider-v1", Execution: "in-process"}
}

func (value testSemanticCapability) Analyze(ctx context.Context, work semantics.Work) (semantics.Submission, error) {
	analysis, err := value.provider.Analyze(ctx, work.Asset, work.Candidates)
	if err != nil {
		return semantics.Submission{}, err
	}
	return semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID,
		Revision: work.Asset.Revision, Capability: value.Identity(), Status: semantics.SubmissionComplete,
		Analysis: analysis,
	}, nil
}

type testVectorCapability struct{ provider semantics.Provider }

func (value testVectorCapability) Name() string { return value.provider.Name() }
func (value testVectorCapability) Space() embedding.Space {
	return embedding.Space{ID: "test:" + strings.TrimSpace(value.provider.Name()), Dimensions: semantics.DefaultEmbeddingDimensions}
}
func (value testVectorCapability) EmbedDocuments(ctx context.Context, inputs []string) ([][]float32, error) {
	return value.provider.Embed(ctx, inputs)
}
func (value testVectorCapability) EmbedQuery(ctx context.Context, input string) ([]float32, error) {
	values, err := value.provider.Embed(ctx, []string{input})
	if err != nil {
		return nil, err
	}
	if len(values) != 1 {
		return nil, errors.New("test vector capability returned an invalid count")
	}
	return values[0], nil
}
func (testVectorCapability) Close() error { return nil }

func newTestCollaborative(t *testing.T, store *assetlog.Store, state *derived.Store, vector embedding.Provider) (*ownedTestService, error) {
	t.Helper()
	authority, err := authorityport.Bind(store)
	if err != nil {
		return nil, err
	}
	service, err := NewCollaborativeWithAuthority(authority, state, vector)
	if err != nil {
		return nil, err
	}
	return &ownedTestService{Service: service, store: store}, nil
}
