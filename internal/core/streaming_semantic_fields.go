package core

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func semanticText(ctx context.Context, n streamjson.Node, key string, limit int) (string, error) {
	v, ok, e := n.Field(key)
	if e != nil || !ok || v.Kind == 'n' {
		return "", e
	}
	text, e := v.Trimmed(ctx)
	if e != nil {
		return "", e
	}
	r, e := text.Open(ctx)
	if e != nil {
		return "", e
	}
	defer r.Close()
	b := bufio.NewReaderSize(r, 4096)
	out := make([]rune, 0, limit+1)
	for len(out) <= limit {
		v, _, e := b.ReadRune()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
		out = append(out, v)
	}
	return string(out), nil
}
func semanticOptional(n streamjson.Node, key string, out any, max int64) error {
	v, ok, e := n.Field(key)
	if e != nil || !ok {
		return e
	}
	return v.DecodeSmall(out, max)
}
func semanticArray(n streamjson.Node, key string, visit func(streamjson.Node) (bool, error)) error {
	v, ok, e := n.Field(key)
	if e != nil || !ok || v.Kind == 'n' {
		return e
	}
	if v.Kind != '[' {
		return errors.New("语义数组格式无效")
	}
	c := v.Children()
	for {
		n, e := c.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		stop, e := visit(n)
		if e != nil || stop {
			return e
		}
	}
}

func (s *StreamingAssets) readSemanticHeader(ctx context.Context, node streamjson.Node, asset domain.Information) (semantics.Submission, streamjson.Node, error) {
	var sub semantics.Submission
	var zero streamjson.Node
	for _, f := range []struct {
		k string
		v any
	}{{"schema", &sub.Schema}, {"work_id", &sub.WorkID}, {"asset_id", &sub.AssetID}, {"asset_revision", &sub.Revision}, {"capability", &sub.Capability}, {"status", &sub.Status}} {
		if e := semanticOptional(node, f.k, f.v, 64*1024); e != nil {
			return sub, zero, e
		}
	}
	var e error
	sub.Uncertainty, e = semanticText(ctx, node, "uncertainty", 512)
	if e != nil {
		return sub, zero, e
	}
	if e = semanticArray(node, "input_assets", func(n streamjson.Node) (bool, error) {
		if len(sub.InputAssets) == 660 {
			return false, errors.New("语义调用的输入引用超过有界批量范围")
		}
		var ref semantics.CandidateReference
		if e := n.DecodeSmall(&ref, 4096); e != nil {
			return false, e
		}
		// Asset identities and generated snapshots are bounded independently of
		// source text; reject impossible references before accumulating them.
		if len(ref.ID) > 256 || len(ref.OrganizationSnapshot) > 68 {
			return false, errors.New("语义输入引用身份无效")
		}
		sub.InputAssets = append(sub.InputAssets, ref)
		return false, nil
	}); e != nil {
		return sub, zero, e
	}
	analysis, ok, e := node.Field("analysis")
	if e != nil || !ok {
		return sub, zero, errors.New("语义分析缺失")
	}
	sub.Analysis.Summary, e = semanticText(ctx, analysis, "summary", 512)
	if e != nil {
		return sub, zero, e
	}
	if e = semanticArray(analysis, "cues", func(n streamjson.Node) (bool, error) {
		var c semantics.Cue
		var e error
		c.Kind, e = semanticText(ctx, n, "kind", 64)
		if e != nil {
			return false, e
		}
		c.Text, e = semanticText(ctx, n, "text", 384)
		if e != nil {
			return false, e
		}
		sub.Analysis.Cues = append(sub.Analysis.Cues, c)
		sub.Analysis = semantics.NormalizeAnalysisFields(asset, sub.Analysis)
		return len(sub.Analysis.Cues) == 24, nil
	}); e != nil {
		return sub, zero, e
	}
	if e = semanticArray(analysis, "topics", func(n streamjson.Node) (bool, error) {
		text, e := n.Trimmed(ctx)
		if e != nil {
			return false, e
		}
		r, e := text.Open(ctx)
		if e != nil {
			return false, e
		}
		b := bufio.NewReaderSize(r, 4096)
		v := []rune{}
		for len(v) <= 128 {
			c, _, e := b.ReadRune()
			if e == io.EOF {
				break
			}
			if e != nil {
				r.Close()
				return false, e
			}
			v = append(v, c)
		}
		r.Close()
		sub.Analysis.Topics = append(sub.Analysis.Topics, string(v))
		sub.Analysis = semantics.NormalizeAnalysisFields(asset, sub.Analysis)
		return len(sub.Analysis.Topics) == 24, nil
	}); e != nil {
		return sub, zero, e
	}
	if e = semanticArray(analysis, "inferred_contexts", func(n streamjson.Node) (bool, error) {
		var c semantics.InferredContext
		var e error
		for _, f := range []struct {
			k     string
			v     *string
			limit int
		}{{"key", &c.Key, 128}, {"value", &c.Value, 256}, {"evidence", &c.Evidence, 240}} {
			*f.v, e = semanticText(ctx, n, f.k, f.limit)
			if e != nil {
				return false, e
			}
		}
		if e = semanticOptional(n, "confidence", &c.Confidence, 64); e != nil {
			return false, e
		}
		checked := semantics.NormalizeAnalysisFields(asset, semantics.Analysis{Contexts: []semantics.InferredContext{c}})
		if len(checked.Contexts) == 0 {
			return false, nil
		}
		c = checked.Contexts[0]
		allowed, e := s.Store.InferredContextAllowed(ctx, asset.ID, strings.TrimSpace(c.Key), strings.TrimSpace(c.Value))
		if e != nil {
			return false, e
		}
		if !allowed {
			return false, nil
		}
		sub.Analysis.Contexts = append(sub.Analysis.Contexts, c)
		sub.Analysis = semantics.NormalizeAnalysisFields(asset, sub.Analysis)
		return len(sub.Analysis.Contexts) == 16, nil
	}); e != nil {
		return sub, zero, e
	}
	if e = semanticArray(analysis, "relations", func(n streamjson.Node) (bool, error) {
		var c semantics.Relation
		var e error
		for _, f := range []struct {
			k string
			v any
		}{{"type", &c.Type}, {"target_id", &c.TargetID}, {"direction", &c.Direction}, {"confidence", &c.Confidence}, {"target_revision", &c.TargetRevision}, {"inferred_by", &c.InferredBy}} {
			if e = semanticOptional(n, f.k, f.v, 2048); e != nil {
				return false, e
			}
		}
		if len(c.TargetID) > 256 || len(c.InferredBy) > 256 {
			return false, errors.New("语义关系引用身份无效")
		}
		c.Evidence, e = semanticText(ctx, n, "evidence", 240)
		if e != nil {
			return false, e
		}
		sub.Analysis.Relations = append(sub.Analysis.Relations, c)
		sub.Analysis = semantics.NormalizeAnalysisFields(asset, sub.Analysis)
		return len(sub.Analysis.Relations) == 32, nil
	}); e != nil {
		return sub, zero, e
	}
	org, _, e := analysis.Field("organization")
	return sub, org, e
}

// writeWithOrganization preserves struct field order and JSON escaping, so
// persisted receipt and organization digests remain identical to the old path.
func (s *StreamingAssets) writeWithOrganization(ctx context.Context, w io.Writer, value any, organization func(io.Writer) error) error {
	data, e := json.Marshal(value)
	if e != nil {
		return e
	}
	d, e := streamjson.Parse(ctx, s.Scratch, strings.NewReader(string(data)), resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Root().Object(w, map[string]func(io.Writer) error{"analysis": func(w io.Writer) error {
		a, _, e := d.Root().Field("analysis")
		if e != nil {
			return e
		}
		return a.Object(w, map[string]func(io.Writer) error{"organization": organization})
	}})
}
func (s *StreamingAssets) streamReceipt(ctx context.Context, sub semantics.Submission, organization func(io.Writer) error) (semantics.SubmissionReceipt, error) {
	base, e := semantics.NewSubmissionReceipt(sub)
	if e != nil {
		return base, e
	}
	sub.AcceptedAt = time.Time{}
	sub.Capability.Execution = ""
	h := sha256.New()
	if e = s.writeWithOrganization(ctx, h, sub, organization); e != nil {
		return base, e
	}
	base.SHA256 = hex.EncodeToString(h.Sum(nil))
	return base, nil
}
