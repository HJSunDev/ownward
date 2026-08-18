package derived

import (
	"container/heap"
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
	mu        sync.RWMutex
	records   map[string]Record
	blocks    map[int]*vectorBlock
	locations map[string]vectorLocation
	forward   map[string][]Edge
	reverse   map[string][]Edge
}

type vectorBlock struct {
	dimensions int
	ids        []string
	active     []bool
	norms      []float64
	contexts   [][]domain.Context
	values     []float32
}

type vectorLocation struct {
	dimensions int
	row        int
}

func NewIndex(records []Record) *Index {
	index := &Index{
		records:   make(map[string]Record, len(records)),
		blocks:    make(map[int]*vectorBlock),
		locations: make(map[string]vectorLocation, len(records)),
		forward:   make(map[string][]Edge),
		reverse:   make(map[string][]Edge),
	}
	counts := make(map[int]int)
	for _, record := range records {
		if len(record.Embedding) > 0 {
			counts[len(record.Embedding)]++
		}
	}
	for dimensions, count := range counts {
		index.blocks[dimensions] = &vectorBlock{
			dimensions: dimensions,
			ids:        make([]string, 0, count), active: make([]bool, 0, count), norms: make([]float64, 0, count), contexts: make([][]domain.Context, 0, count),
			values: make([]float32, 0, count*dimensions),
		}
	}
	for recordIndex, record := range records {
		index.records[record.AssetID] = cloneForIndex(record)
		index.upsertVectorLocked(record)
		records[recordIndex].Embedding = nil
	}
	index.rebuildEdgesLocked()
	return index
}

func (i *Index) Upsert(record Record) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if current, exists := i.records[record.AssetID]; exists && current.AssetRevision > record.AssetRevision {
		return
	}
	i.removeOutgoingLocked(record.AssetID)
	i.records[record.AssetID] = cloneForIndex(record)
	i.upsertVectorLocked(record)
	i.removeStaleIncomingLocked(record.AssetID)
	i.addOutgoingLocked(record.AssetID, record)
}

func cloneForIndex(record Record) Record {
	record = clone(record)
	record.Embedding = nil
	return record
}

func (i *Index) Get(id string) (Record, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	record, ok := i.records[id]
	return clone(record), ok
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
	block := i.blocks[len(vector)]
	if block == nil {
		return nil
	}
	best := make(semanticHitHeap, 0, limit)
	for row, id := range block.ids {
		if !block.active[row] {
			continue
		}
		if len(contexts) > 0 && !matchesContexts(block.contexts[row], contexts) {
			continue
		}
		start := row * block.dimensions
		score := cosineWithNorms(vector, block.values[start:start+block.dimensions], queryNorm, block.norms[row])
		if score > 0 {
			hit := SemanticHit{AssetID: id, Score: score}
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
	return result
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
	for id, record := range i.records {
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

func (i *Index) upsertVectorLocked(record Record) {
	validVector := len(record.Embedding) > 0 && finiteVector(record.Embedding)
	if previous, exists := i.locations[record.AssetID]; exists {
		block := i.blocks[previous.dimensions]
		if validVector && len(record.Embedding) == previous.dimensions {
			start := previous.row * block.dimensions
			copy(block.values[start:start+block.dimensions], record.Embedding)
			block.norms[previous.row] = vectorNorm(record.Embedding)
			block.contexts[previous.row] = append(block.contexts[previous.row][:0], record.Analysis.Contexts...)
			block.active[previous.row] = record.Status != "pending" && block.norms[previous.row] > 0
			return
		}
		block.active[previous.row] = false
		delete(i.locations, record.AssetID)
	}
	if !validVector {
		return
	}
	block := i.blocks[len(record.Embedding)]
	if block == nil {
		block = &vectorBlock{dimensions: len(record.Embedding)}
		i.blocks[len(record.Embedding)] = block
	}
	row := len(block.ids)
	block.ids = append(block.ids, record.AssetID)
	block.active = append(block.active, record.Status != "pending")
	block.norms = append(block.norms, vectorNorm(record.Embedding))
	block.contexts = append(block.contexts, append([]domain.Context(nil), record.Analysis.Contexts...))
	block.values = append(block.values, record.Embedding...)
	if block.norms[row] == 0 {
		block.active[row] = false
	}
	i.locations[record.AssetID] = vectorLocation{dimensions: len(record.Embedding), row: row}
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
	target, exists := i.records[relation.TargetID]
	return exists && relation.TargetID != sourceID && (relation.TargetRevision == 0 || relation.TargetRevision == target.AssetRevision)
}

func edgeFromRelation(sourceID string, relation semantics.Relation) Edge {
	return Edge{
		SourceID: sourceID, TargetID: relation.TargetID, Type: relation.Type,
		Confidence: relation.Confidence, Evidence: relation.Evidence, targetRevision: relation.TargetRevision,
	}
}

func (i *Index) removeStaleIncomingLocked(targetID string) {
	target := i.records[targetID]
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

func cosineWithNorms(left, right []float32, leftNorm, rightNorm float64) float64 {
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	dot := float64(vek32.Dot(left, right))
	return dot / (leftNorm * rightNorm)
}

func vectorNorm(vector []float32) float64 {
	return math.Sqrt(float64(vek32.Dot(vector, vector)))
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
