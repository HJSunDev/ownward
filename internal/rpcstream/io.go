package rpcstream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// IO 沿用SDK会话、取消与消息分派，只替换进入和离开SDK的字节流。
type IO struct {
	Scope  *Scope
	Reader io.ReadCloser
	Writer io.WriteCloser
	// 共享Scope由调用者在其他使用者停止后关闭；默认仍随IO关闭。
	SharedScope bool
}

func (t *IO) Connect(ctx context.Context) (mcp.Connection, error) {
	ctx, cancel := context.WithCancel(ctx)
	read, feed := io.Pipe()
	received, write := io.Pipe()
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			cancel()
			t.Reader.Close()
			t.Writer.Close()
			read.Close()
			feed.Close()
			received.Close()
			write.Close()
			if !t.SharedScope {
				t.Scope.Close()
			}
		})
	}
	go func() { <-ctx.Done(); closeAll() }()
	go func() {
		defer feed.Close()
		reader := bufio.NewReaderSize(t.Reader, EnvelopeBytes)
		for {
			if _, err := reader.Peek(1); err != nil {
				feed.CloseWithError(err)
				return
			}
			data, _, err := t.Scope.Project(ctx, &lineReader{r: reader})
			if err != nil {
				feed.CloseWithError(err)
				return
			}
			if _, err = feed.Write(append(data, '\n')); err != nil {
				return
			}
		}
	}()
	go func() {
		defer received.Close()
		reader := bufio.NewReaderSize(received, EnvelopeBytes+1)
		for {
			line, err := reader.ReadSlice('\n')
			if err != nil {
				closeAll()
				return
			}
			if len(line) > EnvelopeBytes {
				closeAll()
				return
			}
			var message struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(line, &message) != nil {
				closeAll()
				return
			}
			var call *Call
			if message.Method == "" {
				t.Scope.mu.Lock()
				for _, c := range t.Scope.calls {
					if sameRPCIdentity(c.ID, message.ID) {
						call = c
						break
					}
				}
				t.Scope.mu.Unlock()
			}
			if call != nil {
				err = call.Expand(ctx, t.Writer, line)
				call.Close()
			} else {
				_, err = t.Writer.Write(line)
			}
			if err == nil && call != nil {
				_, err = t.Writer.Write([]byte{'\n'})
			}
			if err != nil {
				closeAll()
				return
			}
		}
	}()
	connection, err := (&mcp.IOTransport{Reader: closeReader{read, closeAll}, Writer: closeWriter{write, closeAll}}).Connect(WithScope(ctx, t.Scope))
	if err != nil {
		closeAll()
	}
	return connection, err
}

type closeReader struct {
	io.Reader
	close func()
}

func (r closeReader) Close() error { r.close(); return nil }

type closeWriter struct {
	io.Writer
	close func()
}

func (w closeWriter) Close() error { w.close(); return nil }

type lineReader struct {
	r       *bufio.Reader
	pending []byte
	done    bool
}

func (r *lineReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		if r.done {
			return 0, io.EOF
		}
		b, err := r.r.ReadSlice('\n')
		r.pending = b
		if err == nil {
			r.done = true
			r.pending = b[:len(b)-1]
		} else if errors.Is(err, io.EOF) {
			r.done = true
		} else if err != bufio.ErrBufferFull {
			return 0, err
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	if n == 0 && r.done {
		return 0, io.EOF
	}
	return n, nil
}
