package core

import (
	"bufio"
	"context"
	"errors"
	"io"
	"unicode"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/semanticstream"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

type semanticResolver struct {
	*streamingTexts
	generation string
	references map[string]semantics.CandidateReference
	documents  map[string]*streamjson.Document
}

func (r *semanticResolver) close() {
	r.streamingTexts.close()
	for _, d := range r.documents {
		d.Close()
	}
}
func (r *semanticResolver) Whole(id string) (semanticstream.Text, error) {
	body, e := r.body(id)
	return semanticstream.Text{Source: body}, e
}
func (r *semanticResolver) Length(id string) (int64, error) {
	body, e := r.body(id)
	if e != nil {
		return 0, e
	}
	return int64(body.length), nil
}
func (r *semanticResolver) Resolve(id string, s semanticstream.Selector) (semanticstream.Span, error) {
	body, e := r.body(id)
	if e != nil {
		return semanticstream.Span{}, e
	}
	blank, e := s.Exact.Blank(r.ctx)
	if e != nil {
		return semanticstream.Span{}, e
	}
	if blank {
		return semanticstream.Span{}, errors.New("说明定位的原文不能为空")
	}
	start, end, e := streamjson.ResolveSelector(r.ctx, r.s.Scratch, body, s.Prefix, s.Exact, s.Suffix)
	return semanticstream.Span{Start: start, End: end}, e
}
func (r *semanticResolver) Candidate(id string, wanted semanticstream.Text) (uint64, *semanticstream.Organization, error) {
	ref, ok := r.references[id]
	if !ok {
		return 0, nil, errors.New("关联端点不在当前工作或版本已变化")
	}
	if ref.OrganizationSnapshot == "" {
		return ref.Revision, nil, nil
	}
	for key, doc := range r.documents {
		doc.Close()
		delete(r.documents, key)
	}
	v, e := r.s.Store.CurrentOrganization(r.ctx, r.generation, id)
	if e != nil {
		return 0, nil, e
	}
	if v.Snapshot != ref.OrganizationSnapshot {
		return 0, nil, errors.New("语义调用的实际组织输入已变化")
	}
	raw, e := r.s.Store.OpenOrganization(r.ctx, v)
	if e != nil {
		return 0, nil, e
	}
	doc, e := streamjson.Parse(r.ctx, r.s.Scratch, raw, resourcebudget.FromContext(r.ctx, r.s.Budget), r.s.DiskBytes)
	raw.Close()
	if e != nil {
		return 0, nil, e
	}
	a, _, e := doc.Root().Field("analysis")
	if e != nil {
		doc.Close()
		return 0, nil, e
	}
	o, _, e := a.Field("organization")
	if e != nil {
		doc.Close()
		return 0, nil, e
	}
	org := &semanticstream.Organization{Schema: semantics.OrganizationSchema, Snapshot: ref.OrganizationSnapshot}
	e = semanticArray(o, "units", func(n streamjson.Node) (bool, error) {
		id, ok, e := n.Field("id")
		if e != nil || !ok {
			return false, e
		}
		equal, e := semanticstream.Compare(r.ctx, semanticstream.Text{Source: id}, wanted)
		if e != nil {
			return false, e
		}
		if equal != 0 {
			return false, nil
		}
		unit, e := semanticstream.ReadUnit(n)
		if e != nil {
			return false, e
		}
		org.Units = []semanticstream.Unit{unit}
		return true, nil
	})
	if e != nil {
		doc.Close()
		return 0, nil, e
	}
	r.documents[id] = doc
	return ref.Revision, org, nil

}

type runeSliceSource struct {
	body       *streamedBody
	start, end int64
}

func (s runeSliceSource) Open(ctx context.Context) (io.ReadCloser, error) {
	if s.start == s.end {
		return io.NopCloser(io.NewSectionReader(s.body.file, 0, 0)), nil
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(s.body.file, 0, s.body.size), streamjson.BufferBytes)
	var start, end int64
	for i := int64(0); i < s.end; i++ {
		if i == s.start {
			start = end
		}
		_, size, e := reader.ReadRune()
		if e != nil {
			return nil, e
		}
		end += int64(size)
		if i%16384 == 0 {
			if e = ctx.Err(); e != nil {
				return nil, e
			}
		}
	}
	return io.NopCloser(io.NewSectionReader(s.body.file, start, end-start)), nil
}

type foldedSource struct{ source contract.ContentSource }

func (f foldedSource) Open(ctx context.Context) (io.ReadCloser, error) {
	source, e := f.source.Open(ctx)
	if e != nil {
		return nil, e
	}
	reader, writer := io.Pipe()
	go func() {
		defer source.Close()
		b := bufio.NewReaderSize(source, streamjson.BufferBytes)
		out := bufio.NewWriterSize(writer, streamjson.BufferBytes)
		var fail error
		for {
			v, _, e := b.ReadRune()
			if e == io.EOF {
				break
			}
			if e != nil {
				fail = e
				break
			}
			fold := v
			for x := unicode.SimpleFold(v); x != v; x = unicode.SimpleFold(x) {
				fold = min(fold, x)
			}
			if _, e = out.WriteRune(fold); e != nil {
				fail = e
				break
			}
			if e = ctx.Err(); e != nil {
				fail = e
				break
			}
		}
		if e := out.Flush(); fail == nil {
			fail = e
		}
		writer.CloseWithError(fail)
	}()
	return reader, nil
}

func (r *semanticResolver) Mention(id string, u semanticstream.Unit, name semanticstream.Text) (semanticstream.Selector, error) {
	if empty, e := name.Empty(r.ctx); e != nil {
		return semanticstream.Selector{}, e
	} else if empty {
		return semanticstream.Selector{}, errors.New("对象名称为空")
	}
	body, e := r.body(id)
	if e != nil {
		return semanticstream.Selector{}, e
	}
	found, end := int64(-1), int64(0)
	for _, selector := range append([]semanticstream.Selector{u.Selector}, u.Context...) {
		span, e := r.Resolve(id, selector)
		if e != nil {
			return semanticstream.Selector{}, e
		}
		source := runeSliceSource{body, span.Start, span.End}
		a, b, e := streamjson.ResolveSelector(r.ctx, r.s.Scratch, foldedSource{source}, boundedstore.StringSource(""), foldedSource{name}, boundedstore.StringSource(""))
		if errors.Is(e, streamjson.ErrSelectorMismatch) {
			continue
		}
		if e != nil {
			return semanticstream.Selector{}, e
		}
		a += span.Start
		b += span.Start
		if found >= 0 && found != a {
			return semanticstream.Selector{}, streamjson.ErrSelectorAmbiguous
		}
		found, end = a, b
	}
	if found < 0 {
		return semanticstream.Selector{}, errors.New("名称没有唯一原文位置，请提供 exact 与必要邻文")
	}
	for width := int64(16); ; width *= 2 {
		sel := semanticstream.Selector{Exact: semanticstream.Text{Source: runeSliceSource{body, found, end}}, Prefix: semanticstream.Text{Source: runeSliceSource{body, max(0, found-width), found}}, Suffix: semanticstream.Text{Source: runeSliceSource{body, end, min(int64(body.length), end+width)}}}
		span, e := r.Resolve(id, sel)
		if e == nil && span.Start == found {
			return sel, nil
		}
		if e != nil && !errors.Is(e, streamjson.ErrSelectorMismatch) && !errors.Is(e, streamjson.ErrSelectorAmbiguous) {
			return semanticstream.Selector{}, e
		}
		if width >= int64(body.length) {
			return semanticstream.Selector{}, errors.New("对象定位不能唯一恢复")
		}
	}
}
