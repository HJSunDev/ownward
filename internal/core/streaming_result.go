package core

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"strings"
	"unicode"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func (s *StreamingAssets) writeProjected(ctx context.Context, w io.Writer, value any, replace map[string]func(io.Writer) error) error {
	data, e := json.Marshal(value)
	if e != nil {
		return e
	}
	doc, e := streamjson.Parse(ctx, s.Scratch, strings.NewReader(string(data)), resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes)
	if e != nil {
		return e
	}
	defer doc.Close()
	return doc.Root().Object(w, replace)
}
func lowerTextDigest(ctx context.Context, source contract.ContentSource) ([32]byte, error) {
	var result [32]byte
	r, e := source.Open(ctx)
	if e != nil {
		return result, e
	}
	defer r.Close()
	b := bufio.NewReaderSize(r, streamjson.BufferBytes)
	h := sha256.New()
	for {
		v, _, e := b.ReadRune()
		if e == io.EOF {
			break
		}
		if e != nil {
			return result, e
		}
		io.WriteString(h, string(unicode.ToLower(v)))
	}
	copy(result[:], h.Sum(nil))
	return result, ctx.Err()
}
func (s *StreamingAssets) mergedContextDocument(ctx context.Context, id string, inferred []domain.Context) (*streamjson.Document, error) {
	m, e := s.Store.ReadAssetMeta(ctx, id, 0)
	if e != nil {
		return nil, e
	}
	details, e := s.detailsDocument(ctx, m)
	if e != nil {
		return nil, e
	}
	defer details.Close()
	inferred = normalizeContexts(inferred)
	keys := make([][32]byte, len(inferred))
	hidden := make([]bool, len(inferred))
	for i, c := range inferred {
		keys[i], e = lowerTextDigest(ctx, boundedstore.StringSource(c.Key))
		if e != nil {
			return nil, e
		}
	}
	return streamjson.Build(ctx, s.Scratch, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes, func(w io.Writer) error {
		io.WriteString(w, "[")
		first := true
		e := semanticArray(details.Root(), "contexts", func(n streamjson.Node) (bool, error) {
			k, _, e := n.Field("key")
			if e != nil {
				return false, e
			}
			hash, e := lowerTextDigest(ctx, k)
			if e != nil {
				return false, e
			}
			for i, d := range keys {
				hidden[i] = hidden[i] || d == hash
			}
			if !first {
				io.WriteString(w, ",")
			}
			first = false
			return false, n.Copy(w)
		})
		if e != nil {
			return e
		}
		for i, c := range inferred {
			if hidden[i] {
				continue
			}
			if !first {
				io.WriteString(w, ",")
			}
			first = false
			if e = writeJSON(w, c); e != nil {
				return e
			}
		}
		_, e = io.WriteString(w, "]")
		return e
	})
}
func (s *StreamingAssets) writeRelation(ctx context.Context, w io.Writer, r contract.RelationEvidence, edge boundedstore.Edge) error {
	return s.writeProjected(ctx, w, r, map[string]func(io.Writer) error{"meaning": func(w io.Writer) error { return s.Store.WriteGraphMeaning(ctx, w, edge.Meaning) }})
}
func (s *StreamingAssets) writeSearchRecord(ctx context.Context, w io.Writer, r SearchResult, generation, id string, edges []boundedstore.Edge) error {
	contexts, e := s.mergedContextDocument(ctx, id, r.Contexts)
	if e != nil {
		return e
	}
	defer contexts.Close()
	r.Contexts = nil
	if contexts.Root().Count > 0 {
		r.Contexts = []domain.Context{{}}
	}
	return s.writeProjected(ctx, w, r, map[string]func(io.Writer) error{
		"contexts": contexts.Root().Copy,
		"relations": func(w io.Writer) error {
			io.WriteString(w, "[")
			for i, relation := range r.Relations {
				if i > 0 {
					io.WriteString(w, ",")
				}
				if e := s.writeRelation(ctx, w, relation, edges[i]); e != nil {
					return e
				}
			}
			_, e := io.WriteString(w, "]")
			return e
		},
	})
}
func streamingDirectlyRelated(a, b string, edges []boundedstore.Edge) bool {
	for _, e := range edges {
		if e.Grounded != nil {
			continue
		}
		if e.SourceID == a && e.TargetID == b || e.SourceID == b && e.TargetID == a {
			return true
		}
	}
	return false
}
func (s *StreamingAssets) writeNavigationResult(ctx context.Context, w io.Writer, generation string, result contract.NavigationResult, edges []boundedstore.Edge) error {
	io.WriteString(w, `{"result":`)
	e := s.writeProjected(ctx, w, result, map[string]func(io.Writer) error{
		"nodes": func(w io.Writer) error {
			io.WriteString(w, "[")
			for i, n := range result.Nodes {
				if i > 0 {
					io.WriteString(w, ",")
				}
				contexts, e := s.mergedContextDocument(ctx, n.ID, n.Contexts)
				if e != nil {
					return e
				}
				n.Contexts = nil
				if contexts.Root().Count > 0 {
					n.Contexts = []domain.Context{{}}
				}
				e = s.writeProjected(ctx, w, n, map[string]func(io.Writer) error{"contexts": contexts.Root().Copy})
				contexts.Close()
				if e != nil {
					return e
				}
			}
			_, e := io.WriteString(w, "]")
			return e
		},
		"edges": func(w io.Writer) error {
			io.WriteString(w, "[")
			for i, edge := range result.Edges {
				if i > 0 {
					io.WriteString(w, ",")
				}
				if edge.Grounded == nil {
					if e := writeJSON(w, edge); e != nil {
						return e
					}
					continue
				}
				edge.Evidence = "streamed"
				if e := s.writeProjected(ctx, w, edge, map[string]func(io.Writer) error{"evidence": func(w io.Writer) error { return s.Store.WriteGraphMeaning(ctx, w, edges[i].Meaning) }, "grounded": func(w io.Writer) error { return s.writeRelation(ctx, w, *edge.Grounded, edges[i]) }}); e != nil {
					return e
				}
			}
			_, e := io.WriteString(w, "]")
			return e
		},
	})
	if e != nil {
		return e
	}
	_, e = io.WriteString(w, "}")
	return e
}
