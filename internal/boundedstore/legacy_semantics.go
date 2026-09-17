package boundedstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

// Older formats duplicated original text in work records. Only durable work
// references and the accepted-result receipt survive the format conversion.
func (s *Store) legacySemanticReferences(ctx context.Context, n streamjson.Node) (*semantics.WorkReference, *semantics.SubmissionReceipt, error) {
	var work *semantics.WorkReference
	var receipt *semantics.SubmissionReceipt
	w, has, e := n.Field("semantic_work")
	if e != nil {
		return nil, nil, e
	}
	if has && w.Kind == '{' {
		work = &semantics.WorkReference{Schema: semantics.WorkReferenceSchema}
		for _, f := range []struct {
			k string
			v any
		}{{"id", &work.ID}, {"generation", &work.Generation}, {"created_at", &work.CreatedAt}, {"target_snapshot", &work.TargetSnapshot}, {"previous_analysis", &work.Previous}} {
			if e = nodeDecode(w, f.k, f.v); e != nil {
				return nil, nil, e
			}
		}
		asset, _, e := w.Field("asset")
		if e != nil {
			return nil, nil, e
		}
		if e = nodeDecode(asset, "id", &work.AssetID); e != nil {
			return nil, nil, e
		}
		if e = nodeDecode(asset, "revision", &work.Revision); e != nil {
			return nil, nil, e
		}
		if e = visitArray(w, "candidates", func(c streamjson.Node) error {
			var ref semantics.CandidateReference
			for _, f := range []struct {
				k string
				v any
			}{{"id", &ref.ID}, {"revision", &ref.Revision}, {"semantic_similarity", &ref.Similarity}} {
				if e := nodeDecode(c, f.k, f.v); e != nil {
					return e
				}
			}
			org, ok, e := c.Field("organization")
			if e != nil {
				return e
			}
			if ok && org.Kind == '{' {
				if e = nodeDecode(org, "snapshot", &ref.OrganizationSnapshot); e != nil {
					return e
				}
			}
			work.Candidates = append(work.Candidates, ref)
			return work.Validate()
		}); e != nil {
			return nil, nil, e
		}
		if e = work.Validate(); e != nil {
			return nil, nil, e
		}
	}
	v, has, e := n.Field("semantic_result")
	if e != nil {
		return nil, nil, e
	}
	if has && v.Kind == '{' {
		var sub semantics.Submission
		for _, f := range []struct {
			k string
			v any
		}{{"schema", &sub.Schema}, {"work_id", &sub.WorkID}, {"asset_id", &sub.AssetID}, {"asset_revision", &sub.Revision}, {"capability", &sub.Capability}, {"status", &sub.Status}, {"uncertainty", &sub.Uncertainty}, {"accepted_at", &sub.AcceptedAt}, {"input_assets", &sub.InputAssets}} {
			if e = nodeDecode(v, f.k, f.v); e != nil {
				return nil, nil, e
			}
		}
		r, e := semantics.NewSubmissionReceipt(sub)
		if e != nil {
			return nil, nil, e
		}
		sub.AcceptedAt = time.Time{}
		sub.Capability.Execution = ""
		encoded, e := json.Marshal(sub)
		if e != nil {
			return nil, nil, e
		}
		d, e := streamjson.Parse(ctx, s.directory, bytes.NewReader(encoded), s.budget, 256*resourcebudget.MiB)
		if e != nil {
			return nil, nil, e
		}
		analysis, _, e := v.Field("analysis")
		if e != nil {
			d.Close()
			return nil, nil, e
		}
		h := sha256.New()
		e = d.Root().Object(h, map[string]func(io.Writer) error{"analysis": func(w io.Writer) error { return compactJSONTo(w, analysis.Raw()) }})
		d.Close()
		if e != nil {
			return nil, nil, e
		}
		r.SHA256 = hex.EncodeToString(h.Sum(nil))
		receipt = &r
	}
	return work, receipt, nil
}
