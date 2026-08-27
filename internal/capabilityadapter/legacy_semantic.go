package capabilityadapter

import (
	"context"
	"errors"
	"strings"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
)

// LegacySemanticProvider preserves the old combined provider only for
// constructor equivalence and existing organized callers. Active assembly and
// kernel-generation boundaries consume the returned stable ports.
func LegacySemanticProvider(provider semantics.Provider) (contract.SemanticCapability, contract.VectorCapability) {
	if provider == nil {
		provider = semantics.Heuristic{}
	}
	return legacySemantic{provider: provider}, legacySemanticVector{provider: provider}
}

type legacySemantic struct {
	provider semantics.Provider
}

func (value legacySemantic) Identity() semantics.Capability {
	return semantics.Capability{ID: value.provider.Name(), Version: "legacy-provider-v1", Execution: "in-process"}
}

func (value legacySemantic) Analyze(ctx context.Context, work semantics.Work) (semantics.Submission, error) {
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

type legacySemanticVector struct {
	provider semantics.Provider
}

func (value legacySemanticVector) Name() string {
	return value.provider.Name()
}

func (value legacySemanticVector) Space() embedding.Space {
	return embedding.Space{ID: "organized:" + strings.TrimSpace(value.provider.Name()), Dimensions: semantics.DefaultEmbeddingDimensions}
}

func (value legacySemanticVector) EmbedDocuments(ctx context.Context, inputs []string) ([][]float32, error) {
	return value.provider.Embed(ctx, inputs)
}

func (value legacySemanticVector) EmbedQuery(ctx context.Context, input string) ([]float32, error) {
	values, err := value.provider.Embed(ctx, []string{input})
	if err != nil {
		return nil, err
	}
	if len(values) != 1 {
		return nil, errors.New("语义向量能力返回数量无效")
	}
	return values[0], nil
}

func (legacySemanticVector) Close() error {
	return nil
}
