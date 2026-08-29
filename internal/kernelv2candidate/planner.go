package kernelv2candidate

import (
	"strings"
	"sync"

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
}

type authorityReader func(string) (domain.Information, bool)
type evidenceRanker func(domain.Information, string, int) []domain.EvidenceReference

func NewEvidencePlans() *EvidencePlans {
	return &EvidencePlans{queries: make(map[string]*evidencePlan)}
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
) ([]domain.EvidenceReference, bool) {
	key := normalizePlanQuery(query)
	sourceID = strings.TrimSpace(sourceID)
	if key == "" || sourceID == "" || limit <= 0 || read == nil || rank == nil {
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
			p.mu.Unlock()
			if exists {
				if breadthFirst {
					return cloneReferences(result, min(limit, 1)), true
				}
				if len(result) < planningEvidenceProbe || limit <= len(result) {
					return cloneReferences(result, limit), true
				}
				// The two-reference probe proved that this is the lone deep lane.
				// Re-rank only that source when the caller asks for more depth;
				// every short lane remains cached and avoids duplicate work.
				value, current := read(sourceID)
				if !current {
					return nil, true
				}
				return cloneReferences(rank(value, query, limit), limit), true
			}
			if !breadthFirst {
				return nil, false
			}
			value, exists := read(sourceID)
			if !exists {
				return nil, true
			}
			result = rank(value, query, 1)
			p.mu.Lock()
			if current := p.queries[key]; current == plan && current.breadthFirst {
				current.references[sourceID] = append([]domain.EvidenceReference(nil), result...)
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

		references := make(map[string][]domain.EvidenceReference, 2)
		deepSources := 0
		for _, plannedSource := range sources {
			value, exists := read(plannedSource)
			if !exists {
				references[plannedSource] = nil
				continue
			}
			lane := rank(value, query, planningEvidenceProbe)
			references[plannedSource] = append([]domain.EvidenceReference(nil), lane...)
			if len(lane) > 1 {
				deepSources++
				if deepSources > 1 {
					break
				}
			}
		}

		p.mu.Lock()
		plan.breadthFirst = deepSources > 1
		plan.references = references
		close(plan.ready)
		p.mu.Unlock()
	}
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
