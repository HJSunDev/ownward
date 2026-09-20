package core

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/semanticstream"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func (s *StreamingAssets) sourcePrefix(ctx context.Context, m contract.AssetMeta, limit int) (string, error) {
	r, e := s.Store.OpenContent(ctx, m.ID, m.Revision)
	if e != nil {
		return "", e
	}
	defer r.Close()
	b := bufio.NewReaderSize(r, streamjson.BufferBytes)
	out := make([]rune, 0, limit)
	for len(out) < limit {
		v, _, e := b.ReadRune()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
		out = append(out, v)
	}
	return string(out), nil
}

func (s *StreamingAssets) submitStreamingSemantic(ctx context.Context, node streamjson.Node) (OrganizationState, error) {
	var v boundedstore.OrganizationVersion
	var record derived.Record
	var normalized semantics.Submission
	var generation string
	var orgDocument *streamjson.Document
	defer func() {
		if orgDocument != nil {
			orgDocument.Close()
		}
	}()
	var input semantics.Submission
	var receipt semantics.SubmissionReceipt
	var assetID string
	if e := semanticOptional(node, "asset_id", &assetID, 4096); e != nil {
		return OrganizationState{}, e
	}
	var lease string
	if e := semanticOptional(node, "execution_lease", &lease, 256); e != nil {
		return OrganizationState{}, e
	}
	ctx = boundedstore.WithOrganizationExecution(ctx, assetID, lease)
	completedReplay := false
	if e := s.Store.CheckOrganizationExecution(ctx, assetID); e != nil {
		if !errors.Is(e, boundedstore.ErrOrganizationLease) {
			return OrganizationState{}, e
		}
		if e = s.Store.CheckOrganizationReplay(ctx, assetID); e != nil {
			return OrganizationState{}, e
		}
		completedReplay = true
	}
	writeOrg := func(w io.Writer) error {
		if orgDocument == nil {
			_, e := io.WriteString(w, "null")
			return e
		}
		return orgDocument.Root().Copy(w)
	}
	e := s.Store.WithSnapshot(ctx, func(ctx context.Context) error {
		var e error
		generation, _, e = s.Store.Generation(ctx)
		if e != nil {
			return e
		}
		v, e = s.Store.CurrentOrganization(ctx, generation, assetID)
		if e != nil {
			return e
		}
		record, e = s.Store.RecordHeader(ctx, v)
		if e != nil {
			return e
		}
		ref := record.SemanticWorkReference
		if ref == nil {
			return errors.New("语义工作不存在或已经过期")
		}
		m, e := s.Store.ReadAssetMeta(ctx, assetID, ref.Revision)
		if e != nil {
			return e
		}
		prefix, e := s.sourcePrefix(ctx, m, 513)
		if e != nil {
			return e
		}
		asset := domain.Information{ID: m.ID, Revision: m.Revision, Content: prefix, Kind: m.Kind, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
		var orgNode streamjson.Node
		input, orgNode, e = s.readSemanticHeader(ctx, node, asset)
		if e != nil {
			return e
		}
		if len(input.InputAssets) > 660 {
			return errors.New("语义调用的输入引用超过有界批量范围")
		}
		for _, refs := range [][]semantics.CandidateReference{ref.Candidates, record.InputAssets, input.InputAssets} {
			if e = s.Store.ReferencesCurrent(ctx, generation, refs); e != nil {
				return e
			}
		}
		if record.SemanticReceipt == nil && v.Snapshot != ref.TargetSnapshot {
			return errors.New("目标组织已被替换，请接续当前语义工作")
		}
		refs := append(append([]semantics.CandidateReference(nil), ref.Candidates...), input.InputAssets...)
		normalized, e = semantics.NormalizeSubmissionFields(*ref, asset, input, time.Now().UTC(), refs)
		if e != nil {
			return e
		}
		resolver := &semanticResolver{streamingTexts: &streamingTexts{ctx: ctx, s: s, bodies: map[string]*streamedBody{}, revisions: map[string]uint64{asset.ID: asset.Revision}}, generation: generation, references: map[string]semantics.CandidateReference{}, documents: map[string]*streamjson.Document{}}
		defer resolver.close()
		for _, r := range refs {
			resolver.revisions[r.ID] = r.Revision
			old, ok := resolver.references[r.ID]
			if !ok || old.OrganizationSnapshot == "" {
				resolver.references[r.ID] = r
			}
		}
		org, e := semanticstream.ReadOrganization(orgNode)
		if e != nil {
			return e
		}
		org, e = semanticstream.Normalize(ctx, asset.ID, asset.Revision, org, resolver)
		if e != nil {
			return e
		}
		if org != nil {
			orgDocument, e = streamjson.Build(ctx, s.Scratch, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes, func(w io.Writer) error { return org.Write(ctx, w) })
			if e != nil {
				return e
			}
			normalized.Analysis.Organization = &semantics.Organization{Schema: org.Schema, Snapshot: org.Snapshot}
		}
		receipt, e = s.streamReceipt(ctx, normalized, writeOrg)
		if e != nil {
			return e
		}
		if record.SemanticReceipt != nil && (record.SemanticReceipt.Validate() != nil || record.SemanticReceipt.SHA256 != receipt.SHA256) {
			return ErrSemanticConflict
		}
		record.Embedding, e = s.Store.RawEmbedding(ctx, v)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		return nil
	})
	if e != nil {
		return OrganizationState{}, e
	}
	if completedReplay {
		if record.SemanticReceipt == nil {
			return OrganizationState{}, boundedstore.ErrOrganizationLease
		}
		return organizationState(record), nil
	}
	if record.SemanticReceipt != nil && len(record.Embedding) > 0 {
		return organizationState(record), nil
	}
	// Model work never holds a SQLite read snapshot.
	var embeddingErr error
	if len(record.Embedding) == 0 && s.Embedder != nil {
		record.Embedding, embeddingErr = s.embedSemanticAnalysis(ctx, normalized.Analysis)
	}
	if record.SemanticReceipt != nil && len(record.Embedding) == 0 {
		return organizationState(record), nil
	}
	record.SemanticReceipt = &receipt
	record.GeneratedAt = receipt.AcceptedAt
	record.Provider = "semantic:" + normalized.Capability.ID + "/" + normalized.Capability.Version
	record.Analysis = normalized.Analysis
	record.InputAssets = append(record.InputAssets, normalized.InputAssets...)
	record.InputAssets = derived.Inputs(record)
	record.Status = "ready"
	record.Error = ""
	if normalized.Status == semantics.SubmissionUncertain {
		record.Status = "uncertain"
		record.Error = normalized.Uncertainty
	}
	if len(record.Embedding) == 0 {
		record.Status = "pending"
		record.Error = "向量仍待生成"
		if embeddingErr != nil {
			record.Error = embeddingErr.Error()
		}
	}
	// The staged version must still replace exactly the work normalized above.
	data, e := streamjson.Build(ctx, s.Scratch, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes, func(w io.Writer) error { return s.writeWithOrganization(ctx, w, record, writeOrg) })
	if e != nil {
		return OrganizationState{}, e
	}
	defer data.Close()
	next, e := s.Store.StageOrganization(ctx, generation, streamjson.RawSource{Node: data.Root()})
	if e != nil {
		return OrganizationState{}, e
	}
	if next.Expected != v.ID {
		return OrganizationState{}, errors.New("语义工作已被并发结果替换")
	}
	if e = s.Store.PublishOrganization(ctx, next); e != nil {
		return OrganizationState{}, e
	}
	return organizationState(record), nil
}

func (s *StreamingAssets) semanticSubmitTool(ctx context.Context, operation string, args streamjson.Node) (*contract.StreamResult, error) {
	key := "submission"
	if operation == "ownward_semantic_submit_batch" {
		key = "submissions"
	}
	node, ok, e := args.Field(key)
	if e != nil {
		return nil, e
	}
	if !ok {
		return nil, errors.New("缺少语义提交")
	}
	nodes := []streamjson.Node{node}
	if key == "submissions" {
		if node.Kind != '[' || node.Count < 1 || node.Count > 20 {
			return nil, errors.New("批量语义结果数量必须介于一和二十之间")
		}
		nodes = nil
		c := node.Children()
		for {
			n, e := c.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, e
			}
			nodes = append(nodes, n)
		}
	}
	results := make([]SemanticSubmissionResult, 0, len(nodes))
	for _, n := range nodes {
		var workID string
		e = semanticOptional(n, "work_id", &workID, 4096)
		var state OrganizationState
		if e == nil {
			state, e = s.submitStreamingSemantic(ctx, n)
		}
		if e != nil && key == "submission" {
			return nil, e
		}
		r := SemanticSubmissionResult{WorkID: workID, Organization: state}
		if e != nil {
			r.Error = e.Error()
		}
		results = append(results, r)
	}
	return s.buildRetrieval(ctx, func(ctx context.Context, w io.Writer) error {
		if key == "submission" {
			return writeJSON(w, map[string]any{"organization": results[0].Organization})
		}
		return writeJSON(w, map[string]any{"results": results})
	})
}

func (s *StreamingAssets) embedSemanticAnalysis(ctx context.Context, analysis semantics.Analysis) ([]float32, error) {
	chunks := semanticEmbeddingChunks(analysis)
	vectors := make([][]float32, 0, len(chunks))
	for offset := 0; offset < len(chunks); {
		end := boundedEmbeddingBatchEnd(chunks, offset)
		out, err := s.Embedder.EmbedDocuments(ctx, chunks[offset:end])
		if err != nil {
			return nil, err
		}
		if len(out) != end-offset {
			return nil, errors.New("本地向量能力返回数量无效")
		}
		vectors = append(vectors, out...)
		offset = end
	}
	return aggregateSemanticVectors(vectors)
}
