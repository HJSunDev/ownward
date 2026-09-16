package core

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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

func (s *StreamingAssets) detailsDocument(ctx context.Context, m contract.AssetMeta) (*streamjson.Document, error) {
	r, e := s.Store.OpenDetails(ctx, m.ID, m.Revision)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	return streamjson.Parse(ctx, s.Scratch, r, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes)
}
func (s *StreamingAssets) publishRecord(ctx context.Context, generation string, record derived.Record) (boundedstore.OrganizationVersion, error) {
	data, e := json.Marshal(record)
	if e != nil {
		return boundedstore.OrganizationVersion{}, e
	}
	v, e := s.Store.StageOrganization(ctx, generation, boundedstore.StringSource(data))
	if e != nil {
		return v, e
	}
	e = s.Store.PublishOrganization(ctx, v)
	return v, e
}
func (s *StreamingAssets) prepareStreamingWork(ctx context.Context, id string) (boundedstore.OrganizationVersion, error) {
	if s.Embedder == nil {
		return boundedstore.OrganizationVersion{}, errors.New("外部语义协作未配置向量能力")
	}
	generation, space, e := s.Store.Generation(ctx)
	if e != nil {
		return boundedstore.OrganizationVersion{}, e
	}
	if space != s.Embedder.Space().ID {
		return boundedstore.OrganizationVersion{}, errors.New("向量空间与当前世代不一致")
	}
	current, e := s.Store.CurrentOrganization(ctx, generation, id)
	if e == nil && current.WorkID != "" {
		return current, nil
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) && !errors.Is(e, boundedstore.ErrNotFound) {
		return current, e
	}
	m, e := s.Store.ReadAssetMeta(ctx, id, 0)
	if e != nil {
		return current, e
	}
	var vector []float32
	var embeddingError string
	if prepared, ok := shortEmbedding(ctx, id); ok && prepared.revision == m.Revision {
		vector = prepared.vector
		embeddingError = prepared.failure
	} else if m.ContentBytes <= semanticEmbeddingChunkBytes {
		r, e := s.Store.OpenContent(ctx, id, m.Revision)
		if e != nil {
			return current, e
		}
		data, e := io.ReadAll(r)
		r.Close()
		if e != nil {
			return current, e
		}
		values, e := s.Embedder.EmbedDocuments(ctx, []string{string(data)})
		if e != nil {
			embeddingError = e.Error()
		} else if len(values) == 1 {
			vector = values[0]
		} else {
			embeddingError = "本地向量能力返回数量无效"
		}
	} else {
		embeddingError = "长信息等待语义结果生成检索表示"
	}
	refs := make([]semantics.CandidateReference, 0, 12)
	e = s.Store.WithSnapshot(ctx, func(ctx context.Context) error {
		positions := map[string]int{}
		appendRef := func(id string, similarity float64) error {
			if id == m.ID {
				return nil
			}
			if at, ok := positions[id]; ok {
				refs[at].Similarity = max(refs[at].Similarity, similarity)
				return nil
			}
			if len(refs) == 12 {
				return nil
			}
			candidate, e := s.Store.ReadAssetMeta(ctx, id, 0)
			if errors.Is(e, boundedstore.ErrNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			ref := semantics.CandidateReference{ID: id, Revision: candidate.Revision, Similarity: similarity}
			org, e := s.Store.CurrentOrganization(ctx, generation, id)
			if e == nil {
				depends, e := s.Store.DependsOn(ctx, org, m.ID)
				if e != nil {
					return e
				}
				if !depends {
					ref.OrganizationSnapshot = org.Snapshot
				}
			} else if !errors.Is(e, sql.ErrNoRows) && !errors.Is(e, boundedstore.ErrNotFound) {
				return e
			}
			positions[id] = len(refs)
			refs = append(refs, ref)
			return nil
		}
		details, e := s.detailsDocument(ctx, m)
		if e != nil {
			return e
		}
		defer details.Close()
		if relations, ok, e := details.Root().Field("explicit_relations"); e != nil {
			return e
		} else if ok && relations.Kind == '[' {
			c := relations.Children()
			for {
				n, e := c.Next()
				if e == io.EOF {
					break
				}
				if e != nil {
					return e
				}
				target, e := fieldString(n, "target_id", 256)
				if e != nil {
					return e
				}
				if e = appendRef(target, 0); e != nil {
					return e
				}
			}
		}
		source := currentPart{store: s.Store, id: m.ID, revision: m.Revision}
		names, e := s.Store.NameSource(ctx, generation, source, 6)
		if e != nil {
			return e
		}
		for _, hit := range names {
			if e = appendRef(hit.ID, 0); e != nil {
				return e
			}
		}
		lexical, e := s.Store.LexicalSource(ctx, source, "", nil, 24)
		if e != nil {
			return e
		}
		for _, hit := range lexical {
			if e = appendRef(hit.ID, 0); e != nil {
				return e
			}
			if len(refs) >= 6 {
				break
			}
		}
		if len(vector) > 0 {
			hits, _, e := s.Store.VectorSearch(ctx, generation, space, vector, nil, 24)
			if e != nil {
				return e
			}
			for _, hit := range hits {
				if e = appendRef(hit.ID, hit.Score); e != nil {
					return e
				}
				if len(refs) == 12 {
					break
				}
			}
		}
		for _, hit := range lexical {
			if e = appendRef(hit.ID, 0); e != nil {
				return e
			}
			if len(refs) == 12 {
				break
			}
		}
		return nil
	})
	if e != nil {
		return current, e
	}
	var nonce [16]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return current, e
	}
	ref := semantics.WorkReference{Schema: semantics.WorkReferenceSchema, ID: "sw_" + hex.EncodeToString(nonce[:]), Generation: generation, AssetID: m.ID, Revision: m.Revision, Candidates: refs, CreatedAt: time.Now().UTC()}
	if e = ref.Validate(); e != nil {
		return current, e
	}
	record := derived.Record{AssetID: m.ID, AssetRevision: m.Revision, GeneratedAt: ref.CreatedAt, Provider: s.Embedder.Name(), Status: "pending", Error: embeddingError, SemanticWorkReference: &ref, InputsKnown: true, InputAssets: refs, EmbeddingSpace: space, Embedding: vector}
	return s.publishRecord(ctx, generation, record)
}

func (s *StreamingAssets) semanticWorkTool(ctx context.Context, args streamjson.Node) (*contract.StreamResult, error) {
	var input struct {
		Limit    int      `json:"limit"`
		AssetIDs []string `json:"asset_ids"`
	}
	if e := args.DecodeSmall(&input, 64*1024); e != nil {
		return nil, e
	}
	if len(input.AssetIDs) > 20 {
		return nil, errors.New("定向语义工作不能超过二十项")
	}
	ids := input.AssetIDs
	if len(ids) == 0 {
		if input.Limit == 0 {
			input.Limit = 1
		}
		var e error
		ids, e = s.Store.PendingAssets(ctx, input.Limit)
		if e != nil {
			return nil, e
		}
	}
	ctx = s.prepareShortEmbeddings(ctx, ids)
	var works []boundedstore.OrganizationVersion
	for _, id := range ids {
		v, e := s.prepareStreamingWork(ctx, id)
		if e != nil {
			return nil, e
		}
		record, e := s.Store.RecordHeader(ctx, v)
		if e != nil {
			return nil, e
		}
		if record.HasPendingSemanticWork() {
			works = append(works, v)
		}
	}
	return s.buildRetrieval(ctx, func(ctx context.Context, w io.Writer) error {
		io.WriteString(w, `{"work":[`)
		for i, v := range works {
			if i > 0 {
				io.WriteString(w, ",")
			}
			current, e := s.Store.CurrentOrganization(ctx, v.Generation, v.Asset)
			if e != nil {
				return e
			}
			if current.ID != v.ID {
				return errors.New("语义工作已变化")
			}
			record, e := s.Store.RecordHeader(ctx, v)
			if e != nil {
				return e
			}
			ref := record.SemanticWorkReference
			if ref == nil {
				return errors.New("语义工作引用缺失")
			}
			if e = s.Store.ReferencesCurrent(ctx, v.Generation, ref.Candidates); e != nil {
				return e
			}
			io.WriteString(w, "{")
			for i, f := range []struct {
				k string
				v any
			}{{"schema", semantics.WorkSchema}, {"id", ref.ID}, {"generation", ref.Generation}, {"created_at", ref.CreatedAt}, {"organization_schema", semantics.OrganizationSchema}, {"organization_instructions", semantics.OrganizationInstruction()}, {"target_snapshot", ref.TargetSnapshot}} {
				if i > 0 {
					io.WriteString(w, ",")
				}
				io.WriteString(w, `"`+f.k+`":`)
				if e = writeJSON(w, f.v); e != nil {
					return e
				}
			}
			io.WriteString(w, `,"asset":`)
			m, e := s.Store.ReadAssetMeta(ctx, ref.AssetID, ref.Revision)
			if e != nil {
				return e
			}
			if _, e = s.writeInformation(ctx, w, m); e != nil {
				return e
			}
			if ref.Previous != nil {
				io.WriteString(w, `,"previous_analysis":`)
				if e = writeJSON(w, ref.Previous); e != nil {
					return e
				}
			}
			io.WriteString(w, `,"candidates":[`)
			for j, c := range ref.Candidates {
				if j > 0 {
					io.WriteString(w, ",")
				}
				if e = s.writeSemanticCandidate(ctx, w, ref.Generation, c); e != nil {
					return e
				}
			}
			io.WriteString(w, "]}")
		}
		_, e := io.WriteString(w, "]}")
		return e
	})
}
func (s *StreamingAssets) writeSemanticCandidate(ctx context.Context, w io.Writer, generation string, c semantics.CandidateReference) error {
	m, e := s.Store.ReadAssetMeta(ctx, c.ID, c.Revision)
	if e != nil {
		return e
	}
	io.WriteString(w, `{"id":`)
	if e = writeJSON(w, c.ID); e != nil {
		return e
	}
	io.WriteString(w, `,"revision":`)
	writeJSON(w, c.Revision)
	io.WriteString(w, `,"semantic_similarity":`)
	writeJSON(w, c.Similarity)
	io.WriteString(w, `,"content":`)
	r, e := s.Store.OpenContent(ctx, c.ID, c.Revision)
	if e != nil {
		return e
	}
	e = streamjson.WriteString(ctx, w, r)
	r.Close()
	if e != nil {
		return e
	}
	details, e := s.detailsDocument(ctx, m)
	if e != nil {
		return e
	}
	defer details.Close()
	n, ok, e := details.Root().Field("contexts")
	if e != nil {
		return e
	}
	if ok {
		io.WriteString(w, `,"explicit_contexts":`)
		if e = n.Copy(w); e != nil {
			return e
		}
	}
	if c.OrganizationSnapshot != "" {
		v, e := s.Store.CurrentOrganization(ctx, generation, c.ID)
		if e != nil {
			return e
		}
		if v.Snapshot != c.OrganizationSnapshot {
			return errors.New("候选组织已变化")
		}
		io.WriteString(w, `,"organization":`)
		if e = s.Store.CopyOrganizationInventory(ctx, v, w); e != nil {
			return e
		}
	}
	_, e = io.WriteString(w, "}")
	return e
}
