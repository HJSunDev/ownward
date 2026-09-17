package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func decodeInformationBasis(ref string) (informationBasis, error) {
	var b informationBasis
	if len(ref) > 2048 || !strings.HasPrefix(ref, "b1-") {
		return b, errors.New("依据格式无效")
	}
	data, e := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref, "b1-"))
	if e != nil {
		return b, e
	}
	e = json.Unmarshal(data, &b)
	return b, e
}
func (s *StreamingAssets) checkTool(ctx context.Context, n streamjson.Node) (*contract.StreamResult, error) {
	var refs []string
	values, ok, e := n.Field("bases")
	if e != nil || !ok {
		return nil, errors.New("缺少待核对依据")
	}
	if e := values.DecodeSmall(&refs, 256*1024); e != nil {
		return nil, e
	}
	if len(refs) > contract.MaxCheckItems {
		return nil, errors.New("每次最多核对64项依据")
	}
	stamp, e := s.Store.RetrievalStamp(ctx)
	if e != nil {
		return nil, e
	}
	doc, e := streamjson.Build(ctx, s.Scratch, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes, func(w io.Writer) error {
		if _, e := io.WriteString(w, `{"results":[`); e != nil {
			return e
		}
		for i, ref := range refs {
			if i > 0 {
				if _, e := io.WriteString(w, ","); e != nil {
					return e
				}
			}
			if e := s.writeCheck(ctx, w, ref); e != nil {
				return e
			}
		}
		_, e := io.WriteString(w, `]}`)
		return e
	})
	if e != nil {
		return nil, e
	}
	return s.readResult(ctx, doc, stamp), nil
}
func (s *StreamingAssets) writeCheck(ctx context.Context, w io.Writer, ref string) error {
	out := contract.InformationCheck{Basis: ref, Status: "unverifiable"}
	b, e := decodeInformationBasis(ref)
	if e != nil || b.Schema != contract.BasisSchema || b.System == "" || b.System != contract.InformationSystem(ctx) || b.ID == "" || b.Revision == 0 || len(b.Content) != 64 || len(b.Notes) != 64 || b.Start < 0 || b.End <= b.Start {
		return writeJSON(w, out)
	}
	m, e := s.Store.ReadAssetMeta(ctx, b.ID, 0)
	if errors.Is(e, boundedstore.ErrNotFound) {
		out.Status = "unavailable"
		return writeJSON(w, out)
	}
	if e != nil {
		return e
	}
	hash := sha256.New()
	if _, e = s.writeInformation(ctx, hash, m); e != nil {
		return e
	}
	current, e := streamjson.Build(ctx, s.Scratch, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes, func(w io.Writer) error {
		if _, e := io.WriteString(w, `{"read":false`); e != nil {
			return e
		}
		return s.writeReadBasisRange(ctx, w, m, hex.EncodeToString(hash.Sum(nil)), -1, -1)
	})
	if e != nil {
		return e
	}
	defer current.Close()
	basis, e := fieldString(current.Root(), "basis", 2048)
	if e != nil {
		return e
	}
	now, e := decodeInformationBasis(basis)
	if e != nil {
		return e
	}
	out.Status = "unchanged"
	if b.Revision == now.Revision && b.Content == now.Content && b.Notes == now.Notes {
		return writeJSON(w, out)
	}
	out.Status = "changed"
	out.SourceID = b.ID
	notes, _, e := current.Root().Field("clarifications")
	if e != nil {
		return e
	}
	value := struct {
		Basis          string `json:"basis"`
		Status         string `json:"status"`
		SourceID       string `json:"source_id"`
		Clarifications any    `json:"clarifications"`
	}{out.Basis, out.Status, out.SourceID, nil}
	return s.writeProjected(ctx, w, value, map[string]func(io.Writer) error{"clarifications": notes.Copy})
}
func (s *StreamingAssets) readResult(ctx context.Context, doc *streamjson.Document, stamp boundedstore.RetrievalStamp) *contract.StreamResult {
	check := func(delivery context.Context) error {
		checking, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		stop := context.AfterFunc(delivery, cancel)
		defer stop()
		if e := delivery.Err(); e != nil {
			return e
		}
		return s.Store.AuthorizeRetrieval(checking, stamp)
	}
	return &contract.StreamResult{Value: streamjson.RawSource{Node: doc.RootContext(context.WithoutCancel(ctx))}, Close: doc.Close, Check: check}
}
