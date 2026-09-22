package boundedstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

const ownerSourceActor = "ownward:owner"

func (s *Store) PublishDraft(ctx context.Context, id string, revision uint64, op contract.OperationIdentity) (out contract.AssetVersion, err error) {
	var actor ownerActor
	automaticGeneration := op.Generation == 0
	e := s.view(ctx, func(q queryer) error {
		var e error
		actor, e = requireOwner(ctx, q, true)
		if e != nil {
			return e
		}
		if op.Generation == 0 {
			e = q.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='operation_generation'").Scan(&op.Generation)
		}
		return e
	})
	if e != nil {
		return out, e
	}
	op.System, op.Principal, op.Kind = actor.system, actor.id, "ownward_publish_draft"
	b, _ := json.Marshal(struct {
		ID       string
		Revision uint64
	}{id, revision})
	digest := sha256.Sum256(b)
	op.Digest = hex.EncodeToString(digest[:])
	// Concurrent retries may finish while this call is streaming a now-removed
	// draft. Always return the committed receipt, never an uncommitted local ID.
	defer func() {
		v, found, e := s.draftReceipt(ctx, op)
		if e != nil {
			err = e
			return
		}
		if found {
			out = v
			err = nil
		}
	}()
	previous, found, e := s.draftReceipt(ctx, op)
	if e != nil {
		return out, e
	}
	if found {
		return previous, nil
	}
	d, r, e := s.ReadDraft(ctx, id, "")
	if e != nil {
		return out, e
	}
	r.Close()
	if d.Revision != revision {
		return out, ErrDraftConflict
	}
	if automaticGeneration {
		op.Generation, e = s.OperationGeneration(ctx)
		if e != nil {
			return out, e
		}
	}
	ctx, release, e := s.ownerWorkspace(ctx)
	if e != nil {
		return out, e
	}
	defer release()
	var payload string
	e = s.view(ctx, func(q queryer) error { _, p, e := loadDraft(ctx, q, id); payload = p; return e })
	if e != nil {
		return out, e
	}
	part := func(n int) contract.ContentSource {
		return contentFunc(func(c context.Context) (io.ReadCloser, error) {
			return s.openDraftPart(c, id, "", payload, revision, n)
		})
	}
	r, e = part(1).Open(ctx)
	if e != nil {
		return out, e
	}
	details, e := streamjson.Parse(ctx, s.directory, r, resourcebudget.FromContext(ctx, s.budget), 256*resourcebudget.MiB)
	r.Close()
	if e != nil {
		return out, e
	}
	defer details.Close()
	var source domain.Source
	n, ok, e := details.Root().Field("source")
	if e != nil {
		return out, e
	}
	if ok {
		if e = n.DecodeSmall(&source, 256*1024); e != nil {
			return out, e
		}
	}
	keepOriginal := d.Target.ID != "" && (source.Ref != "" || (source.Actor != "" && source.Actor != ownerSourceActor))
	if d.Target.ID != "" && !keepOriginal {
		e = s.view(ctx, func(q queryer) error {
			return q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM asset_originals WHERE asset=?)", d.Target.ID).Scan(&keepOriginal)
		})
		if e != nil {
			return out, e
		}
	}
	// Metadata identifies the author of the current text. Source evidence is
	// retained separately, so a human revision cannot masquerade as a quotation.
	links, e := resourcebudget.TempFile(ctx, s.directory, "owner-links-", 256*resourcebudget.MiB)
	if e != nil {
		return out, e
	}
	defer links.Close()
	encoder := json.NewEncoder(links)
	var changes contract.RelationInvalidationCounts
	prepared, e := streamjson.Build(ctx, s.directory, resourcebudget.FromContext(ctx, s.budget), 256*resourcebudget.MiB, func(w io.Writer) error {
		if _, e := io.WriteString(w, `{"source":{"actor":"`+ownerSourceActor+`"}`); e != nil {
			return e
		}
		for _, key := range []string{"contexts"} {
			n, ok, e := details.Root().Field(key)
			if e != nil {
				return e
			}
			if !ok {
				continue
			}
			if _, e = io.WriteString(w, `,"`+key+`":`); e != nil {
				return e
			}
			if e = n.Copy(w); e != nil {
				return e
			}
		}
		if _, e := io.WriteString(w, `,"explicit_relations":[`); e != nil {
			return e
		}
		e := s.visitOwnerLinks(ctx, d.Target.ID, details.Root(), part(0), &changes, func(n streamjson.Node, link ExplicitLink) error {
			if link.Ordinal > 0 {
				if _, e := io.WriteString(w, ","); e != nil {
					return e
				}
			}
			if e := n.Copy(w); e != nil {
				return e
			}
			return encoder.Encode(link)
		})
		if e != nil {
			return e
		}
		if _, e = io.WriteString(w, "]"); e != nil {
			return e
		}
		_, e = io.WriteString(w, "}")
		return e
	})
	if e != nil {
		return out, e
	}
	defer prepared.Close()
	p, e := s.Stage(ctx, operationKey(op), part(0), streamjson.RawSource{Node: prepared.Root()})
	if e != nil {
		return out, e
	}
	defer s.Abandon(context.WithoutCancel(ctx), p)
	if _, e = links.Seek(0, io.SeekStart); e != nil {
		return out, e
	}
	decoder := json.NewDecoder(links)
	for {
		var link ExplicitLink
		e = decoder.Decode(&link)
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return out, e
		}
		if e = s.StageLink(ctx, p, link); e != nil {
			return out, e
		}
	}
	now := time.Now().UTC()
	m := contract.AssetMeta{ID: d.Target.ID, Revision: d.Target.Revision + 1, Kind: d.Kind, CreatedAt: now, UpdatedAt: now}
	if d.Target.ID == "" {
		m.ID, e = newID()
	} else {
		var current contract.AssetMeta
		current, e = s.ReadAssetMeta(ctx, d.Target.ID, d.Target.Revision)
		m.CreatedAt = current.CreatedAt
	}
	if e != nil {
		return out, e
	}
	v := AssetWrite{Meta: m, Payload: p, ExpectedRevision: d.Target.Revision, DeferOrganization: true, KeepOriginal: keepOriginal}
	out = contract.AssetVersion{ID: m.ID, Revision: m.Revision}
	receipt := contract.MutationReceipt{Operation: op, Results: []contract.MutationOutcome{{Asset: out}}}
	before := func(tx *sql.Tx) error {
		if _, e := requireOwner(ctx, tx, true); e != nil {
			return e
		}
		current, currentPayload, e := loadDraft(ctx, tx, id)
		if e != nil {
			return e
		}
		if current.Revision != revision || currentPayload != payload {
			return ErrDraftConflict
		}
		return nil
	}
	after := func(tx *sql.Tx) error {
		if e := deleteDraft(ctx, tx, id, payload); e != nil {
			return e
		}
		return recordOwnerEventWithChanges(ctx, tx, "draft_published", out.ID, out.Revision, op.ID, "completed", changes)
	}
	e = s.publish(ctx, receipt, []AssetWrite{v}, before, after)
	return out, e
}

func (s *Store) draftReceipt(ctx context.Context, op contract.OperationIdentity) (out contract.AssetVersion, found bool, err error) {
	err = s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, true); e != nil {
			return e
		}
		r, ok, e := lookupReceipt(ctx, q, op)
		if e != nil || !ok {
			return e
		}
		if len(r.Results) != 1 {
			return errors.New("文稿发布回执无效")
		}
		out = r.Results[0].Asset
		var live bool
		if e = q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM live_assets WHERE id=?)", out.ID).Scan(&live); e != nil {
			return e
		}
		if !live {
			return ErrNotFound
		}
		found = true
		return nil
	})
	return
}

// OpenOriginal returns the separately retained evidence, not an arbitrary old
// edit version. It shares live-asset visibility, authorization and forget state.
func (s *Store) OpenOriginal(ctx context.Context, id string, details bool) (uint64, io.ReadCloser, error) {
	if e := ownerStreamContext(ctx); e != nil {
		return 0, nil, e
	}
	ctx, e := s.BeginAccess(ctx, contract.AuthenticationDigest(ctx), contract.ReadPermission)
	if e != nil {
		return 0, nil, e
	}
	var revision uint64
	var payload string
	resolve := func(q queryer) (string, error) {
		var rev uint64
		var p string
		e := q.QueryRowContext(ctx, "SELECT o.revision,o.payload FROM asset_originals o JOIN live_assets a ON a.id=o.asset WHERE o.asset=?", id).Scan(&rev, &p)
		if errors.Is(e, sql.ErrNoRows) {
			e = ErrNotFound
		}
		if e != nil {
			return "", e
		}
		if revision != 0 && (rev != revision || p != payload) {
			return "", ErrDraftConflict
		}
		revision, payload = rev, p
		return p, nil
	}
	e = s.view(ctx, func(q queryer) error { _, e := resolve(q); return e })
	if e != nil {
		return 0, nil, e
	}
	part := 0
	if details {
		part = 1
	}
	r, e := s.openOwnerPart(ctx, part, resolve)
	return revision, r, e
}

// A text edit invalidates inherited addresses, not the user's ability to save.
// Keep only uniquely resolvable, live references in both metadata and indexes;
// never guess a new span or broaden a lost quotation to the entire document.
func (s *Store) visitOwnerLinks(ctx context.Context, id string, details streamjson.Node, body contract.ContentSource, changes *contract.RelationInvalidationCounts, visit func(streamjson.Node, ExplicitLink) error) error {
	ordinal := int64(0)
	return visitArray(details, "explicit_relations", func(n streamjson.Node) error {
		target, e := nodeString(n, "target_id")
		if e != nil {
			return e
		}
		if target != id {
			if _, e = s.ReadAssetMeta(ctx, target, 0); errors.Is(e, ErrNotFound) {
				changes.TargetUnavailable++
				return nil
			} else if e != nil {
				return e
			}
		}
		kind, e := nodeString(n, "type")
		if e != nil {
			return e
		}
		link := ExplicitLink{Ordinal: ordinal, Target: target, Qualifies: kind == "qualifies"}
		selector, ok, e := n.Field("selector")
		if e != nil {
			return e
		}
		if ok && selector.Kind != 'n' {
			part := func(key string) (contract.ContentSource, error) {
				v, ok, e := selector.Field(key)
				if e != nil {
					return nil, e
				}
				if !ok {
					return StringSource(""), nil
				}
				return v, nil
			}
			prefix, e := part("prefix")
			if e != nil {
				return e
			}
			exact, e := part("exact")
			if e != nil {
				return e
			}
			suffix, e := part("suffix")
			if e != nil {
				return e
			}
			link.StartRune, link.EndRune, e = streamjson.ResolveSelector(ctx, s.directory, body, prefix, exact, suffix)
			if errors.Is(e, streamjson.ErrSelectorMismatch) {
				changes.QuoteMissing++
				return nil
			}
			if errors.Is(e, streamjson.ErrSelectorAmbiguous) {
				changes.QuoteAmbiguous++
				return nil
			}
			if e != nil {
				return e
			}
		}
		ordinal++
		return visit(n, link)
	})
}
