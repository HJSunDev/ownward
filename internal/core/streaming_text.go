package core

import (
	"context"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

type streamedBody struct {
	file   *resourcebudget.File
	size   int64
	length int
	meta   contract.AssetMeta
}

func (b *streamedBody) Open(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(io.NewSectionReader(b.file, 0, b.size)), nil
}

type streamingTexts struct {
	ctx       context.Context
	s         *StreamingAssets
	bodies    map[string]*streamedBody
	revisions map[string]uint64
}

func (t *streamingTexts) close() {
	for _, b := range t.bodies {
		b.file.Close()
	}
}
func (t *streamingTexts) body(id string) (*streamedBody, error) {
	if b := t.bodies[id]; b != nil {
		return b, nil
	}
	revision, ok := t.revisions[id]
	if !ok {
		return nil, errors.New("原文不在语义输入范围内")
	}
	m, e := t.s.Store.ReadAssetMeta(t.ctx, id, revision)
	if e != nil {
		return nil, e
	}
	f, e := resourcebudget.TempFile(t.ctx, t.s.Scratch, "semantic-source-", t.s.DiskBytes)
	if e != nil {
		return nil, e
	}
	r, e := t.s.Store.OpenContent(t.ctx, id, revision)
	if e != nil {
		f.Close()
		return nil, e
	}
	counter := &runeCounter{Reader: r}
	n, e := io.CopyBuffer(f, counter, make([]byte, streamjson.BufferBytes))
	r.Close()
	if e != nil {
		f.Close()
		return nil, e
	}
	b := &streamedBody{f, n, int(counter.count), m}
	t.bodies[id] = b
	return b, nil
}
