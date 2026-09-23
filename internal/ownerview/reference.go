package ownerview

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
)

// A reference is only a locator. It deliberately carries no revision or
// permission. Resolving it requires the current owner, on the same authority;
// writes still require a freshly issued, revision-bound encrypted handle.
type reference struct{ System, Kind, ID string }

func version(cp boundedstore.OwnerCheckpoint, kind, id string, revision uint64) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s/%d", cp.System, kind, id, revision))))
}

func locator(cp boundedstore.OwnerCheckpoint, kind, id string) string {
	b, _ := json.Marshal(reference{cp.System, kind, id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Service) asset(cp boundedstore.OwnerCheckpoint, v boundedstore.OwnerAssetRow) contract.OwnerAsset {
	kind := "asset"
	if v.State == "stopped" {
		kind = "stopped"
	}
	return contract.OwnerAsset{Version: version(cp, "asset", v.Meta.ID, v.Meta.Revision), Reference: locator(cp, "asset", v.Meta.ID), Handle: s.object(cp, kind, v.Meta.ID, v.Meta.Revision), Kind: v.Meta.Kind, State: v.State, CreatedAt: v.Meta.CreatedAt, UpdatedAt: v.Meta.UpdatedAt, Bytes: v.Meta.ContentBytes, HasOriginal: v.Original}
}

func (s *Service) draft(cp boundedstore.OwnerCheckpoint, d contract.Draft) contract.OwnerDraft {
	v := contract.OwnerDraft{Version: version(cp, "draft", d.ID, d.Revision), Reference: locator(cp, "draft", d.ID), Handle: s.object(cp, "draft", d.ID, d.Revision), UpdatedAt: d.UpdatedAt, Bytes: d.ContentBytes}
	if d.Target.ID != "" {
		v.Target = s.object(cp, "asset", d.Target.ID, d.Target.Revision)
		v.TargetReference = locator(cp, "asset", d.Target.ID)
	}
	return v
}

func (s *Service) locate(ctx context.Context, cp boundedstore.OwnerCheckpoint, q contract.OwnerQuery, out *contract.OwnerPage) error {
	var ref reference
	if q.Reference != "" {
		if len(q.Reference) > 8192 {
			return contract.ErrOwnerRefresh
		}
		b, e := base64.RawURLEncoding.DecodeString(q.Reference)
		if e != nil || json.Unmarshal(b, &ref) != nil {
			return contract.ErrOwnerRefresh
		}
	} else {
		h, e := s.open(q.Handle)
		if e != nil || h.OwnerRevision != cp.OwnerRevision {
			return contract.ErrOwnerRefresh
		}
		ref = reference{h.System, h.Type, h.ID}
	}
	if ref.System != cp.System || ref.ID == "" {
		return contract.ErrOwnerRefresh
	}
	var err error
	switch ref.Kind {
	case "draft":
		var d contract.Draft
		d, err = s.Store.DraftMetadata(ctx, ref.ID, "")
		if err == nil {
			out.Drafts = []contract.OwnerDraft{s.draft(cp, d)}
		}
	case "asset":
		var v boundedstore.OwnerAssetRow
		v, err = s.Store.OwnerAsset(ctx, ref.ID)
		if err == nil {
			out.Assets = []contract.OwnerAsset{s.asset(cp, v)}
		}
	default:
		return contract.ErrOwnerRefresh
	}
	if errors.Is(err, boundedstore.ErrNotFound) {
		out.Unavailable = true
		return nil
	}
	return err
}
