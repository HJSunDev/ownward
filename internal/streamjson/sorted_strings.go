package streamjson

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

// SortedStrings 保留说明依据原有的字符串排序身份；任意长条目和条目集合均落盘。
type SortedStrings struct {
	ctx                  context.Context
	data, order, scratch *resourcebudget.File
	count, size          int64
	left, right          []byte
}

func NewSortedStrings(ctx context.Context, dir string) (*SortedStrings, error) {
	s := &SortedStrings{ctx: ctx, left: make([]byte, BufferBytes), right: make([]byte, BufferBytes)}
	for _, f := range []**resourcebudget.File{&s.data, &s.order, &s.scratch} {
		v, err := resourcebudget.TempFile(ctx, dir, "basis-order-", 256*resourcebudget.MiB)
		if err != nil {
			s.Close()
			return nil, err
		}
		*f = v
	}
	return s, nil
}
func (s *SortedStrings) Close() error {
	var err error
	for _, f := range []*resourcebudget.File{s.data, s.order, s.scratch} {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
	}
	return err
}
func (s *SortedStrings) Add(r io.Reader) error {
	n, err := io.CopyBuffer(s.data, contextReader{s.ctx, r}, s.left)
	if err != nil {
		return err
	}
	var b [16]byte
	binary.LittleEndian.PutUint64(b[:8], uint64(s.size))
	binary.LittleEndian.PutUint64(b[8:], uint64(n))
	if _, err = s.order.WriteAt(b[:], s.count*16); err != nil {
		return err
	}
	s.count++
	s.size += n
	return nil
}

type stringPosition struct{ start, length int64 }

func position(f *resourcebudget.File, i int64) (stringPosition, error) {
	var b [16]byte
	_, err := f.ReadAt(b[:], i*16)
	return stringPosition{int64(binary.LittleEndian.Uint64(b[:8])), int64(binary.LittleEndian.Uint64(b[8:]))}, err
}
func (s *SortedStrings) less(a, b stringPosition) (bool, error) {
	n := min(a.length, b.length)
	for offset := int64(0); offset < n; {
		if err := s.ctx.Err(); err != nil {
			return false, err
		}
		size := min(int64(BufferBytes), n-offset)
		if _, err := s.data.ReadAt(s.left[:size], a.start+offset); err != nil {
			return false, err
		}
		if _, err := s.data.ReadAt(s.right[:size], b.start+offset); err != nil {
			return false, err
		}
		if v := bytes.Compare(s.left[:size], s.right[:size]); v != 0 {
			return v < 0, nil
		}
		offset += size
	}
	return a.length <= b.length, nil
}
func (s *SortedStrings) WriteJSON(w io.Writer) error {
	for width := int64(1); width < s.count; width *= 2 {
		for base := int64(0); base < s.count; base += 2 * width {
			i, j, end := base, min(base+width, s.count), min(base+2*width, s.count)
			middle := j
			for out := base; out < end; out++ {
				if err := s.ctx.Err(); err != nil {
					return err
				}
				var p stringPosition
				var err error
				if i >= middle {
					p, err = position(s.order, j)
					j++
				} else if j >= end {
					p, err = position(s.order, i)
					i++
				} else {
					a, e := position(s.order, i)
					if e != nil {
						return e
					}
					b, e := position(s.order, j)
					if e != nil {
						return e
					}
					left, e := s.less(a, b)
					if e != nil {
						return e
					}
					if left {
						p = a
						i++
					} else {
						p = b
						j++
					}
				}
				if err != nil {
					return err
				}
				var b [16]byte
				binary.LittleEndian.PutUint64(b[:8], uint64(p.start))
				binary.LittleEndian.PutUint64(b[8:], uint64(p.length))
				if _, err = s.scratch.WriteAt(b[:], out*16); err != nil {
					return err
				}
			}
		}
		s.order, s.scratch = s.scratch, s.order
	}
	if _, err := io.WriteString(w, "["); err != nil {
		return err
	}
	for i := int64(0); i < s.count; i++ {
		if i > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		p, err := position(s.order, i)
		if err != nil {
			return err
		}
		if err = WriteString(s.ctx, w, io.NewSectionReader(s.data, p.start, p.length)); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "]")
	return err
}
