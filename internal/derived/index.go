package derived

import (
	"container/heap"
	"encoding/binary"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/viterin/vek/vek32"
)

type SemanticHit struct {
	AssetID string
	Score   float64
}

type Edge struct {
	SourceID       string  `json:"source_id"`
	TargetID       string  `json:"target_id"`
	Type           string  `json:"type"`
	Confidence     float64 `json:"confidence"`
	Evidence       string  `json:"evidence,omitempty"`
	Depth          int     `json:"depth"`
	targetRevision uint64
}

type Index struct {
	mu             sync.RWMutex
	searchCacheMu  sync.Mutex
	records        []indexedRecord
	blocks         map[int]*vectorBlock
	activeVectors  int
	locations      map[string]uint32
	forward        map[string][]Edge
	reverse        map[string][]Edge
	pendingReverse map[string]map[string]struct{}
	searchCache    map[string]*searchCacheEntry
}

const maxSearchCacheEntries = 64

type searchCacheEntry struct {
	ready chan struct{}
	hits  []SemanticHit
}

type vectorBlock struct {
	dimensions int
	records    []uint32
	active     []bool
	contexts   [][]domain.Context
	values     []float32
}

type vectorLocation struct {
	dimensions int
	row        int
}

type indexedRecord struct {
	record    Record
	vector    vectorLocation
	hasVector bool
}

func NewIndex(records []Record) *Index {
	index := &Index{
		records:        make([]indexedRecord, 0, len(records)),
		blocks:         make(map[int]*vectorBlock),
		locations:      make(map[string]uint32, len(records)),
		forward:        make(map[string][]Edge),
		reverse:        make(map[string][]Edge),
		pendingReverse: make(map[string]map[string]struct{}),
		searchCache:    make(map[string]*searchCacheEntry),
	}
	counts := make(map[int]int)
	for index, record := range records {
		if compact, err := canonicalRecord(record); err == nil {
			record = compact
			records[index] = compact
		}
		if len(record.Embedding) > 0 {
			counts[len(record.Embedding)]++
		}
	}
	for dimensions, count := range counts {
		index.blocks[dimensions] = &vectorBlock{
			dimensions: dimensions,
			records:    make([]uint32, 0, count), active: make([]bool, 0, count), contexts: make([][]domain.Context, 0, count),
			values: make([]float32, 0, count*dimensions),
		}
	}
	for recordIndex, record := range records {
		location := uint32(len(index.records))
		index.locations[record.AssetID] = location
		index.records = append(index.records, indexedRecord{record: cloneForIndex(record)})
		index.addPendingLocked(record)
		index.upsertVectorLocked(location, record)
		records[recordIndex].Embedding = nil
	}
	index.rebuildEdgesLocked()
	return index
}

func (i *Index) Upsert(record Record) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if compact, err := canonicalRecord(record); err == nil {
		record = compact
	}
	location, exists := i.locations[record.AssetID]
	if exists && i.records[location].record.AssetRevision > record.AssetRevision {
		return
	}
	i.removeOutgoingLocked(record.AssetID)
	i.removePendingLocked(record.AssetID)
	if !exists {
		location = uint32(len(i.records))
		i.locations[record.AssetID] = location
		i.records = append(i.records, indexedRecord{})
	}
	i.records[location].record = cloneForIndex(record)
	i.addPendingLocked(record)
	i.upsertVectorLocked(location, record)
	i.removeStaleIncomingLocked(record.AssetID)
	i.addOutgoingLocked(record.AssetID, record)
	i.invalidateSearchCache()
}

func cloneForIndex(record Record) Record {
	record = clone(record)
	record.Embedding = nil
	return record
}

func (i *Index) Get(id string) (Record, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	location, ok := i.locations[id]
	if !ok {
		return Record{}, false
	}
	return clone(i.records[location].record), true
}

// HasVectors 判断当前派生世代是否包含可用于语义检索的向量。
func (i *Index) HasVectors() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.activeVectors > 0
}

func (i *Index) Dependents(targetID string) []string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, edge := range i.reverse[targetID] {
		if edge.targetRevision == 0 || edge.SourceID == targetID {
			continue
		}
		seen[edge.SourceID] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func (i *Index) PendingDependents(targetID string) []string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	sources := i.pendingReverse[targetID]
	result := make([]string, 0, len(sources))
	for sourceID := range sources {
		result = append(result, sourceID)
	}
	sort.Strings(result)
	return result
}

func (i *Index) addPendingLocked(record Record) {
	if !record.HasPendingSemanticWork() {
		return
	}
	for _, candidate := range record.SemanticWorkReference.Candidates {
		if candidate.ID == "" || candidate.ID == record.AssetID {
			continue
		}
		sources := i.pendingReverse[candidate.ID]
		if sources == nil {
			sources = make(map[string]struct{})
			i.pendingReverse[candidate.ID] = sources
		}
		sources[record.AssetID] = struct{}{}
	}
}

func (i *Index) removePendingLocked(assetID string) {
	location, exists := i.locations[assetID]
	if !exists {
		return
	}
	record := i.records[location].record
	if !record.HasPendingSemanticWork() {
		return
	}
	for _, candidate := range record.SemanticWorkReference.Candidates {
		sources := i.pendingReverse[candidate.ID]
		delete(sources, assetID)
		if len(sources) == 0 {
			delete(i.pendingReverse, candidate.ID)
		}
	}
}

func (i *Index) Search(vector []float32, contexts []domain.Context, limit int) []SemanticHit {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if len(vector) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 10
	}
	if !finiteVector(vector) {
		return nil
	}
	queryNorm := vectorNorm(vector)
	if queryNorm == 0 {
		return nil
	}
	normalizedQuery := append([]float32(nil), vector...)
	vek32.MulNumber_Inplace(normalizedQuery, float32(1/queryNorm))
	block := i.blocks[len(vector)]
	if block == nil {
		return nil
	}
	cacheKey := semanticSearchCacheKey(normalizedQuery, contexts, limit)
	cacheEntry, calculate := i.searchCacheEntry(cacheKey)
	if !calculate {
		<-cacheEntry.ready
		return cloneSemanticHits(cacheEntry.hits)
	}
	best := make(semanticHitHeap, 0, limit)
	for row, recordID := range block.records {
		if !block.active[row] {
			continue
		}
		if len(contexts) > 0 && !matchesContexts(block.contexts[row], contexts) {
			continue
		}
		start := row * block.dimensions
		score := float64(vek32.Dot(normalizedQuery, block.values[start:start+block.dimensions]))
		if score > 0 {
			hit := SemanticHit{AssetID: i.records[recordID].record.AssetID, Score: score}
			if len(best) < limit {
				heap.Push(&best, hit)
			} else if betterSemanticHit(hit, best[0]) {
				best[0] = hit
				heap.Fix(&best, 0)
			}
		}
	}
	result := append([]SemanticHit(nil), best...)
	sort.Slice(result, func(left, right int) bool {
		if result[left].Score == result[right].Score {
			return result[left].AssetID < result[right].AssetID
		}
		return result[left].Score > result[right].Score
	})
	i.completeSearchCacheEntry(cacheEntry, result)
	return result
}

func semanticSearchCacheKey(vector []float32, contexts []domain.Context, limit int) string {
	encoded := make([]byte, 0, 16+len(vector)*4)
	encoded = binary.LittleEndian.AppendUint64(encoded, uint64(limit))
	encoded = binary.LittleEndian.AppendUint64(encoded, uint64(len(vector)))
	for _, value := range vector {
		encoded = binary.LittleEndian.AppendUint32(encoded, math.Float32bits(value))
	}
	encoded = binary.LittleEndian.AppendUint64(encoded, uint64(len(contexts)))
	for _, context := range contexts {
		encoded = binary.LittleEndian.AppendUint64(encoded, uint64(len(context.Key)))
		encoded = append(encoded, context.Key...)
		encoded = binary.LittleEndian.AppendUint64(encoded, uint64(len(context.Value)))
		encoded = append(encoded, context.Value...)
	}
	return string(encoded)
}

func (i *Index) searchCacheEntry(key string) (*searchCacheEntry, bool) {
	i.searchCacheMu.Lock()
	defer i.searchCacheMu.Unlock()
	if entry, ok := i.searchCache[key]; ok {
		return entry, false
	}
	if len(i.searchCache) >= maxSearchCacheEntries {
		i.searchCache = make(map[string]*searchCacheEntry)
	}
	entry := &searchCacheEntry{ready: make(chan struct{})}
	i.searchCache[key] = entry
	return entry, true
}

func (i *Index) completeSearchCacheEntry(entry *searchCacheEntry, hits []SemanticHit) {
	i.searchCacheMu.Lock()
	entry.hits = cloneSemanticHits(hits)
	close(entry.ready)
	i.searchCacheMu.Unlock()
}

func (i *Index) invalidateSearchCache() {
	i.searchCacheMu.Lock()
	i.searchCache = make(map[string]*searchCacheEntry)
	i.searchCacheMu.Unlock()
}

func cloneSemanticHits(hits []SemanticHit) []SemanticHit {
	return append([]SemanticHit(nil), hits...)
}

func (i *Index) Navigate(start []string, relationTypes []string, maxDepth, limit int) []Edge {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if maxDepth <= 0 {
		maxDepth = 1
	}
	if maxDepth > 5 {
		maxDepth = 5
	}
	if limit <= 0 {
		limit = 50
	}
	allowed := make(map[string]struct{}, len(relationTypes))
	for _, value := range relationTypes {
		allowed[strings.TrimSpace(value)] = struct{}{}
	}
	type pending struct {
		id    string
		depth int
	}
	queue := make([]pending, 0, len(start))
	visited := make(map[string]struct{}, len(start))
	for _, id := range start {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		visited[id] = struct{}{}
		queue = append(queue, pending{id: id})
	}
	result := make([]Edge, 0, limit)
	visitedEdges := make(map[string]struct{})
	for len(queue) > 0 && len(result) < limit {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= maxDepth {
			continue
		}
		edges := append(append([]Edge(nil), i.forward[current.id]...), i.reverse[current.id]...)
		sort.Slice(edges, func(left, right int) bool {
			if edges[left].Confidence == edges[right].Confidence {
				return edges[left].TargetID < edges[right].TargetID
			}
			return edges[left].Confidence > edges[right].Confidence
		})
		for _, edge := range edges {
			if len(allowed) > 0 {
				if _, ok := allowed[edge.Type]; !ok {
					continue
				}
			}
			edgeID := edge.SourceID + "\x00" + edge.Type + "\x00" + edge.TargetID
			if _, exists := visitedEdges[edgeID]; exists {
				continue
			}
			visitedEdges[edgeID] = struct{}{}
			nextID := edge.TargetID
			if edge.TargetID == current.id {
				nextID = edge.SourceID
			}
			edge.Depth = current.depth + 1
			result = append(result, edge)
			if len(result) == limit {
				break
			}
			if _, exists := visited[nextID]; !exists {
				visited[nextID] = struct{}{}
				queue = append(queue, pending{id: nextID, depth: current.depth + 1})
			}
		}
	}
	return result
}

func (i *Index) rebuildEdgesLocked() {
	i.forward = make(map[string][]Edge)
	i.reverse = make(map[string][]Edge)
	for _, indexed := range i.records {
		record := indexed.record
		id := record.AssetID
		for _, relation := range record.Analysis.Relations {
			if !i.relationCurrentLocked(id, relation) {
				continue
			}
			edge := edgeFromRelation(id, relation)
			i.forward[id] = append(i.forward[id], edge)
			i.reverse[relation.TargetID] = append(i.reverse[relation.TargetID], edge)
		}
	}
}

func (i *Index) upsertVectorLocked(recordID uint32, record Record) {
	validVector := len(record.Embedding) > 0 && finiteVector(record.Embedding)
	indexed := &i.records[recordID]
	if indexed.hasVector {
		previous := indexed.vector
		block := i.blocks[previous.dimensions]
		wasActive := block.active[previous.row]
		if validVector && len(record.Embedding) == previous.dimensions {
			start := previous.row * block.dimensions
			values := block.values[start : start+block.dimensions]
			copy(values, record.Embedding)
			norm := vectorNorm(record.Embedding)
			if norm > 0 {
				vek32.MulNumber_Inplace(values, float32(1/norm))
			}
			block.contexts[previous.row] = append(block.contexts[previous.row][:0], semantics.ContextValues(record.Analysis.Contexts)...)
			block.active[previous.row] = norm > 0
			if wasActive != block.active[previous.row] {
				if block.active[previous.row] {
					i.activeVectors++
				} else {
					i.activeVectors--
				}
			}
			return
		}
		block.active[previous.row] = false
		if wasActive {
			i.activeVectors--
		}
		indexed.hasVector = false
	}
	if !validVector {
		return
	}
	block := i.blocks[len(record.Embedding)]
	if block == nil {
		block = &vectorBlock{dimensions: len(record.Embedding)}
		i.blocks[len(record.Embedding)] = block
	}
	row := len(block.records)
	block.records = append(block.records, recordID)
	block.active = append(block.active, true)
	block.contexts = append(block.contexts, semantics.ContextValues(record.Analysis.Contexts))
	start := len(block.values)
	block.values = append(block.values, record.Embedding...)
	norm := vectorNorm(record.Embedding)
	if norm == 0 {
		block.active[row] = false
	} else {
		vek32.MulNumber_Inplace(block.values[start:], float32(1/norm))
		i.activeVectors++
	}
	indexed.vector = vectorLocation{dimensions: len(record.Embedding), row: row}
	indexed.hasVector = true
}

func (i *Index) removeOutgoingLocked(id string) {
	for _, edge := range i.forward[id] {
		reverse := i.reverse[edge.TargetID]
		filtered := reverse[:0]
		for _, candidate := range reverse {
			if candidate.SourceID != id {
				filtered = append(filtered, candidate)
			}
		}
		if len(filtered) == 0 {
			delete(i.reverse, edge.TargetID)
		} else {
			i.reverse[edge.TargetID] = filtered
		}
	}
	delete(i.forward, id)
}

func (i *Index) addOutgoingLocked(id string, record Record) {
	for _, relation := range record.Analysis.Relations {
		if !i.relationCurrentLocked(id, relation) {
			continue
		}
		edge := edgeFromRelation(id, relation)
		i.forward[id] = append(i.forward[id], edge)
		i.reverse[relation.TargetID] = append(i.reverse[relation.TargetID], edge)
	}
}

func (i *Index) relationCurrentLocked(sourceID string, relation semantics.Relation) bool {
	location, exists := i.locations[relation.TargetID]
	if !exists {
		return false
	}
	target := i.records[location].record
	return relation.TargetID != sourceID && (relation.TargetRevision == 0 || relation.TargetRevision == target.AssetRevision)
}

func edgeFromRelation(sourceID string, relation semantics.Relation) Edge {
	return Edge{
		SourceID: sourceID, TargetID: relation.TargetID, Type: relation.Type,
		Confidence: relation.Confidence, Evidence: relation.Evidence, targetRevision: relation.TargetRevision,
	}
}

func (i *Index) removeStaleIncomingLocked(targetID string) {
	location, exists := i.locations[targetID]
	if !exists {
		return
	}
	target := i.records[location].record
	incoming := i.reverse[targetID]
	keptIncoming := incoming[:0]
	for _, edge := range incoming {
		if edge.targetRevision == 0 || edge.targetRevision == target.AssetRevision {
			keptIncoming = append(keptIncoming, edge)
			continue
		}
		outgoing := i.forward[edge.SourceID]
		keptOutgoing := outgoing[:0]
		for _, candidate := range outgoing {
			if candidate.TargetID == targetID && candidate.targetRevision != 0 && candidate.targetRevision != target.AssetRevision {
				continue
			}
			keptOutgoing = append(keptOutgoing, candidate)
		}
		if len(keptOutgoing) == 0 {
			delete(i.forward, edge.SourceID)
		} else {
			i.forward[edge.SourceID] = keptOutgoing
		}
	}
	if len(keptIncoming) == 0 {
		delete(i.reverse, targetID)
	} else {
		i.reverse[targetID] = keptIncoming
	}
}

func vectorNorm(vector []float32) float64 {
	sum := 0.0
	for _, value := range vector {
		converted := float64(value)
		sum += converted * converted
	}
	return math.Sqrt(sum)
}

func finiteVector(vector []float32) bool {
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

type semanticHitHeap []SemanticHit

func (h semanticHitHeap) Len() int { return len(h) }
func (h semanticHitHeap) Less(left, right int) bool {
	if h[left].Score == h[right].Score {
		return h[left].AssetID > h[right].AssetID
	}
	return h[left].Score < h[right].Score
}
func (h semanticHitHeap) Swap(left, right int) { h[left], h[right] = h[right], h[left] }
func (h *semanticHitHeap) Push(value any)      { *h = append(*h, value.(SemanticHit)) }
func (h *semanticHitHeap) Pop() any {
	previous := *h
	last := previous[len(previous)-1]
	*h = previous[:len(previous)-1]
	return last
}

func betterSemanticHit(left, right SemanticHit) bool {
	if left.Score == right.Score {
		return left.AssetID < right.AssetID
	}
	return left.Score > right.Score
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
