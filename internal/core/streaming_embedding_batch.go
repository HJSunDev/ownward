package core

import (
	"context"
	"errors"
	"io"
)

type preparedEmbedding struct {
	revision uint64
	vector   []float32
	failure  string
}
type preparedEmbeddingKey struct{}

// Batch only the original short-source inputs. Long sources still wait for
// semantic organization; no truncation or new embedding strategy is applied.
func (s *StreamingAssets) prepareShortEmbeddings(ctx context.Context, ids []string) context.Context {
	if s.Embedder == nil {
		return ctx
	}
	generation, _, e := s.Store.Generation(ctx)
	if e != nil {
		return ctx
	}
	var keys, texts []string
	var revisions []uint64
	for _, id := range ids {
		if v, e := s.Store.CurrentOrganization(ctx, generation, id); e == nil && v.WorkID != "" {
			continue
		}
		m, e := s.Store.ReadAssetMeta(ctx, id, 0)
		if e != nil || m.ContentBytes > semanticEmbeddingChunkBytes {
			continue
		}
		r, e := s.Store.OpenContent(ctx, id, m.Revision)
		if e != nil {
			continue
		}
		data, e := io.ReadAll(r)
		r.Close()
		if e != nil {
			continue
		}
		keys = append(keys, id)
		revisions = append(revisions, m.Revision)
		texts = append(texts, string(data))
	}
	prepared := map[string]preparedEmbedding{}
	for i := 0; i < len(texts); {
		end := boundedEmbeddingBatchEnd(texts, i)
		vectors, e := s.Embedder.EmbedDocuments(ctx, texts[i:end])
		if e == nil && len(vectors) != end-i {
			e = errors.New("本地向量能力返回数量无效")
		}
		for j := i; j < end; j++ {
			v := preparedEmbedding{revision: revisions[j]}
			if e != nil {
				v.failure = e.Error()
			} else {
				v.vector = vectors[j-i]
			}
			prepared[keys[j]] = v
		}
		i = end
	}
	return context.WithValue(ctx, preparedEmbeddingKey{}, prepared)
}

func shortEmbedding(ctx context.Context, id string) (preparedEmbedding, bool) {
	values, _ := ctx.Value(preparedEmbeddingKey{}).(map[string]preparedEmbedding)
	v, ok := values[id]
	return v, ok
}
