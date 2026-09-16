package streamjson

import (
	"bufio"
	"context"
	"crypto/sha256"
	"io"
	"unicode"
)

func countRunes(r io.Reader) (int64, error) {
	b := bufio.NewReaderSize(r, BufferBytes)
	var count int64
	for {
		_, _, err := b.ReadRune()
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return 0, err
		}
		count++
	}
}

// TrimmedText 按UTF-8字节定位首尾，重复打开时流式略过空白，不保留大字符串。
type TrimmedText struct {
	Node          Node
	Start, Length int64
}

func (n Node) Trimmed(ctx context.Context) (TrimmedText, error) {
	r, err := n.Open(ctx)
	if err != nil {
		return TrimmedText{}, err
	}
	defer r.Close()
	b := bufio.NewReaderSize(r, BufferBytes)
	var pos, start, end int64
	start = -1
	for {
		v, size, err := b.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			return TrimmedText{}, err
		}
		if !unicode.IsSpace(v) {
			if start < 0 {
				start = pos
			}
			end = pos + int64(size)
		}
		pos += int64(size)
	}
	if start < 0 {
		start = 0
	}
	return TrimmedText{n, start, end - start}, nil
}

type sectionCloser struct {
	io.Reader
	io.Closer
}

func (t TrimmedText) Open(ctx context.Context) (io.ReadCloser, error) {
	r, err := t.Node.Open(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = io.CopyN(io.Discard, r, t.Start); err != nil {
		r.Close()
		return nil, err
	}
	return sectionCloser{io.LimitReader(r, t.Length), r}, nil
}
func (t TrimmedText) WriteJSON(ctx context.Context, w io.Writer) error {
	r, err := t.Open(ctx)
	if err != nil {
		return err
	}
	defer r.Close()
	return WriteString(ctx, w, r)
}

// FoldedPairDigest 保持既有场景去重的 strings.ToLower(key)+NUL+strings.ToLower(value) 规则。
func FoldedPairDigest(ctx context.Context, key, value TrimmedText) ([]byte, error) {
	h := sha256.New()
	for i, t := range []TrimmedText{key, value} {
		if i > 0 {
			h.Write([]byte{0})
		}
		r, err := t.Open(ctx)
		if err != nil {
			return nil, err
		}
		b := bufio.NewReaderSize(r, BufferBytes)
		for {
			v, _, err := b.ReadRune()
			if err == io.EOF {
				break
			}
			if err != nil {
				r.Close()
				return nil, err
			}
			io.WriteString(h, string(unicode.ToLower(v)))
		}
		r.Close()
	}
	return h.Sum(nil), nil
}

// RawSource 与字符串解码区分：返回整个JSON值的原始序列化。
type RawSource struct{ Node Node }

func (s RawSource) Open(ctx context.Context) (io.ReadCloser, error) {
	return io.NopCloser(contextReader{ctx, s.Node.Raw()}), nil
}
