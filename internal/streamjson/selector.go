package streamjson

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"io"

	"github.com/HJSunDev/ownward/internal/contract"
)

var ErrSelectorAmbiguous = errors.New("说明原文不唯一，请提供相邻原文消歧")
var ErrSelectorMismatch = errors.New("说明定位与当前正文失配，请同步修正定位")

// ResolveSelector 对任意长正文和引用使用有界磁盘窗口；哈希只筛候选，命中仍逐字节核验。
func ResolveSelector(ctx context.Context, dir string, content contract.ContentSource, prefix, exact, suffix contract.ContentSource) (start, end int64, err error) {
	makeFile := func(source contract.ContentSource) (*resourcebudget.File, int64, error) {
		f, e := resourcebudget.TempFile(ctx, dir, "selector-", 256*resourcebudget.MiB)
		if e != nil {
			return nil, 0, e
		}
		r, e := source.Open(ctx)
		if e != nil {
			f.Close()
			return nil, 0, e
		}
		n, e := io.CopyBuffer(f, contextReader{ctx, r}, make([]byte, BufferBytes))
		r.Close()
		if e != nil {
			f.Close()
			return nil, 0, e
		}
		return f, n, nil
	}
	body, bodySize, err := makeFile(content)
	if err != nil {
		return 0, 0, err
	}
	defer func() { body.Close() }()
	p, _, err := makeFile(prefix)
	if err != nil {
		return 0, 0, err
	}
	defer func() { p.Close() }()
	e, _, err := makeFile(exact)
	if err != nil {
		return 0, 0, err
	}
	defer func() { e.Close() }()
	s, _, err := makeFile(suffix)
	if err != nil {
		return 0, 0, err
	}
	defer func() { s.Close() }()
	pi, _ := p.Stat()
	ei, _ := e.Stat()
	si, _ := s.Stat()
	plen, elen, slen := pi.Size(), ei.Size(), si.Size()
	size := plen + elen + slen
	if elen == 0 || size > bodySize {
		return 0, 0, ErrSelectorMismatch
	}
	pattern := func() io.Reader {
		return io.MultiReader(io.NewSectionReader(p, 0, plen), io.NewSectionReader(e, 0, elen), io.NewSectionReader(s, 0, slen))
	}
	const base uint64 = 16777619
	var wanted, power uint64
	power = 1
	b := bufio.NewReaderSize(pattern(), BufferBytes)
	for i := int64(0); i < size; i++ {
		v, er := b.ReadByte()
		if er != nil {
			return 0, 0, er
		}
		wanted = wanted*base + uint64(v) + 1
		if i < size-1 {
			power *= base
		}
	}
	incoming := bufio.NewReaderSize(io.NewSectionReader(body, 0, bodySize), BufferBytes)
	outgoing := bufio.NewReaderSize(io.NewSectionReader(body, 0, bodySize), BufferBytes)
	var hash uint64
	found := int64(-1)
	left, right := make([]byte, BufferBytes), make([]byte, BufferBytes)
	for pos := int64(0); pos < bodySize; pos++ {
		if pos%BufferBytes == 0 {
			if er := ctx.Err(); er != nil {
				return 0, 0, er
			}
		}
		v, er := incoming.ReadByte()
		if er != nil {
			return 0, 0, er
		}
		if pos >= size {
			old, er := outgoing.ReadByte()
			if er != nil {
				return 0, 0, er
			}
			hash -= (uint64(old) + 1) * power
		}
		hash = hash*base + uint64(v) + 1
		if pos+1 < size || hash != wanted {
			continue
		}
		offset := pos + 1 - size
		actual := io.NewSectionReader(body, offset, size)
		expected := pattern()
		equal := true
		for remain := size; remain > 0; {
			n := int(min(remain, int64(BufferBytes)))
			if _, er = io.ReadFull(actual, left[:n]); er != nil {
				return 0, 0, er
			}
			if _, er = io.ReadFull(expected, right[:n]); er != nil {
				return 0, 0, er
			}
			if !bytes.Equal(left[:n], right[:n]) {
				equal = false
				break
			}
			remain -= int64(n)
		}
		if equal {
			if found >= 0 {
				return 0, 0, ErrSelectorAmbiguous
			}
			found = offset + plen
		}
	}
	if found < 0 {
		return 0, 0, ErrSelectorMismatch
	}
	start, err = countRunes(io.NewSectionReader(body, 0, found))
	if err != nil {
		return 0, 0, err
	}
	length, err := countRunes(io.NewSectionReader(e, 0, elen))
	return start, start + length, err
}
