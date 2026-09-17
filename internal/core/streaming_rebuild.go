package core

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func (s *StreamingAssets) rebuildStreaming(ctx context.Context) (failure error) {
	if s.Embedder == nil {
		return nil
	}
	if !s.rebuildMu.TryLock() {
		return errors.New("已有重建正在进行")
	}
	defer s.rebuildMu.Unlock()
	if _, _, e := s.Store.Generation(ctx); errors.Is(e, sql.ErrNoRows) {
		if _, e = s.Store.InitializeGeneration(ctx, s.Embedder.Space().ID); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	before, e := s.Store.RetrievalStamp(ctx)
	if e != nil {
		return e
	}
	id, e := s.Store.BeginRebuild(ctx, s.Embedder.Space().ID)
	if e != nil {
		return e
	}
	committed := false
	defer func() {
		if !committed {
			failure = errors.Join(failure, s.Store.AbandonRebuild(context.WithoutCancel(ctx), id))
		}
	}()
	cursor := ""
	for {
		page, e := s.Store.ScanAssets(ctx, cursor, 4096)
		if e != nil {
			return e
		}
		for _, m := range page.Items {
			if e = s.rebuildStreamingAsset(ctx, before.Generation, id, m); e != nil {
				return e
			}
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if e = s.Store.FinishRebuild(ctx, id, before); e != nil {
		return e
	}
	committed = true
	return nil
}

func (s *StreamingAssets) rebuildStreamingAsset(ctx context.Context, old, next string, m contract.AssetMeta) error {
	s.deliveryMu.RLock()
	defer s.deliveryMu.RUnlock()
	ctx, leave, e := s.Store.BeginForeground(ctx)
	if e != nil {
		return e
	}
	defer leave()
	release, e := s.Budget.Acquire(ctx, 4*resourcebudget.MiB, false)
	if e != nil {
		return e
	}
	defer release()
	work, _ := resourcebudget.New(4*resourcebudget.MiB, 0)
	ctx = resourcebudget.WithContext(ctx, work)
	r := derived.Record{AssetID: m.ID, AssetRevision: m.Revision, GeneratedAt: time.Now().UTC(), Provider: s.Embedder.Name(), Status: "pending", InputsKnown: true}
	var doc *streamjson.Document
	defer func() {
		if doc != nil {
			doc.Close()
		}
	}()
	v, e := s.Store.CurrentOrganization(ctx, old, m.ID)
	if e == nil {
		r, e = s.Store.RecordHeader(ctx, v)
		if e != nil {
			return e
		}
		reader, e := s.Store.OpenOrganization(ctx, v)
		if e != nil {
			return e
		}
		doc, e = streamjson.Parse(ctx, s.Scratch, reader, work, s.DiskBytes)
		reader.Close()
		if e != nil {
			return e
		}
		if r.EmbeddingSpace == s.Embedder.Space().ID {
			r.Embedding, e = s.Store.RawEmbedding(ctx, v)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
	} else if !errors.Is(e, sql.ErrNoRows) && !errors.Is(e, boundedstore.ErrNotFound) {
		return e
	}
	if len(r.Embedding) == 0 {
		var chunks []string
		if r.HasSemanticResult() {
			chunks = semanticEmbeddingChunks(r.Analysis)
		} else if m.ContentBytes <= semanticEmbeddingChunkBytes {
			reader, e := s.Store.OpenContent(ctx, m.ID, m.Revision)
			if e != nil {
				return e
			}
			data, e := io.ReadAll(reader)
			reader.Close()
			if e != nil {
				return e
			}
			chunks = []string{string(data)}
		}
		if len(chunks) > 0 {
			var vectors [][]float32
			for at := 0; at < len(chunks); {
				end := boundedEmbeddingBatchEnd(chunks, at)
				out, e := s.Embedder.EmbedDocuments(ctx, chunks[at:end])
				if e != nil {
					return e
				}
				if len(out) != end-at {
					return errors.New("重建向量数量不一致")
				}
				vectors = append(vectors, out...)
				at = end
			}
			r.Embedding, e = aggregateSemanticVectors(vectors)
			if e != nil {
				return e
			}
		}
	}
	r.EmbeddingSpace = s.Embedder.Space().ID
	if r.HasSemanticResult() {
		r.Status = "ready"
		r.Error = ""
		if r.SemanticReceipt.Status == semantics.SubmissionUncertain {
			r.Status = "uncertain"
			r.Error = r.SemanticReceipt.Uncertainty
		}
	} else if r.SemanticWorkReference != nil {
		copy := *r.SemanticWorkReference
		copy.Generation = next
		r.SemanticWorkReference = &copy
	}
	if len(r.Embedding) == 0 {
		r.Status = "pending"
		r.Error = "长信息等待语义结果生成检索表示"
	}
	writeOrg := func(w io.Writer) error {
		if doc != nil {
			a, ok, e := doc.Root().Field("analysis")
			if e != nil {
				return e
			}
			if ok {
				n, ok, e := a.Field("organization")
				if e != nil {
					return e
				}
				if ok {
					return n.Copy(w)
				}
			}
		}
		_, e := io.WriteString(w, "null")
		return e
	}
	data, e := streamjson.Build(ctx, s.Scratch, work, s.DiskBytes, func(w io.Writer) error { return s.writeWithOrganization(ctx, w, r, writeOrg) })
	if e != nil {
		return e
	}
	defer data.Close()
	v, e = s.Store.StageOrganization(ctx, next, streamjson.RawSource{Node: data.Root()})
	if e != nil {
		return e
	}
	return s.Store.InstallRebuildOrganization(ctx, v)
}
