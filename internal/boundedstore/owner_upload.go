package boundedstore

import (
	"context"
	"io"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

// The request spool is bounded scratch, removed on success, error, or startup
// recovery. It supplies the repeatable ContentSource required by WriteDraft.
func (s *Store) WriteOwnerDraftStream(ctx context.Context, id string, revision uint64, body io.Reader) (contract.Draft, error) {
	if _, e := s.OwnerCheckpoint(ctx); e != nil {
		return contract.Draft{}, e
	}
	d, e := s.DraftMetadata(ctx, id, "")
	if e != nil {
		return d, e
	}
	if d.Revision != revision {
		return d, ErrDraftConflict
	}
	f, e := resourcebudget.TempFile(ctx, s.directory, "owner-input-", contract.OwnerDraftUploadBytes)
	if e != nil {
		return d, e
	}
	defer f.Close()
	n, e := io.CopyBuffer(f, body, make([]byte, ChunkBytes))
	if e != nil {
		return d, e
	}
	if e = ctx.Err(); e != nil {
		return d, e
	}
	return s.WriteDraft(ctx, contract.DraftWrite{ID: id, ExpectedRevision: revision, Content: textFile{f, n}})
}
