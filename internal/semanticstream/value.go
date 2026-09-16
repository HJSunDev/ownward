// Package semanticstream represents semantic locators and judgments as disk
// backed values. It preserves the semantic contract without loading sources.
package semanticstream

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

type literal string

func (s literal) Open(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(s))), nil
}

type Text struct{ Source contract.ContentSource }

func Literal(s string) Text { return Text{literal(s)} }
func (t Text) Open(ctx context.Context) (io.ReadCloser, error) {
	if t.Source == nil {
		return literal("").Open(ctx)
	}
	return t.Source.Open(ctx)
}
func (t Text) Empty(ctx context.Context) (bool, error) {
	r, e := t.Open(ctx)
	if e != nil {
		return false, e
	}
	defer r.Close()
	var b [1]byte
	n, e := r.Read(b[:])
	if e == io.EOF {
		e = nil
	}
	return n == 0, e
}
func (t Text) Blank(ctx context.Context) (bool, error) {
	r, e := t.Open(ctx)
	if e != nil {
		return false, e
	}
	defer r.Close()
	b := bufio.NewReaderSize(r, streamjson.BufferBytes)
	for {
		v, _, e := b.ReadRune()
		if e == io.EOF {
			return true, nil
		}
		if e != nil {
			return false, e
		}
		if !unicode.IsSpace(v) {
			return false, nil
		}
	}
}
func (t Text) Write(ctx context.Context, w io.Writer) error {
	r, e := t.Open(ctx)
	if e != nil {
		return e
	}
	defer r.Close()
	return streamjson.WriteString(ctx, w, r)
}
func (t Text) Digest(ctx context.Context) (string, error) {
	h := sha256.New()
	r, e := t.Open(ctx)
	if e != nil {
		return "", e
	}
	defer r.Close()
	if _, e = io.CopyBuffer(h, r, make([]byte, streamjson.BufferBytes)); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func Compare(ctx context.Context, a, b Text) (int, error) {
	x, e := a.Open(ctx)
	if e != nil {
		return 0, e
	}
	defer x.Close()
	y, e := b.Open(ctx)
	if e != nil {
		return 0, e
	}
	defer y.Close()
	xr, yr := bufio.NewReaderSize(x, 4096), bufio.NewReaderSize(y, 4096)
	for {
		av, ae := xr.ReadByte()
		bv, be := yr.ReadByte()
		if ae != nil && ae != io.EOF {
			return 0, ae
		}
		if be != nil && be != io.EOF {
			return 0, be
		}
		if ae == io.EOF && be == io.EOF {
			return 0, nil
		}
		if ae == io.EOF {
			return -1, nil
		}
		if be == io.EOF {
			return 1, nil
		}
		if av < bv {
			return -1, nil
		}
		if av > bv {
			return 1, nil
		}
	}
}
func textField(n streamjson.Node, key string) (Text, error) {
	v, ok, e := n.Field(key)
	if e != nil || !ok || v.Kind == 'n' {
		return Text{}, e
	}
	if v.Kind != '"' {
		return Text{}, errors.New("语义文本字段格式无效")
	}
	return Text{v.TextReference()}, nil
}
func smallField(n streamjson.Node, key string) (string, error) {
	v, ok, e := n.Field(key)
	if e != nil || !ok || v.Kind == 'n' {
		return "", e
	}
	limit := 68
	if key == "asset_id" {
		limit = 256
	}
	// These are kernel identities, hashes and schema/type names. Text selected
	// or authored by the model uses Text and remains backed by the document.
	value, e := v.String(int64(limit*6 + 2))
	if e != nil {
		return "", e
	}
	if len(value) > limit {
		return "", errors.New("组织引用身份无效")
	}
	return value, nil
}
func each(n streamjson.Node, key string, limit int, fn func(streamjson.Node) error) error {
	v, ok, e := n.Field(key)
	if e != nil || !ok || v.Kind == 'n' {
		return e
	}
	if v.Kind != '[' || v.Count > int64(limit) {
		return errors.New("关系组织格式或工作量无效")
	}
	c := v.Children()
	for {
		child, e := c.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		if e = fn(child); e != nil {
			return e
		}
	}
}

type field struct {
	key   string
	write func(io.Writer) error
	omit  bool
}

func object(w io.Writer, fields ...field) error {
	if _, e := io.WriteString(w, "{"); e != nil {
		return e
	}
	first := true
	for _, f := range fields {
		if f.omit {
			continue
		}
		if !first {
			if _, e := io.WriteString(w, ","); e != nil {
				return e
			}
		}
		first = false
		key, _ := json.Marshal(f.key)
		if _, e := w.Write(key); e != nil {
			return e
		}
		if _, e := io.WriteString(w, ":"); e != nil {
			return e
		}
		if e := f.write(w); e != nil {
			return e
		}
	}
	_, e := io.WriteString(w, "}")
	return e
}
func valueField(key string, value any, omit bool) field {
	return field{key, func(w io.Writer) error {
		data, e := json.Marshal(value)
		if e != nil {
			return e
		}
		_, e = w.Write(data)
		return e
	}, omit}
}
func textJSON(ctx context.Context, key string, t Text, optional bool) (field, error) {
	empty, e := t.Empty(ctx)
	return field{key, func(w io.Writer) error { return t.Write(ctx, w) }, optional && empty}, e
}
func digestJSON(write func(io.Writer) error) (string, error) {
	h := sha256.New()
	if e := write(h); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)[:16]), nil
}
