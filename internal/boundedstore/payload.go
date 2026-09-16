package boundedstore

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

type Staged struct {
	ID            string
	Operation     string
	ContentBytes  int64
	ContentSHA256 string
}

func newID() (string, error) {
	var b [24]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}

// Stage 只持有一个正文块；完整发布前任何读取入口均不可见。
func (s *Store) Stage(ctx context.Context, operation string, content, details contract.ContentSource) (Staged, error) {
	if operation == "" || content == nil {
		return Staged{}, errors.New("暂存操作及正文不能为空")
	}
	release, err := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 3*ChunkBytes, false)
	if err != nil {
		return Staged{}, err
	}
	defer release()
	id, err := newID()
	if err != nil {
		return Staged{}, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO payloads(id,operation,state) VALUES(?,?,'staging')", id, operation)
		return e
	})
	if err != nil {
		return Staged{}, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = s.queueReclaim(context.WithoutCancel(ctx), id, "aborted")
		}
	}()
	n, hash, err := s.stagePart(ctx, id, 0, content, true)
	if err != nil {
		return Staged{}, err
	}
	if details == nil {
		details = StringSource("{}")
	}
	d, _, err := s.stagePart(ctx, id, 1, details, false)
	if err != nil {
		return Staged{}, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "UPDATE payloads SET state='ready',content_bytes=?,details_bytes=?,digest=? WHERE id=? AND state='staging'", n, d, hash, id)
		return e
	})
	if err != nil {
		return Staged{}, err
	}
	complete = true
	return Staged{ID: id, Operation: operation, ContentBytes: n, ContentSHA256: hash}, nil
}

func (s *Store) stagePart(ctx context.Context, id string, part int, source contract.ContentSource, text bool) (int64, string, error) {
	r, err := source.Open(ctx)
	if err != nil {
		return 0, "", err
	}
	defer r.Close()
	b := bufio.NewReaderSize(r, ChunkBytes)
	buffer := make([]byte, ChunkBytes)
	hash := sha256.New()
	var count int64
	ordinal := 0
	nonblank := false
	var tail []byte
	for {
		if err = ctx.Err(); err != nil {
			return 0, "", err
		}
		n, e := io.ReadFull(b, buffer)
		if e != nil && e != io.EOF && e != io.ErrUnexpectedEOF {
			return 0, "", e
		}
		if n == 0 {
			break
		}
		chunk := buffer[:n]
		if text {
			probe := append(tail, chunk...)
			pos := 0
			for pos < len(probe) {
				if !utf8.FullRune(probe[pos:]) {
					break
				}
				v, size := utf8.DecodeRune(probe[pos:])
				if v == utf8.RuneError && size == 1 {
					return 0, "", errors.New("正文包含无效UTF-8")
				}
				if !unicode.IsSpace(v) {
					nonblank = true
				}
				pos += size
			}
			tail = append([]byte(nil), probe[pos:]...)
		}
		if err = s.write(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, "INSERT INTO content_chunks(payload,part,ordinal,bytes) VALUES(?,?,?,?)", id, part, ordinal, chunk)
			return e
		}); err != nil {
			return 0, "", err
		}
		hash.Write(chunk)
		count += int64(n)
		ordinal++
		if e != nil {
			break
		}
	}
	if text && (len(tail) != 0 || !nonblank) {
		return 0, "", errors.New("正文为空白或UTF-8不完整")
	}
	return count, hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Store) queueReclaim(ctx context.Context, id, reason string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO reclaim_jobs(payload,reason) VALUES(?,?)", id, reason)
		return err
	})
}

type StringSource string

// Abandon 只登记未发布且属于本操作的暂存；已提交回执和资产不受影响。
func (s *Store) Abandon(ctx context.Context, p Staged) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO reclaim_jobs(payload,reason) SELECT id,'abandoned' FROM payloads WHERE id=? AND operation=? AND state<>'published'", p.ID, p.Operation)
		return err
	})
}

func (s StringSource) Open(ctx context.Context) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(string(s))), nil
}
