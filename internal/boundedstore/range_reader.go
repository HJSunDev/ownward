package boundedstore

import (
	"context"
	"errors"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"io"
)

func (s *Store) OpenRange(ctx context.Context, id string, revision uint64, start, end int64) (io.ReadCloser, error) {
	release, e := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 2*ChunkBytes, false)
	if e != nil {
		return nil, e
	}
	q, done, e := s.snapshotReader(ctx)
	if e != nil {
		release()
		return nil, e
	}
	finish := func() { done(); release() }
	var payload string
	var length int64
	e = q.QueryRowContext(ctx, "SELECT a.payload,p.content_bytes FROM assets a JOIN payloads p ON p.id=a.payload WHERE a.id=? AND a.deleted=0 AND a.revision=?", id, revision).Scan(&payload, &length)
	if e != nil {
		finish()
		return nil, e
	}
	if start < 0 || end < start || end > length {
		finish()
		return nil, errors.New("原文区间无效")
	}
	rows, e := q.QueryContext(ctx, `SELECT substr(bytes,max(0,?-ordinal*65536)+1,min(length(bytes),?-ordinal*65536)-max(0,?-ordinal*65536)) FROM content_chunks WHERE payload=? AND part=0 AND ordinal>=? AND ordinal<=? AND ?<? ORDER BY ordinal`, start, end, start, payload, start/ChunkBytes, max(0, end-1)/ChunkBytes, start, end)
	if e != nil {
		finish()
		return nil, e
	}
	return &chunkReader{ctx: ctx, rows: rows, release: finish}, nil
}
