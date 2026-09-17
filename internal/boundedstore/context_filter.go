package boundedstore

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func foldDigest(r io.Reader) ([32]byte, error) {
	h := sha256.New()
	var runeBytes [4]byte
	b := bufio.NewReaderSize(r, 65536)
	for {
		v, _, err := b.ReadRune()
		if err == io.EOF {
			var d [32]byte
			copy(d[:], h.Sum(nil))
			return d, nil
		}
		if err != nil {
			return [32]byte{}, err
		}
		minimum := v
		for next := unicode.SimpleFold(v); next != v; next = unicode.SimpleFold(next) {
			if next < minimum {
				minimum = next
			}
		}
		h.Write(utf8.AppendRune(runeBytes[:0], minimum))
	}
}
func foldNode(ctx context.Context, n streamjson.Node) ([32]byte, error) {
	r, err := n.Open(ctx)
	if err != nil {
		return [32]byte{}, err
	}
	defer r.Close()
	return foldDigest(r)
}

// Table and key names are internal constants, never request data.
func matchStoredContexts(ctx context.Context, q queryer, table, key, id string, contexts []domain.Context) (bool, error) {
	return visitRequiredContexts(ctx, contexts, func(kd, vd [32]byte) (bool, error) {
		var declared, compatible int
		err := q.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(value=?),0) FROM "+table+" WHERE "+key+"=? AND key=?", vd[:], id, kd[:]).Scan(&declared, &compatible)
		if err != nil {
			return false, err
		}
		if declared > 0 && compatible == 0 {
			return false, nil
		}
		return true, nil
	})
}

type requiredContextsKey struct{}

// WithContextFilter keeps request context values in their streamed JSON document.
func WithContextFilter(ctx context.Context, n streamjson.Node) context.Context {
	return context.WithValue(ctx, requiredContextsKey{}, n)
}
func visitRequiredContexts(ctx context.Context, values []domain.Context, visit func([32]byte, [32]byte) (bool, error)) (bool, error) {
	if n, ok := ctx.Value(requiredContextsKey{}).(streamjson.Node); ok {
		if n.Kind == 'n' {
			return true, nil
		}
		if n.Kind != '[' {
			return false, errors.New("上下文必须为数组")
		}
		it := n.Children()
		for {
			item, e := it.Next()
			if e == io.EOF {
				return true, nil
			}
			if e != nil {
				return false, e
			}
			var digests [2][32]byte
			for i, field := range []string{"key", "value"} {
				v, ok, e := item.Field(field)
				if e != nil {
					return false, e
				}
				if !ok {
					digests[i], _ = foldDigest(strings.NewReader(""))
					continue
				}
				text, e := v.Trimmed(ctx)
				if e != nil {
					return false, e
				}
				r, e := text.Open(ctx)
				if e != nil {
					return false, e
				}
				digests[i], e = foldDigest(r)
				r.Close()
				if e != nil {
					return false, e
				}
			}
			ok, e := visit(digests[0], digests[1])
			if e != nil || !ok {
				return ok, e
			}
		}
	}
	for _, v := range values {
		kd, _ := foldDigest(strings.NewReader(v.Key))
		vd, _ := foldDigest(strings.NewReader(v.Value))
		ok, e := visit(kd, vd)
		if e != nil || !ok {
			return ok, e
		}
	}
	return true, nil
}

func lowerDigest(r io.Reader) ([32]byte, error) {
	h := sha256.New()
	var runeBytes [4]byte
	b := bufio.NewReaderSize(r, ChunkBytes)
	var result [32]byte
	for {
		v, _, e := b.ReadRune()
		if e == io.EOF {
			copy(result[:], h.Sum(nil))
			return result, nil
		}
		if e != nil {
			return result, e
		}
		h.Write(utf8.AppendRune(runeBytes[:0], unicode.ToLower(v)))
	}
}
func lowerNode(ctx context.Context, n streamjson.Node) ([32]byte, error) {
	r, e := n.Open(ctx)
	if e != nil {
		return [32]byte{}, e
	}
	defer r.Close()
	return lowerDigest(r)
}
func (s *Store) InferredContextAllowed(ctx context.Context, id, key, value string) (bool, error) {
	var count, matches int
	kd, _ := lowerDigest(strings.NewReader(key))
	vd, _ := lowerDigest(strings.NewReader(value))
	e := s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(c.lower_value=?),0) FROM lexical_contexts c JOIN live_assets a ON a.payload=c.payload WHERE a.id=? AND c.lower_key=?", vd[:], id, kd[:]).Scan(&count, &matches)
	})
	return count == 0 || matches > 0, e
}
