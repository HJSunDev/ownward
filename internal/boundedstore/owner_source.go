package boundedstore

import (
	"context"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func sourceNeedsEvidence(source domain.Source) bool {
	return source.Ref != "" || (source.Actor != "" && source.Actor != ownerSourceActor)
}

// Source is a small literal metadata projection, using the same deterministic
// branch as publication. Large unrelated details stay out of the browser.
func (s *Store) OwnerSource(ctx context.Context, id string, revision uint64) (out contract.OwnerSource, err error) {
	if _, err = s.OwnerCheckpoint(ctx); err != nil {
		return
	}
	row, err := s.OwnerAsset(ctx, id)
	if err != nil {
		return out, err
	}
	if row.Meta.Revision != revision {
		return out, ErrDraftConflict
	}
	r, err := s.openOwnerAssetPart(ctx, id, revision, 1)
	if err != nil {
		return out, err
	}
	defer r.Close()
	doc, err := streamjson.Parse(ctx, s.directory, r, resourcebudget.FromContext(ctx, s.budget), 256*resourcebudget.MiB)
	if err != nil {
		return out, err
	}
	defer doc.Close()
	n, ok, err := doc.Root().Field("source")
	if err != nil {
		return out, err
	}
	var source domain.Source
	if ok {
		if err = n.DecodeSmall(&source, 256*1024); err != nil {
			return out, err
		}
	}
	out = contract.OwnerSource{Actor: source.Actor, Ref: source.Ref, Authored: source.Actor == ownerSourceActor, PreserveOriginal: row.Original || sourceNeedsEvidence(source)}
	return out, nil
}
