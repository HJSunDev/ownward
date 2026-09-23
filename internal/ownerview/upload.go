package ownerview

import (
	"context"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
)

// ReplaceDraft streams one complete conditional write through the existing
// staging transaction. It creates no upload session or second authority.
func (s *Service) ReplaceDraft(ctx context.Context, token string, source io.Reader) (out contract.OwnerResult, err error) {
	defer func() {
		if errors.Is(err, boundedstore.ErrDraftConflict) || errors.Is(err, boundedstore.ErrNotFound) || errors.Is(err, boundedstore.ErrSnapshotInterrupted) {
			err = contract.ErrOwnerRefresh
		}
	}()
	cp, e := s.Store.OwnerCheckpoint(ctx)
	if e != nil {
		return out, e
	}
	h, e := s.resolve(cp, token, "draft")
	if e != nil {
		return out, e
	}
	d, e := s.Store.WriteOwnerDraftStream(ctx, h.ID, h.Revision, source)
	if e != nil {
		return out, e
	}
	return contract.OwnerResult{Schema: contract.OwnerViewSchema, Handle: s.object(cp, "draft", d.ID, d.Revision), Reference: locator(cp, "draft", d.ID), State: "completed"}, nil
}
