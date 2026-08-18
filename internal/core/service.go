package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/retrieval"
	"github.com/HJSunDev/ownward/internal/semantics"
)

const CollaborationRules = `# Ownward 协作规则

Ownward 只保存属于用户且可长期复用的信息。创建信息时保留完整原意；只有信息的含义或适用性依赖场景时才附加场景。不要把某个智能体的临时工作状态写入 Ownward。

开始任务时，先检索可能影响当前判断的个人信息。检索先取得低成本线索与关联，再按需读取完整内容；简单问题可以一次检索，复杂问题应依据累计证据继续检索、沿关系扩展或调整方向，直到证据足以支持当前目的。真实工作中形成的错误教训、解决经验和可复用路径应在确认后补充，并标明适用场景。
`

type Service struct {
	store        *assetlog.Store
	derivedStore *derived.Store
	index        *retrieval.Lexical
	semantic     *derived.Index
	provider     semantics.Provider
	now          func() time.Time
	mutationMu   [256]sync.Mutex
}

type OrganizationState struct {
	Status   string `json:"status"`
	Provider string `json:"provider,omitempty"`
	Error    string `json:"error,omitempty"`
}

type MutationResult struct {
	Information  domain.Information `json:"information"`
	Organization OrganizationState  `json:"organization"`
}

type CreateInput struct {
	Kind      domain.InformationKind
	Content   string
	Contexts  []domain.Context
	Relations []domain.ExplicitRelation
	Source    domain.Source
}

type UpdateInput struct {
	ID               string
	ExpectedRevision uint64
	Kind             *domain.InformationKind
	Content          *string
	Contexts         *[]domain.Context
	Relations        *[]domain.ExplicitRelation
	Source           *domain.Source
}

type SearchInput struct {
	Query                    string
	Contexts                 []domain.Context
	Limit                    int
	DisableRelationExpansion bool
}

type SearchResult struct {
	ID       string                 `json:"id"`
	Kind     domain.InformationKind `json:"kind"`
	Summary  string                 `json:"summary"`
	Contexts []domain.Context       `json:"contexts,omitempty"`
	Score    float64                `json:"score"`
	Signals  []string               `json:"signals"`
}

type NavigationNode struct {
	ID        string                 `json:"id"`
	Kind      domain.InformationKind `json:"kind"`
	Summary   string                 `json:"summary"`
	Contexts  []domain.Context       `json:"contexts,omitempty"`
	Cues      []semantics.Cue        `json:"cues,omitempty"`
	UpdatedAt time.Time              `json:"updated_at"`
}

type NavigationResult struct {
	Nodes []NavigationNode `json:"nodes"`
	Edges []derived.Edge   `json:"edges"`
}

func New(store *assetlog.Store) *Service {
	return &Service{store: store, index: retrieval.NewLexical(store.All()), now: time.Now}
}

func NewOrganized(store *assetlog.Store, derivedStore *derived.Store, provider semantics.Provider) (*Service, error) {
	if provider == nil {
		provider = semantics.Heuristic{}
	}
	records, err := derivedStore.AllWithEmbeddings()
	if err != nil {
		return nil, fmt.Errorf("加载派生检索状态: %w", err)
	}
	assets := store.All()
	currentRevision := make(map[string]uint64, len(assets))
	for _, asset := range assets {
		currentRevision[asset.ID] = asset.Revision
	}
	currentRecords := records[:0]
	for _, record := range records {
		if currentRevision[record.AssetID] == record.AssetRevision {
			currentRecords = append(currentRecords, record)
		}
	}
	return &Service{
		store:        store,
		derivedStore: derivedStore,
		index:        retrieval.NewLexical(assets),
		semantic:     derived.NewIndex(currentRecords),
		provider:     provider,
		now:          time.Now,
	}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (MutationResult, error) {
	if strings.TrimSpace(input.Content) == "" {
		return MutationResult{}, errors.New("信息内容不能为空")
	}
	if input.Kind == "" {
		input.Kind = domain.KindGeneral
	}
	if _, err := domain.ParseKind(string(input.Kind)); err != nil {
		return MutationResult{}, err
	}
	if err := s.validateRelationTargets("", input.Relations); err != nil {
		return MutationResult{}, err
	}
	now := s.now().UTC()
	id, err := newID(now)
	if err != nil {
		return MutationResult{}, err
	}
	value := domain.Information{
		Schema:    domain.AssetSchema,
		ID:        id,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
		Kind:      input.Kind,
		Content:   strings.TrimSpace(input.Content),
		Contexts:  normalizeContexts(input.Contexts),
		Relations: append([]domain.ExplicitRelation(nil), input.Relations...),
		Source:    input.Source,
	}
	if err := s.store.Create(value); err != nil {
		return MutationResult{}, err
	}
	s.index.Upsert(value)
	return MutationResult{Information: value, Organization: s.organize(ctx, value)}, nil
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (MutationResult, error) {
	unlock := s.lockMutation(input.ID)
	released := false
	defer func() {
		if !released {
			unlock()
		}
	}()
	current, ok := s.store.Get(strings.TrimSpace(input.ID))
	if !ok {
		return MutationResult{}, errors.New("信息不存在")
	}
	if input.Kind == nil && input.Content == nil && input.Contexts == nil && input.Relations == nil && input.Source == nil {
		return MutationResult{}, errors.New("更新内容不能为空")
	}
	updated := current
	updated.Revision++
	updated.UpdatedAt = s.now().UTC()
	if input.Kind != nil {
		if _, err := domain.ParseKind(string(*input.Kind)); err != nil {
			return MutationResult{}, err
		}
		updated.Kind = *input.Kind
	}
	if input.Content != nil {
		updated.Content = strings.TrimSpace(*input.Content)
	}
	if input.Contexts != nil {
		updated.Contexts = normalizeContexts(*input.Contexts)
	}
	if input.Relations != nil {
		if err := s.validateRelationTargets(updated.ID, *input.Relations); err != nil {
			return MutationResult{}, err
		}
		updated.Relations = append([]domain.ExplicitRelation(nil), (*input.Relations)...)
	}
	if input.Source != nil {
		updated.Source = *input.Source
	}
	var dependents []string
	if s.semantic != nil {
		dependents = s.semantic.Dependents(updated.ID)
	}
	if err := s.store.Update(updated, input.ExpectedRevision); err != nil {
		return MutationResult{}, err
	}
	s.index.Upsert(updated)
	organization := s.organize(ctx, updated)
	unlock()
	released = true
	if pending := s.refreshDependents(ctx, dependents); pending > 0 {
		organization.Status = "pending"
		organization.Error = strings.Trim(strings.Join([]string{organization.Error, fmt.Sprintf("%d 条关联信息待重新组织", pending)}, "; "), "; ")
	}
	return MutationResult{Information: updated, Organization: organization}, nil
}

func (s *Service) Read(_ context.Context, id string) (domain.Information, error) {
	value, ok := s.store.Get(strings.TrimSpace(id))
	if !ok {
		return domain.Information{}, errors.New("信息不存在")
	}
	return value, nil
}

func (s *Service) Search(ctx context.Context, input SearchInput) ([]SearchResult, error) {
	if strings.TrimSpace(input.Query) == "" {
		return nil, errors.New("检索内容不能为空")
	}
	if input.Limit < 0 || input.Limit > 100 {
		return nil, errors.New("检索数量必须介于一和一百之间")
	}
	contexts := normalizeContexts(input.Contexts)
	limit := input.Limit
	if limit == 0 {
		limit = 10
	}
	candidateLimit := limit * 4
	if candidateLimit < 20 {
		candidateLimit = 20
	}
	lexical := s.index.Search(input.Query, contexts, candidateLimit)
	trimmedQuery := strings.TrimSpace(input.Query)
	if len(lexical) > 0 && lexical[0].Information.ID == trimmedQuery && contains(lexical[0].Signals, "identity") {
		return s.compactResults(lexical[:1]), nil
	}
	if s.semantic == nil || s.provider == nil {
		if len(lexical) > limit {
			lexical = lexical[:limit]
		}
		return s.compactResults(lexical), nil
	}
	vectors, err := s.provider.Embed(ctx, []string{input.Query})
	if err != nil || len(vectors) != 1 {
		if len(lexical) > limit {
			lexical = lexical[:limit]
		}
		return s.compactResults(lexical), nil
	}
	semanticHits := s.semantic.Search(vectors[0], contexts, candidateLimit)
	type fused struct {
		score   float64
		signals map[string]struct{}
	}
	fusedByID := make(map[string]*fused, len(lexical)+len(semanticHits))
	add := func(id, signal string, rank int, weight float64) {
		item := fusedByID[id]
		if item == nil {
			item = &fused{signals: make(map[string]struct{})}
			fusedByID[id] = item
		}
		item.score += weight / float64(60+rank)
		item.signals[signal] = struct{}{}
	}
	seeds := make([]string, 0, 8)
	seedSet := make(map[string]struct{}, 8)
	appendSeed := func(id string) {
		if _, exists := seedSet[id]; exists {
			return
		}
		seedSet[id] = struct{}{}
		seeds = append(seeds, id)
	}
	for rank, hit := range lexical {
		add(hit.Information.ID, "lexical", rank+1, 1)
		if rank < 4 {
			appendSeed(hit.Information.ID)
		}
	}
	for rank, hit := range semanticHits {
		add(hit.AssetID, "semantic", rank+1, 1)
		if rank < 4 {
			appendSeed(hit.AssetID)
		}
	}
	var related []derived.Edge
	if !input.DisableRelationExpansion {
		related = s.semantic.Navigate(seeds, nil, 1, candidateLimit)
	}
	for rank, edge := range related {
		_, sourceIsSeed := seedSet[edge.SourceID]
		_, targetIsSeed := seedSet[edge.TargetID]
		target := ""
		switch {
		case sourceIsSeed && !targetIsSeed:
			target = edge.TargetID
		case targetIsSeed && !sourceIsSeed:
			target = edge.SourceID
		default:
			continue
		}
		// 关系证据用于补充直接命中，不能压过明确的词法或语义证据。
		add(target, "relation", rank+1, 0.1*edge.Confidence)
	}
	results := make([]SearchResult, 0, len(fusedByID))
	for id, item := range fusedByID {
		value, ok := s.store.Get(id)
		effectiveContexts := s.effectiveContexts(id, value.Contexts)
		if !ok || !matchesContexts(effectiveContexts, contexts) {
			continue
		}
		signals := make([]string, 0, len(item.signals))
		for signal := range item.signals {
			signals = append(signals, signal)
		}
		sort.Strings(signals)
		results = append(results, s.compactResult(value, effectiveContexts, item.score, signals))
	}
	sort.Slice(results, func(left, right int) bool {
		if results[left].Score == results[right].Score {
			return results[left].ID < results[right].ID
		}
		return results[left].Score > results[right].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *Service) compactResults(values []retrieval.Result) []SearchResult {
	results := make([]SearchResult, 0, len(values))
	for _, value := range values {
		contexts := s.effectiveContexts(value.Information.ID, value.Information.Contexts)
		results = append(results, s.compactResult(value.Information, contexts, value.Score, value.Signals))
	}
	return results
}

func (s *Service) compactResult(value domain.Information, contexts []domain.Context, score float64, signals []string) SearchResult {
	summary := truncate(value.Content, 240)
	kind := value.Kind
	if s.semantic != nil {
		if record, ok := s.semantic.Get(value.ID); ok {
			if strings.TrimSpace(record.Analysis.Summary) != "" {
				summary = record.Analysis.Summary
			}
			if kind == domain.KindGeneral && record.Analysis.Kind != "" {
				kind = record.Analysis.Kind
			}
		}
	}
	return SearchResult{
		ID: value.ID, Kind: kind, Summary: summary, Contexts: append([]domain.Context(nil), contexts...),
		Score: score, Signals: append([]string(nil), signals...),
	}
}

func (s *Service) Navigate(_ context.Context, start, relationTypes []string, depth, limit int) (NavigationResult, error) {
	if s.semantic == nil {
		return NavigationResult{}, errors.New("语义关系导航未启用")
	}
	if len(start) == 0 {
		return NavigationResult{}, errors.New("关系导航至少需要一个起点")
	}
	edges := s.semantic.Navigate(start, relationTypes, depth, limit)
	ids := make(map[string]struct{}, len(start)+len(edges)*2)
	for _, id := range start {
		ids[id] = struct{}{}
	}
	for _, edge := range edges {
		ids[edge.SourceID] = struct{}{}
		ids[edge.TargetID] = struct{}{}
	}
	nodes := make([]NavigationNode, 0, len(ids))
	for id := range ids {
		value, ok := s.store.Get(id)
		if !ok {
			continue
		}
		summary := truncate(value.Content, 240)
		var cues []semantics.Cue
		contexts := append([]domain.Context(nil), value.Contexts...)
		if record, ok := s.semantic.Get(id); ok {
			if strings.TrimSpace(record.Analysis.Summary) != "" {
				summary = record.Analysis.Summary
			}
			cues = append([]semantics.Cue(nil), record.Analysis.Cues...)
			contexts = mergeContexts(contexts, record.Analysis.Contexts)
			if value.Kind == domain.KindGeneral && record.Analysis.Kind != "" {
				value.Kind = record.Analysis.Kind
			}
		}
		nodes = append(nodes, NavigationNode{ID: id, Kind: value.Kind, Summary: summary, Contexts: contexts, Cues: cues, UpdatedAt: value.UpdatedAt})
	}
	sort.Slice(nodes, func(left, right int) bool { return nodes[left].ID < nodes[right].ID })
	return NavigationResult{Nodes: nodes, Edges: edges}, nil
}

func (s *Service) Rules(context.Context) string {
	return CollaborationRules
}

func (s *Service) Close() error {
	var first error
	if s.derivedStore != nil {
		first = s.derivedStore.Close()
	}
	if err := s.store.Close(); first == nil {
		first = err
	}
	return first
}

func (s *Service) Maintain(ctx context.Context, rebuild bool) (map[string]int, error) {
	if s.derivedStore == nil || s.provider == nil {
		return nil, errors.New("语义组织未启用")
	}
	if rebuild {
		if err := s.derivedStore.Reset(); err != nil {
			return nil, err
		}
		s.semantic = derived.NewIndex(nil)
	}
	counts := map[string]int{"ready": 0, "degraded": 0, "pending": 0, "unchanged": 0}
	for _, value := range s.store.All() {
		unlock := s.lockMutation(value.ID)
		latest, exists := s.store.Get(value.ID)
		if !exists {
			unlock()
			continue
		}
		value = latest
		current, exists := s.derivedStore.Get(value.ID)
		if !rebuild && exists && current.AssetRevision == value.Revision && current.Status != "pending" && !s.hasStaleRelation(current) {
			counts["unchanged"]++
			unlock()
			continue
		}
		state := s.organize(ctx, value)
		counts[state.Status]++
		unlock()
	}
	return counts, nil
}

func (s *Service) organize(ctx context.Context, value domain.Information) OrganizationState {
	if s.derivedStore == nil || s.semantic == nil || s.provider == nil {
		return OrganizationState{Status: "unavailable"}
	}
	vectors, embeddingErr := s.provider.Embed(ctx, []string{value.Content})
	lexical := s.index.Search(value.Content, nil, 17)
	candidates := make([]semantics.Candidate, 0, 16)
	candidateIDs := make(map[string]struct{}, 16)
	appendCandidate := func(candidate domain.Information) {
		if candidate.ID == value.ID || len(candidates) == 16 {
			return
		}
		if _, exists := candidateIDs[candidate.ID]; exists {
			return
		}
		candidateIDs[candidate.ID] = struct{}{}
		candidates = append(candidates, semantics.Candidate{
			ID:       candidate.ID,
			Kind:     candidate.Kind,
			Content:  candidate.Content,
			Contexts: candidate.Contexts,
		})
	}
	for _, relation := range value.Relations {
		if candidate, ok := s.store.Get(relation.TargetID); ok {
			appendCandidate(candidate)
		}
	}
	for _, hit := range lexical {
		appendCandidate(hit.Information)
		if len(candidates) == 8 {
			break
		}
	}
	if len(vectors) == 1 {
		for _, hit := range s.semantic.Search(vectors[0], nil, 16) {
			if candidate, ok := s.store.Get(hit.AssetID); ok {
				appendCandidate(candidate)
			}
			if len(candidates) == 16 {
				break
			}
		}
	}
	analysis, analysisErr := s.provider.Analyze(ctx, value, candidates)
	analysis.Relations = s.finalizeRelations(value, analysis.Relations)
	status := "ready"
	errorText := ""
	if _, fallback := s.provider.(semantics.Heuristic); fallback {
		status = "degraded"
	}
	if analysisErr != nil || embeddingErr != nil || len(vectors) != 1 {
		status = "pending"
		parts := make([]string, 0, 2)
		if analysisErr != nil {
			parts = append(parts, analysisErr.Error())
		}
		if embeddingErr != nil {
			parts = append(parts, embeddingErr.Error())
		}
		if len(vectors) != 1 && embeddingErr == nil {
			parts = append(parts, "嵌入结果数量无效")
		}
		errorText = strings.Join(parts, "; ")
	}
	record := derived.Record{
		AssetID:       value.ID,
		AssetRevision: value.Revision,
		GeneratedAt:   s.now().UTC(),
		Provider:      s.provider.Name(),
		Status:        status,
		Error:         errorText,
		Analysis:      analysis,
	}
	if len(vectors) == 1 {
		record.Embedding = vectors[0]
	}
	current, exists := s.store.Get(value.ID)
	if !exists || current.Revision != value.Revision {
		return OrganizationState{Status: "pending", Provider: s.provider.Name(), Error: "信息已被更新版本取代"}
	}
	if err := s.derivedStore.Put(record); err != nil {
		return OrganizationState{Status: "pending", Provider: s.provider.Name(), Error: err.Error()}
	}
	s.semantic.Upsert(record)
	return OrganizationState{Status: status, Provider: record.Provider, Error: errorText}
}

func (s *Service) lockMutation(id string) func() {
	var bucket byte
	for index := 0; index < len(id); index++ {
		bucket = bucket*31 + id[index]
	}
	s.mutationMu[bucket].Lock()
	return s.mutationMu[bucket].Unlock
}

func (s *Service) refreshDependents(ctx context.Context, ids []string) int {
	pending := 0
	for _, id := range ids {
		unlock := s.lockMutation(id)
		value, exists := s.store.Get(id)
		if !exists {
			unlock()
			continue
		}
		state := s.organize(ctx, value)
		unlock()
		if state.Status == "pending" {
			pending++
		}
	}
	return pending
}

func (s *Service) finalizeRelations(value domain.Information, inferred []semantics.Relation) []semantics.Relation {
	result := make([]semantics.Relation, 0, len(value.Relations)+len(inferred))
	seen := make(map[string]struct{}, len(value.Relations)+len(inferred))
	for _, relation := range value.Relations {
		key := relation.Type + "\x00" + relation.TargetID
		seen[key] = struct{}{}
		result = append(result, semantics.Relation{Type: relation.Type, TargetID: relation.TargetID, Confidence: 1})
	}
	for _, relation := range inferred {
		key := relation.Type + "\x00" + relation.TargetID
		if _, exists := seen[key]; exists || relation.TargetID == value.ID {
			continue
		}
		target, exists := s.store.Get(relation.TargetID)
		if !exists {
			continue
		}
		relation.TargetRevision = target.Revision
		seen[key] = struct{}{}
		result = append(result, relation)
	}
	return result
}

func (s *Service) hasStaleRelation(record derived.Record) bool {
	for _, relation := range record.Analysis.Relations {
		if relation.TargetRevision == 0 {
			continue
		}
		target, exists := s.store.Get(relation.TargetID)
		if !exists || target.Revision != relation.TargetRevision {
			return true
		}
	}
	return false
}

func (s *Service) validateRelationTargets(sourceID string, relations []domain.ExplicitRelation) error {
	for _, relation := range relations {
		if relation.TargetID == sourceID {
			return errors.New("信息不能显式关联自身")
		}
		if _, exists := s.store.Get(relation.TargetID); !exists {
			return fmt.Errorf("关系目标 %q 不存在", relation.TargetID)
		}
	}
	return nil
}

func normalizeContexts(values []domain.Context) []domain.Context {
	result := make([]domain.Context, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value.Key = strings.TrimSpace(value.Key)
		value.Value = strings.TrimSpace(value.Value)
		key := strings.ToLower(value.Key) + "\x00" + strings.ToLower(value.Value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func mergeContexts(left, right []domain.Context) []domain.Context {
	return normalizeContexts(append(append([]domain.Context(nil), left...), right...))
}

func truncate(value string, length int) string {
	runes := []rune(value)
	if len(runes) <= length {
		return value
	}
	return strings.TrimSpace(string(runes[:length])) + "…"
}

func matchesContexts(actual, required []domain.Context) bool {
	if len(required) == 0 {
		return true
	}
	for _, expected := range required {
		declared := false
		compatible := false
		for _, candidate := range actual {
			if strings.EqualFold(candidate.Key, expected.Key) {
				declared = true
				if strings.EqualFold(candidate.Value, expected.Value) {
					compatible = true
				}
			}
		}
		if declared && !compatible {
			return false
		}
	}
	return true
}

func (s *Service) effectiveContexts(id string, explicit []domain.Context) []domain.Context {
	if s.semantic == nil {
		return explicit
	}
	record, ok := s.semantic.Get(id)
	if !ok {
		return explicit
	}
	return mergeContexts(explicit, record.Analysis.Contexts)
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func newID(now time.Time) (string, error) {
	var random [10]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("生成信息标识: %w", err)
	}
	milliseconds := uint64(now.UnixMilli())
	var raw [16]byte
	raw[0] = byte(milliseconds >> 40)
	raw[1] = byte(milliseconds >> 32)
	raw[2] = byte(milliseconds >> 24)
	raw[3] = byte(milliseconds >> 16)
	raw[4] = byte(milliseconds >> 8)
	raw[5] = byte(milliseconds)
	copy(raw[6:], random[:])
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80
	buffer := make([]byte, 36)
	hex.Encode(buffer[0:8], raw[0:4])
	buffer[8] = '-'
	hex.Encode(buffer[9:13], raw[4:6])
	buffer[13] = '-'
	hex.Encode(buffer[14:18], raw[6:8])
	buffer[18] = '-'
	hex.Encode(buffer[19:23], raw[8:10])
	buffer[23] = '-'
	hex.Encode(buffer[24:36], raw[10:16])
	return string(buffer), nil
}
