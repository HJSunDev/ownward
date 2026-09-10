package core

import (
	"errors"
	"fmt"
	"sort"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/retrieval"
	"github.com/HJSunDev/ownward/internal/semantics"
)

// StopUsing 不调用模型：版本核对、删除屏障与内存可见性在返回前成立。
func (s *Service) StopUsing(targets, recoveredAffected []contract.AssetVersion, operationID string, persist func([]contract.AssetVersion) error) error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	deletion, ok := s.authority.(contract.AssetDeletion)
	if !ok {
		return errors.New("当前资产权威不支持遗忘")
	}
	deleted := map[string]bool{}
	for _, target := range targets {
		asset, exists := s.authority.ReadCurrent(target.ID)
		if (!exists && recoveredAffected == nil) || (exists && asset.Revision != target.Revision) {
			return errors.New("遗忘目标已变化，需重新核对")
		}
		deleted[target.ID] = true
	}
	var records []derived.Record
	if s.derivedStore != nil {
		var err error
		records, err = s.derivedStore.AllWithEmbeddings()
		if err != nil {
			return err
		}
	}
	affected := map[string]uint64{}
	for _, v := range recoveredAffected {
		affected[v.ID] = v.Revision
	}
	for _, record := range records {
		// 旧版未记录完整输入，不能把缺少证据当成无依赖；只失效派生，原文保留。
		if deleted[record.AssetID] || !record.InputsKnown {
			affected[record.AssetID] = record.AssetRevision
			continue
		}
		for _, input := range derived.Inputs(record) {
			if deleted[input.ID] {
				affected[record.AssetID] = record.AssetRevision
				break
			}
		}
	}
	versions := make([]contract.AssetVersion, 0, len(affected)+len(targets))
	for _, v := range targets {
		affected[v.ID] = v.Revision
	}
	for id, revision := range affected {
		versions = append(versions, contract.AssetVersion{ID: id, Revision: revision})
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].ID < versions[j].ID })
	if err := persist(versions); err != nil {
		return err
	}
	if err := deletion.DeleteAssets(targets); err != nil {
		return err
	}
	assets := s.authority.ListCurrent()
	s.index = retrieval.NewLexical(assets)
	if s.derivedStore == nil {
		return nil
	}
	kept := make([]derived.Record, 0, len(records))
	for _, record := range records {
		if _, removed := affected[record.AssetID]; !removed {
			kept = append(kept, record)
		}
	}
	s.semantic = derived.NewIndex(kept)
	var sanitized []derived.Record
	for _, asset := range assets {
		if _, invalid := affected[asset.ID]; !invalid {
			continue
		}
		candidates := s.semanticCandidates(asset, nil, s.semantic)
		work, err := semantics.NewWork(s.derivedStore.Generation()+"/forget/"+operationID, asset, candidates, nil, s.now())
		if err != nil {
			return err
		}
		reference, err := semantics.ReferenceWork(work)
		if err != nil {
			return err
		}
		record := derived.Record{AssetID: asset.ID, AssetRevision: asset.Revision, GeneratedAt: s.now(), Provider: "external-semantic-capability", Status: "pending", SemanticWorkReference: &reference, InputAssets: reference.Candidates, InputsKnown: true}
		sanitized = append(sanitized, record)
	}
	// 已删除项在旧派生日志里也以无正文状态遮蔽，物理清理另行完成。
	for _, target := range targets {
		sanitized = append(sanitized, derived.Record{AssetID: target.ID, AssetRevision: target.Revision, GeneratedAt: s.now(), Provider: "authority", Status: "pending"})
	}
	for start := 0; start < len(sanitized); start += 20 {
		if err := s.derivedStore.PutBatch(sanitized[start:min(start+20, len(sanitized))]); err != nil {
			return err
		}
	}
	for _, record := range sanitized {
		if !deleted[record.AssetID] {
			s.semantic.Upsert(record)
		}
	}
	return nil
}

// CleanForgotten 复制未受影响的派生结果，不进行全库语义重建。
func (s *Service) CleanForgotten() error {
	if err := s.authority.Compact(); err != nil {
		return err
	}
	if s.derivedStore == nil {
		return nil
	}
	s.stateMu.RLock()
	records, err := s.derivedStore.AllWithEmbeddings()
	assets := s.authority.ListCurrent()
	currentGeneration := s.derivedStore.Generation()
	s.stateMu.RUnlock()
	if err != nil {
		return err
	}
	stateDigest, err := recordSnapshotDigest(records)
	if err != nil {
		return err
	}
	digest, err := informationSnapshotDigest(assets)
	if err != nil {
		return err
	}
	live := map[string]uint64{}
	for _, asset := range assets {
		live[asset.ID] = asset.Revision
	}
	kept := make([]derived.Record, 0, len(records))
	present := map[string]bool{}
	for _, record := range records {
		if revision, exists := live[record.AssetID]; exists && revision == record.AssetRevision {
			kept = append(kept, record)
			present[record.AssetID] = true
		}
	}
	// 原文提交成功但派生写入中断时，补回无旧材料的待组织工作；清理不能依赖模型。
	for _, asset := range assets {
		if present[asset.ID] {
			continue
		}
		work, err := semantics.NewWork(currentGeneration+"/forget-recovery", asset, nil, nil, s.now())
		if err != nil {
			return err
		}
		ref, err := semantics.ReferenceWork(work)
		if err != nil {
			return err
		}
		kept = append(kept, derived.Record{AssetID: asset.ID, AssetRevision: asset.Revision, GeneratedAt: s.now(), Provider: "external-semantic-capability", Status: "pending", SemanticWorkReference: &ref, InputsKnown: true})
	}
	generation, err := derived.NewGenerationID(s.now())
	if err != nil {
		return err
	}
	next, err := derived.CreateGeneration(s.derivedStore.Root(), generation)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = next.Discard()
		}
	}()
	if err := next.StageGeneration(kept); err != nil {
		return err
	}
	space := ""
	if s.embedder != nil {
		space = s.embedder.Space().ID
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	latestRecords, err := s.derivedStore.AllWithEmbeddings()
	if err != nil {
		return err
	}
	latestState, err := recordSnapshotDigest(latestRecords)
	if err != nil {
		return err
	}
	latestAssets, err := informationSnapshotDigest(s.authority.ListCurrent())
	if err != nil {
		return err
	}
	if latestAssets != digest || latestState != stateDigest || s.derivedStore.Generation() != currentGeneration {
		return errors.New("资料在清理期间更新，将接续最新状态")
	}
	if err := s.derivedStore.CommitGeneration(next, derived.GenerationMetadata{AssetCount: len(assets), AssetSnapshot: digest, EmbeddingSpace: space}); err != nil {
		return err
	}
	committed = true
	s.semantic = derived.NewIndex(kept)
	if err := s.derivedStore.PurgeInactive(); err != nil {
		return fmt.Errorf("清理旧派生副本: %w", err)
	}
	return nil
}
