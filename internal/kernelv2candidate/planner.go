package kernelv2candidate

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
)

const (
	SourceSchedulingPolicy = "bounded-source-breadth-before-repeated-depth/v1"
	planningSourceLimit    = 8
	planningEvidenceProbe  = 2
	maximumTrackedQueries  = 64
)

// EvidencePlans coordinates only the already returned sources of one search.
// It is ephemeral, bounded and deliberately absent from authority and derived
// state. Search-only callers pay no evidence ranking cost; the first evidence
// request prepares the bounded source lanes once for that query.
type EvidencePlans struct {
	mu      sync.Mutex
	queries map[string]*evidencePlan
}

type evidencePlan struct {
	sources      []string
	preparing    bool
	ready        chan struct{}
	breadthFirst bool
	references   map[string][]domain.EvidenceReference
	deep         map[string]bool
	evidence     map[string]domain.Evidence
}

type authorityReader func(string) (domain.Information, bool)
type evidenceRanker func(domain.Information, string, int) []domain.EvidenceReference
type evidenceProber func(domain.Information, string) ([]domain.EvidenceReference, bool)

func NewEvidencePlans() *EvidencePlans {
	return &EvidencePlans{queries: make(map[string]*evidencePlan)}
}

// Reset invalidates every ephemeral plan after an accepted authority write.
// The owning service calls it in the same mutation critical section as the
// write, so a cached reference can never outlive its source revision.
func (p *EvidencePlans) Reset() {
	p.mu.Lock()
	p.queries = make(map[string]*evidencePlan)
	p.mu.Unlock()
}

// ObserveSearch freezes the source order returned by one public Search call.
// It stores stable source identities only; authoritative content remains owned
// by the authority and is read lazily if evidence is actually requested.
func (p *EvidencePlans) ObserveSearch(query string, sourceIDs []string) {
	key := normalizePlanQuery(query)
	if key == "" {
		return
	}
	seen := make(map[string]struct{}, min(len(sourceIDs), planningSourceLimit))
	sources := make([]string, 0, min(len(sourceIDs), planningSourceLimit))
	for _, sourceID := range sourceIDs {
		sourceID = strings.TrimSpace(sourceID)
		if sourceID == "" {
			continue
		}
		if _, exists := seen[sourceID]; exists {
			continue
		}
		seen[sourceID] = struct{}{}
		sources = append(sources, sourceID)
		if len(sources) == planningSourceLimit {
			break
		}
	}
	if len(sources) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queries) >= maximumTrackedQueries {
		// Plans are disposable request state. Clearing the bounded cache is safer
		// than retaining an unbounded query history; a missing plan falls back to
		// the unchanged per-source evidence behavior.
		p.queries = make(map[string]*evidencePlan)
	}
	p.queries[key] = &evidencePlan{sources: sources, ready: make(chan struct{})}
}

// References returns a source lane from the exact preceding Search plan. When
// several returned sources each contain repeated query-positive evidence, one
// reference per source is exposed before repeated depth can consume the fixed
// read budget. A lone deep source retains its full requested depth, protecting
// same-source multi-fact delivery.
func (p *EvidencePlans) References(
	query string,
	sourceID string,
	limit int,
	read authorityReader,
	rank evidenceRanker,
	probe evidenceProber,
) ([]domain.EvidenceReference, bool) {
	return p.references(query, sourceID, domain.Information{}, limit, read, rank, probe)
}

// ReferencesWithCurrent reuses the exact authority value that the public
// EvidenceSearch boundary has already validated. The first bounded plan must
// still read every other returned source, but it must not read this source a
// second time merely to prepare the same immutable revision.
func (p *EvidencePlans) ReferencesWithCurrent(
	query string,
	current domain.Information,
	limit int,
	read authorityReader,
	rank evidenceRanker,
	probe evidenceProber,
) ([]domain.EvidenceReference, bool) {
	return p.references(query, current.ID, current, limit, read, rank, probe)
}

// CachedReferences returns an already prepared lane without reopening the
// authority. It is safe only because every accepted authority mutation resets
// all plans before another request can observe the changed service state.
func (p *EvidencePlans) CachedReferences(query, sourceID string, limit int) ([]domain.EvidenceReference, bool) {
	key := normalizePlanQuery(query)
	sourceID = strings.TrimSpace(sourceID)
	if key == "" || sourceID == "" || limit <= 0 {
		return nil, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	plan := p.queries[key]
	if plan == nil || plan.references == nil {
		return nil, false
	}
	result, exists := plan.references[sourceID]
	if !exists {
		return nil, false
	}
	if !plan.breadthFirst && plan.deep[sourceID] && limit > len(result) {
		return nil, false
	}
	if plan.breadthFirst {
		limit = min(limit, 1)
	}
	return cloneReferences(result, limit), true
}

func (p *EvidencePlans) references(
	query string,
	sourceID string,
	initial domain.Information,
	limit int,
	read authorityReader,
	rank evidenceRanker,
	probe evidenceProber,
) ([]domain.EvidenceReference, bool) {
	key := normalizePlanQuery(query)
	sourceID = strings.TrimSpace(sourceID)
	if key == "" || sourceID == "" || limit <= 0 || read == nil || rank == nil || probe == nil {
		return nil, false
	}
	for {
		p.mu.Lock()
		plan := p.queries[key]
		if plan == nil {
			p.mu.Unlock()
			return nil, false
		}
		if plan.references != nil {
			breadthFirst := plan.breadthFirst
			result, exists := plan.references[sourceID]
			deep := plan.deep[sourceID]
			p.mu.Unlock()
			if exists {
				if breadthFirst {
					return cloneReferences(result, min(limit, 1)), true
				}
				if !deep || limit <= len(result) {
					return cloneReferences(result, limit), true
				}
				// The two-reference probe proved that this is the lone deep lane.
				// Re-rank only that source when the caller asks for more depth;
				// every short lane remains cached and avoids duplicate work.
				value, current := read(sourceID)
				if !current {
					return nil, true
				}
				result = cloneReferences(rank(value, query, limit), limit)
				cached := cacheableEvidence(value, result)
				p.mu.Lock()
				if current := p.queries[key]; current == plan {
					for id, item := range cached {
						current.evidence[id] = item
					}
				}
				p.mu.Unlock()
				return result, true
			}
			if !breadthFirst {
				return nil, false
			}
			value, exists := read(sourceID)
			if !exists {
				return nil, true
			}
			result = rank(value, query, 1)
			cached := cacheableEvidence(value, result)
			p.mu.Lock()
			if current := p.queries[key]; current == plan && current.breadthFirst {
				current.references[sourceID] = append([]domain.EvidenceReference(nil), result...)
				for id, item := range cached {
					current.evidence[id] = item
				}
			}
			p.mu.Unlock()
			return cloneReferences(result, 1), true
		}
		if plan.preparing {
			ready := plan.ready
			p.mu.Unlock()
			<-ready
			continue
		}
		plan.preparing = true
		sources := append([]string(nil), plan.sources...)
		p.mu.Unlock()

		type rankedLane struct {
			value      domain.Information
			references []domain.EvidenceReference
			deep       bool
		}
		ranked := make([]rankedLane, len(sources))
		var wait sync.WaitGroup
		wait.Add(len(sources))
		for index, plannedSource := range sources {
			go func(index int, plannedSource string) {
				defer wait.Done()
				value, exists := initial, initial.ID == plannedSource
				if !exists {
					value, exists = read(plannedSource)
				}
				if !exists {
					return
				}
				references, deep := probe(value, query)
				ranked[index] = rankedLane{value: value, references: references, deep: deep}
			}(index, plannedSource)
		}
		wait.Wait()

		references := make(map[string][]domain.EvidenceReference, 2)
		deep := make(map[string]bool, 2)
		cachedEvidence := make(map[string]domain.Evidence)
		deepSources := 0
		for index, plannedSource := range sources {
			lane := ranked[index]
			if lane.value.ID == "" {
				references[plannedSource] = nil
				continue
			}
			references[plannedSource] = append([]domain.EvidenceReference(nil), lane.references...)
			deep[plannedSource] = lane.deep
			for id, item := range cacheableEvidence(lane.value, lane.references) {
				cachedEvidence[id] = item
			}
			if lane.deep {
				deepSources++
			}
		}

		p.mu.Lock()
		plan.breadthFirst = deepSources > 1
		plan.references = references
		plan.deep = deep
		plan.evidence = cachedEvidence
		close(plan.ready)
		p.mu.Unlock()
	}
}

// Read returns only evidence produced by the exact preceding bounded plan and
// only while its authoritative source revision is still current. A caller-
// supplied or stale ID misses this cache and must take the full validating
// authority path. The cache is bounded by maximumTrackedQueries and the fixed
// evidence probe/read limits, and never becomes durable product state.
func (p *EvidencePlans) Read(id string, read authorityReader) (domain.Evidence, bool) {
	id = strings.TrimSpace(id)
	if id == "" || read == nil {
		return domain.Evidence{}, false
	}
	p.mu.Lock()
	var cached domain.Evidence
	for _, plan := range p.queries {
		if item, exists := plan.evidence[id]; exists {
			cached = item
			break
		}
	}
	p.mu.Unlock()
	if cached.ID == "" {
		return domain.Evidence{}, false
	}
	current, exists := read(cached.SourceID)
	if !exists || current.Revision != cached.SourceRevision {
		return domain.Evidence{}, false
	}
	return cached, true
}

// ReadCached returns an exact reference produced by the current ephemeral
// plan. The owning service must pair this with Reset on every authority write.
func (p *EvidencePlans) ReadCached(id string) (domain.Evidence, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.Evidence{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, plan := range p.queries {
		if item, exists := plan.evidence[id]; exists {
			return item, true
		}
	}
	return domain.Evidence{}, false
}

func cacheableEvidence(value domain.Information, references []domain.EvidenceReference) map[string]domain.Evidence {
	result := make(map[string]domain.Evidence, len(references))
	for _, reference := range references {
		if reference.Validate() != nil || reference.SourceID != value.ID || reference.SourceRevision != value.Revision {
			continue
		}
		unit, err := derived.ParseEvidenceUnitID(reference.ID)
		if err != nil || unit.SourceID != reference.SourceID || unit.SourceRevision != reference.SourceRevision ||
			unit.StartRune != reference.StartRune || unit.EndRune != reference.EndRune || unit.StartByte < 0 ||
			unit.EndByte <= unit.StartByte || unit.EndByte > len(value.Content) {
			continue
		}
		content := value.Content[unit.StartByte:unit.EndByte]
		if !utf8.ValidString(content) || utf8.RuneCountInString(content) != reference.ContentRunes {
			continue
		}
		result[reference.ID] = domain.Evidence{
			Schema: domain.EvidenceSchema, ID: reference.ID, SourceID: reference.SourceID,
			SourceRevision: reference.SourceRevision, StartRune: reference.StartRune,
			EndRune: reference.EndRune, Content: content,
		}
	}
	return result
}

func normalizePlanQuery(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(query), " "))
}

func cloneReferences(values []domain.EvidenceReference, limit int) []domain.EvidenceReference {
	if len(values) > limit {
		values = values[:limit]
	}
	return append([]domain.EvidenceReference(nil), values...)
}
