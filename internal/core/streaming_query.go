package core

import (
	"context"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func queryField(ctx context.Context, args streamjson.Node) (contract.ContentSource, error) {
	n, ok, e := args.Field("query")
	if e != nil {
		return nil, e
	}
	if !ok {
		return nil, errors.New("检索内容不能为空")
	}
	text, e := n.Trimmed(ctx)
	if e != nil {
		return nil, e
	}
	r, e := text.Open(ctx)
	if e != nil {
		return nil, e
	}
	var b [1]byte
	size, e := r.Read(b[:])
	r.Close()
	if e != nil && e != io.EOF {
		return nil, e
	}
	if size == 0 {
		return nil, errors.New("检索内容不能为空")
	}
	// Leading/trailing query whitespace is retained for the model, as before.
	return n, nil
}
func integerField(n streamjson.Node, name string) (int, error) {
	v, ok, e := n.Field(name)
	if e != nil || !ok {
		return 0, e
	}
	var result int
	e = v.DecodeSmall(&result, 128)
	return result, e
}
func smallSource(ctx context.Context, s contract.ContentSource, limit int64) (string, bool) {
	r, e := s.Open(ctx)
	if e != nil {
		return "", false
	}
	defer r.Close()
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	return string(b), e == nil && int64(len(b)) <= limit
}
func (s *StreamingAssets) embedQuerySource(ctx context.Context, source contract.ContentSource) ([]float32, error) {
	if p, ok := s.Embedder.(interface {
		EmbedQueryReader(context.Context, io.Reader) ([]float32, error)
	}); ok {
		r, e := source.Open(ctx)
		if e != nil {
			return nil, e
		}
		defer r.Close()
		return p.EmbedQueryReader(ctx, r)
	}
	value, ok := smallSource(ctx, source, 8192)
	if !ok {
		return nil, errors.New("向量提供方不支持流式查询")
	}
	return s.Embedder.EmbedQuery(ctx, value)
}
