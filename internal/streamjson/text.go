package streamjson

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Open 逐字符解码JSON字符串，跨缓冲边界保持与标准JSON解码一致。
func (n Node) Open(ctx context.Context) (io.ReadCloser, error) {
	if n.Kind != '"' {
		return nil, errors.New("正文必须为字符串")
	}
	r := bufio.NewReaderSize(n.Raw(), BufferBytes)
	if _, err := r.ReadByte(); err != nil {
		return nil, err
	}
	return &textReader{ctx: ctx, r: r}, nil
}

type textReader struct {
	ctx     context.Context
	r       *bufio.Reader
	pending []byte
	done    bool
}

func (r *textReader) Close() error { r.done = true; r.pending = nil; return nil }
func (r *textReader) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if r.done {
		return 0, io.EOF
	}
	n := 0
	for n < len(b) {
		if err := r.ctx.Err(); err != nil {
			return n, err
		}
		if len(r.pending) > 0 {
			count := copy(b[n:], r.pending)
			r.pending = r.pending[count:]
			n += count
			continue
		}
		v, _, err := r.r.ReadRune()
		if err != nil {
			return n, err
		}
		if v == '"' {
			r.done = true
			if n > 0 {
				return n, nil
			}
			return 0, io.EOF
		}
		if v == '\\' {
			escape, err := r.r.ReadByte()
			if err != nil {
				return n, err
			}
			switch escape {
			case '"', '\\', '/':
				v = rune(escape)
			case 'b':
				v = '\b'
			case 'f':
				v = '\f'
			case 'n':
				v = '\n'
			case 'r':
				v = '\r'
			case 't':
				v = '\t'
			case 'u':
				v, err = readHex(r.r)
				if err != nil {
					return n, err
				}
				if utf16.IsSurrogate(v) {
					peek, e := r.r.Peek(6)
					if e == nil && peek[0] == '\\' && peek[1] == 'u' {
						second, e := strconv.ParseUint(string(peek[2:]), 16, 16)
						if e == nil {
							decoded := utf16.DecodeRune(v, rune(second))
							if decoded != utf8.RuneError {
								r.r.Discard(6)
								v = decoded
							} else {
								v = utf8.RuneError
							}
						} else {
							v = utf8.RuneError
						}
					} else {
						v = utf8.RuneError
					}
				}
			default:
				return n, errors.New("无效字符串转义")
			}
		}
		var encoded [utf8.UTFMax]byte
		size := utf8.EncodeRune(encoded[:], v)
		r.pending = append(r.pending[:0], encoded[:size]...)
	}
	return n, nil
}

func readHex(r *bufio.Reader) (rune, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(string(b[:]), 16, 16)
	return rune(n), err
}

// WriteString 保留标准库的转义规则；结果与 json.Marshal(string) 相同。
func WriteString(ctx context.Context, w io.Writer, source io.Reader) error {
	b := bufio.NewReaderSize(source, BufferBytes)
	out := bufio.NewWriterSize(w, BufferBytes)
	if err := out.WriteByte('"'); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		v, _, err := b.ReadRune()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch v {
		case '"', '\\':
			err = out.WriteByte('\\')
			if err == nil {
				err = out.WriteByte(byte(v))
			}
		case '\n':
			_, err = out.WriteString("\\n")
		case '\r':
			_, err = out.WriteString("\\r")
		case '\t':
			_, err = out.WriteString("\\t")
		case '\b':
			_, err = out.WriteString("\\b")
		case '\f':
			_, err = out.WriteString("\\f")
		default:
			if v < 32 || v == '<' || v == '>' || v == '&' || v == 0x2028 || v == 0x2029 {
				const hex = "0123456789abcdef"
				var escaped = [6]byte{'\\', 'u', hex[(v>>12)&15], hex[(v>>8)&15], hex[(v>>4)&15], hex[v&15]}
				_, err = out.Write(escaped[:])
			} else {
				_, err = out.WriteRune(v)
			}
		}
		if err != nil {
			return err
		}
	}
	if err := out.WriteByte('"'); err != nil {
		return err
	}
	return out.Flush()
}

func (n Node) Canonical(ctx context.Context, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch n.Kind {
	case '"':
		r, err := n.Open(ctx)
		if err != nil {
			return err
		}
		defer r.Close()
		return WriteString(ctx, w, r)
	case '{':
		type field struct {
			name string
			node Node
		}
		fields := make([]field, 0, n.Count)
		c := n.Children()
		for {
			child, err := c.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			key, err := child.Key()
			if err != nil {
				return err
			}
			fields = append(fields, field{key, child})
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })
		if _, err := io.WriteString(w, "{"); err != nil {
			return err
		}
		for i, f := range fields {
			if i > 0 {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			key, _ := json.Marshal(f.name)
			if _, err := w.Write(key); err != nil {
				return err
			}
			if _, err := io.WriteString(w, ":"); err != nil {
				return err
			}
			if err := f.node.Canonical(ctx, w); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, "}")
		return err
	case '[':
		if _, err := io.WriteString(w, "["); err != nil {
			return err
		}
		c := n.Children()
		first := true
		for {
			child, err := c.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if !first {
				if _, err = io.WriteString(w, ","); err != nil {
					return err
				}
			}
			first = false
			if err = child.Canonical(ctx, w); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, "]")
		return err
	default:
		var value any
		if err := n.DecodeSmall(&value, 1024); err != nil {
			return err
		}
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
}
