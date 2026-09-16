package core

import (
	"bufio"
	"context"

	"io"
	"strings"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"

	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func (s *StreamingAssets) streamingRelationWeight(ctx context.Context, generation string, edge boundedstore.Edge, seed string, scorer boundedstore.TextScorer) (float64, error) {
	if edge.Grounded.Type == "related_to" {
		return .3, nil
	}
	m, e := s.Store.ReadAssetMeta(ctx, seed, 0)
	if e != nil {
		return 0, e
	}
	texts := &streamingTexts{ctx: ctx, s: s, bodies: map[string]*streamedBody{}, revisions: map[string]uint64{seed: m.Revision}}
	defer texts.close()
	body, e := texts.body(seed)
	if e != nil {
		return 0, e
	}
	whole := 0.0
	reader := bufio.NewReaderSize(io.NewSectionReader(body.file, 0, body.size), streamjson.BufferBytes)
	offset, start := int64(0), int64(0)
	last := byte(0)
	for {
		c, e := reader.ReadByte()
		if e == io.EOF {
			v, e := scorer.ScoreReader(io.NewSectionReader(body.file, start, offset-start))
			if e != nil {
				return 0, e
			}
			whole = max(whole, v)
			break
		}
		if e != nil {
			return 0, e
		}
		offset++
		if c == '\n' && last == '\n' {
			v, e := scorer.ScoreReader(io.NewSectionReader(body.file, start, offset-start-2))
			if e != nil {
				return 0, e
			}
			whole = max(whole, v)
			start = offset
			last = 0
		} else {
			last = c
		}
	}
	endpoint := edge.Grounded.Source
	loc := edge.Endpoints[0]
	if endpoint.AssetID != seed {
		endpoint = edge.Grounded.Target
		loc = edge.Endpoints[1]
	}
	passages := func(spans []boundedstore.GraphSpan) ([]io.Reader, func(), error) {
		var readers []io.Reader
		var open []io.Closer
		done := func() {
			for _, r := range open {
				r.Close()
			}
		}
		for i, span := range spans {
			r, e := (runeSliceSource{body, span.Start, span.End}).Open(ctx)
			if e != nil {
				done()
				return nil, func() {}, e
			}
			open = append(open, r)
			if i > 0 {
				readers = append(readers, strings.NewReader(" "))
			}
			readers = append(readers, r)
		}
		return readers, done, nil
	}
	e = s.Store.VisitUnits(ctx, generation, seed, func(unit boundedstore.UnitProjection) error {
		readers, done, e := passages(append([]boundedstore.GraphSpan{unit.Span}, unit.Context...))
		if e != nil {
			return e
		}
		defer done()
		v, e := scorer.ScoreReader(io.MultiReader(readers...))
		whole = max(whole, v)
		return e
	})
	if e != nil {
		return 0, e
	}
	if whole == 0 {
		return .3, nil
	}
	readers, done, e := passages(append([]boundedstore.GraphSpan{loc.Span}, loc.Context...))
	if e != nil {
		return 0, e
	}
	defer done()
	meaning, e := s.Store.OpenGraphText(ctx, edge.Meaning)
	if e != nil {
		return 0, e
	}
	defer meaning.Close()
	r, e := meaning.Root().Open(ctx)
	if e != nil {
		return 0, e
	}
	defer r.Close()
	readers = append(readers, strings.NewReader(" "), r)
	support, e := scorer.ScoreReader(io.MultiReader(readers...))
	return max(.3, .96*min(1, support/whole)), e
}

func (s *StreamingAssets) streamingRelationEvidence(ctx context.Context, generation string, edge boundedstore.Edge) (*contract.RelationEvidence, error) {
	if edge.Grounded == nil {
		return nil, nil
	}
	link := edge.Grounded
	result := &contract.RelationEvidence{ID: link.ID, Origin: "derived_interpretation", Type: link.Type, Meaning: link.Meaning}
	texts := &streamingTexts{ctx: ctx, s: s, bodies: map[string]*streamedBody{}, revisions: map[string]uint64{}}
	defer texts.close()
	var lines []domain.EvidenceReference
	endpoints := append([]semantics.GraphEndpoint{link.Source, link.Target}, link.Conditions...)
	for n, ep := range endpoints {
		texts.revisions[ep.AssetID] = ep.Revision
		body, e := texts.body(ep.AssetID)
		if e != nil {
			return nil, e
		}
		loc := edge.Endpoints[n]
		ref, e := s.rangeReference(ctx, body.meta, loc.Span.Start, loc.Span.End)
		if e != nil {
			return nil, e
		}
		for _, boundary := range []int{ref.StartRune, ref.EndRune} {
			if boundary <= 0 || boundary >= body.length {
				continue
			}
			b := bufio.NewReaderSize(io.NewSectionReader(body.file, 0, body.size), streamjson.BufferBytes)
			start, end := 0, body.length
			previous := rune(0)
			skip := false
			for at := 0; at < body.length; at++ {
				c, _, e := b.ReadRune()
				if e != nil {
					return nil, e
				}
				if at == boundary && (previous == '\n' || c == '\n') {
					skip = true
					break
				}
				if at < boundary && c == '\n' {
					start = at + 1
				}
				if at >= boundary && c == '\n' {
					end = at + 1
					break
				}
				previous = c
			}
			if skip || start >= ref.StartRune && end <= ref.EndRune {
				continue
			}
			r, e := s.rangeReference(ctx, body.meta, int64(start), int64(end))
			if e != nil {
				return nil, e
			}
			lines = append(lines, r)
		}
		if n == 0 {
			result.Source = ref
		} else if n == 1 {
			result.Target = ref
		} else {
			result.Conditions = append(result.Conditions, ref)
		}
		for _, span := range loc.Context {
			r, e := s.rangeReference(ctx, body.meta, span.Start, span.End)
			if e != nil {
				return nil, e
			}
			result.Context = append(result.Context, r)
		}

	}
	covered := append([]domain.EvidenceReference{result.Source, result.Target}, result.Conditions...)
	covered = append(covered, result.Context...)
	for _, candidate := range lines {
		found := false
		for _, r := range covered {
			if r.SourceID == candidate.SourceID && r.SourceRevision == candidate.SourceRevision && r.StartRune <= candidate.StartRune && r.EndRune >= candidate.EndRune {
				found = true
				break
			}
		}
		if !found {
			result.Context = append(result.Context, candidate)
			covered = append(covered, candidate)
		}
	}
	return result, nil
}

func (s *StreamingAssets) completeSearchEvidence(ctx context.Context, generation string, query contract.ContentSource, result *SearchResult) error {
	m, e := s.Store.ReadAssetMeta(ctx, result.ID, 0)
	if e != nil {
		return e
	}
	prefix, e := s.sourcePrefix(ctx, m, 514)
	if e != nil {
		return e
	}
	if m.ContentBytes > int64(len(prefix)) || strings.TrimSpace(result.Summary) != strings.TrimSpace(prefix) {
		result.Evidence, e = s.Store.RankEvidenceSource(ctx, generation, result.ID, query, 3)
		if e != nil {
			return e
		}
	}
	result.Summary, e = s.Store.EvidenceSummarySource(ctx, result.ID, query, result.Summary, result.Evidence)
	return e
}
