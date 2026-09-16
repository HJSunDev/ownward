package embedding

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// EmbedQuerySource preserves the configured prefix, model and normalization.
// Only the transport changes: the request string is not copied into one buffer.
func (m *Managed) EmbedQueryReader(ctx context.Context, r io.Reader) ([]float32, error) {
	m.requestMu.Lock()
	defer m.requestMu.Unlock()
	if e := m.ensureRunning(ctx); e != nil {
		return nil, e
	}
	input, output := io.Pipe()
	defer input.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, e := io.WriteString(output, `{"input":[`)
		if e == nil {
			e = writeQueryString(ctx, output, io.MultiReader(strings.NewReader(m.bundle.Manifest.Space.QueryPrefix), r))
		}
		if e == nil {
			_, e = io.WriteString(output, `],"model":"embeddinggemma"}`)
		}
		output.CloseWithError(e)
	}()
	vectors, e := m.embedRequest(ctx, input, 1)
	input.Close()
	<-done
	if e != nil {
		return nil, e
	}
	if len(vectors) != 1 {
		return nil, errors.New("本地向量运行时返回数量无效")
	}
	return vectors[0], nil
}

func writeQueryString(ctx context.Context, w io.Writer, r io.Reader) error {
	if _, e := io.WriteString(w, `"`); e != nil {
		return e
	}
	b := bufio.NewReaderSize(r, 32768)
	for {
		chunk := make([]rune, 0, 8192)
		done := false
		for len(chunk) < cap(chunk) {
			if e := ctx.Err(); e != nil {
				return e
			}
			v, _, e := b.ReadRune()
			if e == io.EOF {
				done = true
				break
			}
			if e != nil {
				return e
			}
			chunk = append(chunk, v)
		}
		encoded, e := json.Marshal(string(chunk))
		if e != nil {
			return e
		}
		if _, e = w.Write(encoded[1 : len(encoded)-1]); e != nil {
			return e
		}
		if done {
			break
		}
	}
	_, e := io.WriteString(w, `"`)
	return e
}
