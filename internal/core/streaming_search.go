package core

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"sort"
	"strings"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"

	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func (s *StreamingAssets) compactStreaming(ctx context.Context, generation, id string, score float64, signals []string) (SearchResult, error) {
	var out SearchResult
	m, e := s.Store.ReadAssetMeta(ctx, id, 0)
	if e != nil {
		return out, e
	}
	text, e := s.sourcePrefix(ctx, m, 241)
	if e != nil {
		return out, e
	}
	out = SearchResult{ID: id, Kind: m.Kind, Summary: truncate(text, 240), Score: score, Signals: signals}
	v, e := s.Store.CurrentOrganization(ctx, generation, id)
	if e == nil {
		r, e := s.Store.RecordHeader(ctx, v)
		if e != nil {
			return out, e
		}
		if strings.TrimSpace(r.Analysis.Summary) != "" {
			out.Summary = r.Analysis.Summary
		}
		out.Contexts = semantics.ContextValues(r.Analysis.Contexts)
	} else if !errors.Is(e, sql.ErrNoRows) && !errors.Is(e, boundedstore.ErrNotFound) {
		return out, e
	}
	return out, nil
}

func (s *StreamingAssets) searchTool(ctx context.Context, args streamjson.Node) (*contract.StreamResult, error) {
	query, e := queryField(ctx, args)
	if e != nil {
		return nil, e
	}
	limit, e := integerField(args, "limit")
	if e != nil {
		return nil, e
	}
	if limit < 0 || limit > 100 {
		return nil, errors.New("检索数量必须介于一和一百之间")
	}
	if limit == 0 {
		limit = 10
	}
	if n, ok, e := args.Field("contexts"); e != nil {
		return nil, e
	} else if ok {
		ctx = boundedstore.WithContextFilter(ctx, n)
	}
	identity, complete := smallSource(ctx, query, 256)
	if !complete {
		identity = ""
	}
	identity = strings.TrimSpace(identity)
	var vector []float32
	hasVector := false
	if s.Embedder != nil {
		var e error
		hasVector, e = s.Store.HasVectors(ctx)
		if e != nil {
			return nil, e
		}
	}
	if _, e := s.Store.ReadAssetMeta(ctx, identity, 0); e == nil {
		hasVector = false
	}
	if hasVector {
		var e error
		vector, e = s.embedQuerySource(ctx, query)
		if e != nil {
			vector = nil
		}
	}
	return s.buildRetrieval(ctx, func(ctx context.Context, w io.Writer) error {
		stamp, e := s.Store.RetrievalStamp(ctx)
		if e != nil {
			return e
		}
		generation := stamp.Generation
		limit := limit
		candidateLimit := max(20, limit*4)
		lexical, e := s.Store.LexicalSource(ctx, query, identity, nil, candidateLimit)
		if e != nil {
			return e
		}
		if len(lexical) > 0 && lexical[0].ID == identity && contains(lexical[0].Signals, "identity") {
			r, e := s.compactStreaming(ctx, generation, lexical[0].ID, lexical[0].Score, lexical[0].Signals)
			if e != nil {
				return e
			}
			io.WriteString(w, `{"results":[`)
			if e = s.writeSearchRecord(ctx, w, r, generation, r.ID, nil); e != nil {
				return e
			}
			_, e = io.WriteString(w, "]}")
			return e
		}
		if generation == "" {
			io.WriteString(w, `{"results":[`)
			for i, hit := range lexical[:min(limit, len(lexical))] {
				if i > 0 {
					io.WriteString(w, ",")
				}
				r, e := s.compactStreaming(ctx, generation, hit.ID, hit.Score, hit.Signals)
				if e != nil {
					return e
				}
				if e = s.writeSearchRecord(ctx, w, r, generation, r.ID, nil); e != nil {
					return e
				}
			}
			_, e = io.WriteString(w, "]}")
			return e
		}
		type fused struct {
			score   float64
			signals map[string]struct{}
		}
		items := map[string]*fused{}
		add := func(id, signal string, rank int) {
			v := items[id]
			if v == nil {
				v = &fused{signals: map[string]struct{}{}}
				items[id] = v
			}
			v.score += 1 / float64(60+rank)
			v.signals[signal] = struct{}{}
		}
		for rank, h := range lexical {
			add(h.ID, "lexical", rank+1)
		}
		if len(vector) > 0 {
			_, space, e := s.Store.Generation(ctx)
			if e != nil {
				return e
			}
			hits, _, e := s.Store.VectorSearch(ctx, generation, space, vector, nil, candidateLimit)
			if e != nil {
				return e
			}
			for rank, h := range hits {
				add(h.ID, "semantic", rank+1)
			}
		}
		names, e := s.Store.NameSource(ctx, generation, query, candidateLimit)
		if e != nil {
			return e
		}
		for rank, h := range names {
			if items[h.ID] == nil {
				add(h.ID, "object", rank+1)
			}
		}
		ids := make([]string, 0, len(items))
		for id := range items {
			ids = append(ids, id)
		}
		less := func(a, b string) bool {
			if items[a].score == items[b].score {
				ap, bp := fusedSignalPriority(items[a].signals), fusedSignalPriority(items[b].signals)
				if ap != bp {
					return ap > bp
				}
				return a < b
			}
			return items[a].score > items[b].score
		}
		sort.Slice(ids, func(i, j int) bool { return less(ids[i], ids[j]) })
		direct := map[string]bool{}
		for _, id := range ids[:min(limit, len(ids))] {
			direct[id] = true
		}
		seeds := ids[:min(4, len(ids))]
		seedScores := map[string]float64{}
		for _, id := range seeds {
			seedScores[id] = items[id].score
		}
		page, e := s.Store.NavigatePage(ctx, generation, seeds, nil, 1, candidateLimit)
		if e != nil {
			return e
		}
		entrances := map[string]boundedstore.Edge{}
		related := page.Edges
		scorer, e := s.Store.SourceScorer(ctx, query, nil)
		if e != nil {
			return e
		}
		for _, edge := range related {
			if edge.Grounded != nil && direct[edge.SourceID] && direct[edge.TargetID] {
				continue
			}
			for _, pair := range [][2]string{{edge.SourceID, edge.TargetID}, {edge.TargetID, edge.SourceID}} {
				score, ok := seedScores[pair[0]]
				if !ok {
					continue
				}
				contribution := score * .3 * edge.Confidence
				if edge.Grounded != nil {
					weight, e := s.streamingRelationWeight(ctx, generation, edge, pair[0], scorer)
					if e != nil {
						return e
					}
					contribution = score * weight
				}
				neighbor := pair[1]
				v := items[neighbor]
				if v != nil {
					if edge.Grounded != nil {
						if contribution > v.score {
							v.score = contribution
							v.signals["relation"] = struct{}{}
							entrances[neighbor] = edge
						}
						continue
					}
					if _, ok := v.signals["relation"]; ok && len(v.signals) == 1 {
						v.score = max(v.score, contribution)
					}
					continue
				}
				items[neighbor] = &fused{contribution, map[string]struct{}{"relation": {}}}
				if edge.Grounded != nil {
					entrances[neighbor] = edge
				} else if len(seeds) > 0 && pair[0] == seeds[0] {
					items[pair[0]].signals["relation"] = struct{}{}
				}
			}
		}
		ids = ids[:0]
		for id := range items {
			ok, e := s.Store.MatchesEffectiveContexts(ctx, generation, id, nil)
			if e != nil {
				return e
			}
			if ok {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return less(ids[i], ids[j]) })
		if len(ids) >= 2 && streamingDirectlyRelated(ids[0], ids[1], related) {
			items[ids[0]].signals["relation"] = struct{}{}
			items[ids[1]].signals["relation"] = struct{}{}
		}
		ids = ids[:min(limit, len(ids))]
		selected := map[string]bool{}
		for _, id := range ids {
			if e, ok := entrances[id]; ok {
				selected[e.Grounded.ID] = true
			}
		}
		io.WriteString(w, `{"results":[`)
		for i, id := range ids {
			if i > 0 {
				io.WriteString(w, ",")
			}
			v := items[id]
			signals := make([]string, 0, len(v.signals))
			for signal := range v.signals {
				signals = append(signals, signal)
			}
			sort.Strings(signals)
			result, e := s.compactStreaming(ctx, generation, id, v.score, signals)
			if e != nil {
				return e
			}
			if e = s.completeSearchEvidence(ctx, generation, query, &result); e != nil {
				return e
			}
			var relationEdges []boundedstore.Edge
			for _, edge := range related {
				if edge.Grounded == nil || edge.SourceID == edge.TargetID || (edge.SourceID != id && edge.TargetID != id) || direct[edge.SourceID] && direct[edge.TargetID] || !selected[edge.Grounded.ID] {
					continue
				}
				relation, e := s.streamingRelationEvidence(ctx, generation, edge)
				if e != nil {
					return e
				}
				result.Relations = append(result.Relations, *relation)
				relationEdges = append(relationEdges, edge)
				if !contains(result.Signals, "relation") {
					result.Signals = append(result.Signals, "relation")
					sort.Strings(result.Signals)
				}
				if len(result.Relations) == 2 {
					break
				}
			}
			if e = s.writeSearchRecord(ctx, w, result, generation, id, relationEdges); e != nil {
				return e
			}
		}
		_, e = io.WriteString(w, "]}")
		return e
	})
}

func (s *StreamingAssets) navigateTool(ctx context.Context, args streamjson.Node) (*contract.StreamResult, error) {
	var input struct {
		Start []string `json:"start_ids"`
		Types []string `json:"relation_types"`
		Depth int      `json:"depth"`
		Limit int      `json:"limit"`
	}
	if e := args.DecodeSmall(&input, 64*1024); e != nil {
		return nil, e
	}
	if len(input.Start) == 0 {
		return nil, errors.New("关系导航至少需要一个起点")
	}
	return s.buildRetrieval(ctx, func(ctx context.Context, w io.Writer) error {
		generation, _, e := s.Store.Generation(ctx)
		if e != nil {
			return e
		}
		page, e := s.Store.NavigatePage(ctx, generation, input.Start, input.Types, input.Depth, input.Limit)
		if e != nil {
			return e
		}
		result := contract.NavigationResult{Continuation: page.Continuation, Incomplete: page.Incomplete, Nodes: []NavigationNode{}, Edges: []contract.NavigationEdge{}}
		ids := map[string]bool{}
		for _, id := range input.Start {
			ids[id] = true
		}
		for _, edge := range page.Edges {
			relation, e := s.streamingRelationEvidence(ctx, generation, edge)
			if e != nil {
				return e
			}
			result.Edges = append(result.Edges, contract.NavigationEdge{Grounded: relation, SourceID: edge.SourceID, TargetID: edge.TargetID, Type: edge.Type, Confidence: edge.Confidence, Evidence: edge.Evidence, Depth: edge.Depth})
			ids[edge.SourceID] = true
			ids[edge.TargetID] = true
		}
		for id := range ids {
			m, e := s.Store.ReadAssetMeta(ctx, id, 0)
			if errors.Is(e, boundedstore.ErrNotFound) {
				continue
			}
			if e != nil {
				return e
			}
			r, e := s.compactStreaming(ctx, generation, id, 0, nil)
			if e != nil {
				return e
			}
			n := NavigationNode{ID: id, Kind: m.Kind, Summary: r.Summary, Contexts: r.Contexts, UpdatedAt: m.UpdatedAt}
			if v, e := s.Store.CurrentOrganization(ctx, generation, id); e == nil {
				record, e := s.Store.RecordHeader(ctx, v)
				if e != nil {
					return e
				}
				n.Cues = record.Analysis.Cues
			}
			result.Nodes = append(result.Nodes, n)
		}
		sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].ID < result.Nodes[j].ID })
		return s.writeNavigationResult(ctx, w, generation, result, page.Edges)
	})
}
