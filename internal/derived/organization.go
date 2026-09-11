package derived

import (
	"sort"
	"strings"
	"unicode"

	"github.com/HJSunDev/ownward/internal/semantics"
)

type organizationEdge struct {
	Owner string
	Link  semantics.GroundedLink
}

// All indexes are disposable projections of the same durable asset record.
type organizationIndex struct {
	adjacent map[string][]organizationEdge
	names    map[string][]string
	inputs   map[string]map[string]bool
}

func newOrganizationIndex() organizationIndex {
	return organizationIndex{adjacent: map[string][]organizationEdge{}, names: map[string][]string{}, inputs: map[string]map[string]bool{}}
}

func NameTerms(value string) []string {
	seen := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		seen[word] = true
		runes := []rune(word)
		for n := 1; n < len(runes); n++ {
			if unicode.Is(unicode.Han, runes[n-1]) && unicode.Is(unicode.Han, runes[n]) {
				seen[string(runes[n-1:n+1])] = true
			}
		}
	}
	result := make([]string, 0, len(seen))
	for v := range seen {
		result = append(result, v)
	}
	sort.Strings(result)
	return result
}

func organizationNames(record Record) []string {
	if record.Analysis.Organization == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, unit := range record.Analysis.Organization.Units {
		for _, mention := range unit.Mentions {
			for _, term := range NameTerms(mention.Name) {
				seen[term] = true
			}
		}
	}
	result := make([]string, 0, len(seen))
	for term := range seen {
		result = append(result, term)
	}
	return result
}

func (i *Index) addOrganizationLocked(record Record) {
	org := record.Analysis.Organization
	if org == nil {
		return
	}
	for _, input := range Inputs(record) {
		if i.organized.inputs[input.ID] == nil {
			i.organized.inputs[input.ID] = map[string]bool{}
		}
		i.organized.inputs[input.ID][record.AssetID] = true
	}
	for _, name := range organizationNames(record) {
		ids := i.organized.names[name]
		n := sort.SearchStrings(ids, record.AssetID)
		if n == len(ids) || ids[n] != record.AssetID {
			ids = append(ids, "")
			copy(ids[n+1:], ids[n:])
			ids[n] = record.AssetID
		}
		i.organized.names[name] = ids
	}
	for _, link := range org.Links {
		for _, id := range uniqueIDs(link.Source.AssetID, link.Target.AssetID) {
			i.organized.adjacent[id] = append(i.organized.adjacent[id], organizationEdge{record.AssetID, link})
		}
	}
}

func uniqueIDs(a, b string) []string {
	if a == b {
		return []string{a}
	}
	return []string{a, b}
}

func (i *Index) removeOrganizationLocked(record Record) {
	org := record.Analysis.Organization
	if org == nil {
		return
	}
	for _, input := range Inputs(record) {
		delete(i.organized.inputs[input.ID], record.AssetID)
		if len(i.organized.inputs[input.ID]) == 0 {
			delete(i.organized.inputs, input.ID)
		}
	}
	for _, name := range organizationNames(record) {
		ids := i.organized.names[name]
		n := sort.SearchStrings(ids, record.AssetID)
		if n < len(ids) && ids[n] == record.AssetID {
			ids = append(ids[:n], ids[n+1:]...)
		}
		if len(ids) == 0 {
			delete(i.organized.names, name)
		} else {
			i.organized.names[name] = ids
		}
	}
	seen := map[string]bool{}
	for _, link := range org.Links {
		for _, id := range uniqueIDs(link.Source.AssetID, link.Target.AssetID) {
			if seen[id] {
				continue
			}
			seen[id] = true
			edges := i.organized.adjacent[id]
			kept := edges[:0]
			for _, edge := range edges {
				if edge.Owner != record.AssetID {
					kept = append(kept, edge)
				}
			}
			if len(kept) == 0 {
				delete(i.organized.adjacent, id)
			} else {
				i.organized.adjacent[id] = kept
			}
		}
	}
}

func (i *Index) organizationCurrentLocked(record Record) bool {
	if record.Analysis.Organization == nil {
		return false
	}
	for _, input := range Inputs(record) {
		location, ok := i.locations[input.ID]
		if !ok {
			return false
		}
		other := i.records[location].record
		if input.Revision != 0 && input.Revision != other.AssetRevision {
			return false
		}
		if input.OrganizationSnapshot != "" && (other.Analysis.Organization == nil || input.OrganizationSnapshot != other.Analysis.Organization.Snapshot) {
			return false
		}
	}
	return true
}

func (i *Index) OrganizationCurrent(id string) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	location, ok := i.locations[id]
	return ok && i.organizationCurrentLocked(i.records[location].record)
}

func (i *Index) OrganizationLinksCurrent(id string) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	location, ok := i.locations[id]
	if !ok || i.records[location].record.Analysis.Organization == nil {
		return true
	}
	for _, link := range i.records[location].record.Analysis.Organization.Links {
		if _, valid := i.groundedEdgeLocked(organizationEdge{id, link}); !valid {
			return false
		}
	}
	return true
}

func (i *Index) NameSearch(query string, limit int) []SemanticHit {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if limit <= 0 {
		return nil
	}
	if limit > 100 {
		limit = 100
	}
	scores := map[string]float64{}
	scanned := 0
	for _, term := range NameTerms(query) {
		for _, id := range i.organized.names[term] {
			if scanned >= limit*16 {
				break
			}
			scanned++
			location := i.locations[id]
			if i.organizationCurrentLocked(i.records[location].record) {
				scores[id]++
			}
		}
		if scanned >= limit*16 {
			break
		}
	}
	result := make([]SemanticHit, 0, len(scores))
	for id, score := range scores {
		result = append(result, SemanticHit{id, score})
	}
	sort.Slice(result, func(a, b int) bool {
		if result[a].Score == result[b].Score {
			return result[a].AssetID < result[b].AssetID
		}
		return result[a].Score > result[b].Score
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func (i *Index) bindEndpointLocked(endpoint semantics.GraphEndpoint) (semantics.GraphEndpoint, bool) {
	location, ok := i.locations[endpoint.AssetID]
	if !ok {
		return endpoint, false
	}
	record := i.records[location].record
	if record.AssetRevision != endpoint.Revision {
		return endpoint, false
	}
	if endpoint.UnitID == "" {
		return endpoint, true
	}
	org := record.Analysis.Organization
	if !i.organizationCurrentLocked(record) {
		return endpoint, false
	}
	var matches []semantics.SemanticUnit
	for _, unit := range org.Units {
		if org.Snapshot == endpoint.Snapshot && unit.ID == endpoint.UnitID {
			matches = []semantics.SemanticUnit{unit}
			break
		}
		if org.Snapshot != endpoint.Snapshot && endpoint.Fingerprint != "" && semantics.UnitFingerprint(unit) == endpoint.Fingerprint {
			matches = append(matches, unit)
		}
	}
	if len(matches) != 1 {
		return endpoint, false
	}
	unit := matches[0]
	if endpoint.MentionID != "" {
		found := ""
		count := 0
		for _, mention := range unit.Mentions {
			if (endpoint.MentionFingerprint != "" && semantics.MentionFingerprint(mention) == endpoint.MentionFingerprint) || (endpoint.MentionFingerprint == "" && ((endpoint.Selector.Exact != "" && mention.Selector == endpoint.Selector) || (endpoint.Selector.Exact == "" && mention.ID == endpoint.MentionID && org.Snapshot == endpoint.Snapshot))) {
				found = mention.ID
				count++
			}
		}
		if count != 1 {
			return endpoint, false
		}
		endpoint.MentionID = found
		for _, mention := range unit.Mentions {
			if mention.ID == found {
				endpoint.Selector = mention.Selector
			}
		}
	} else if endpoint.Selector.Exact != "" && unit.Selector != endpoint.Selector {
		return endpoint, false
	} else {
		endpoint.Selector = unit.Selector
	}
	endpoint.Snapshot = org.Snapshot
	endpoint.UnitID = unit.ID
	return endpoint, true
}

func (i *Index) groundedEdgeLocked(stored organizationEdge) (Edge, bool) {
	location, ok := i.locations[stored.Owner]
	if !ok || !i.organizationCurrentLocked(i.records[location].record) {
		return Edge{}, false
	}
	link := stored.Link
	link.Conditions = append([]semantics.GraphEndpoint(nil), link.Conditions...)
	link.Source, ok = i.bindEndpointLocked(link.Source)
	if !ok {
		return Edge{}, false
	}
	link.Target, ok = i.bindEndpointLocked(link.Target)
	if !ok {
		return Edge{}, false
	}
	for n := range link.Conditions {
		link.Conditions[n], ok = i.bindEndpointLocked(link.Conditions[n])
		if !ok {
			return Edge{}, false
		}
	}
	return Edge{SourceID: link.Source.AssetID, TargetID: link.Target.AssetID, Type: link.Type, Evidence: link.Meaning, Grounded: &link, OwnerID: stored.Owner}, true
}
