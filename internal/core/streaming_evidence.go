package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func (s *StreamingAssets) buildRetrieval(ctx context.Context, build func(context.Context, io.Writer) error) (*contract.StreamResult, error) {
	var doc *streamjson.Document
	var stamp boundedstore.RetrievalStamp
	err := s.Store.WithSnapshot(ctx, func(ctx context.Context) error {
		var err error
		stamp, err = s.Store.RetrievalStamp(ctx)
		if err != nil {
			return err
		}
		doc, err = streamjson.Build(ctx, s.Scratch, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes, func(w io.Writer) error { return build(ctx, w) })
		return err
	})
	if err != nil {
		return nil, err
	}
	check := func(delivery context.Context) error {
		checking, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		stop := context.AfterFunc(delivery, cancel)
		defer stop()
		if err := delivery.Err(); err != nil {
			return err
		}
		return s.Store.AuthorizeRetrieval(checking, stamp)
	}
	return &contract.StreamResult{Value: streamjson.RawSource{Node: doc.RootContext(context.WithoutCancel(ctx))}, Check: check, Close: doc.Close}, nil
}

func (s *StreamingAssets) evidenceTool(ctx context.Context, operation string, args streamjson.Node) (*contract.StreamResult, error) {
	if operation == "ownward_evidence_search" {
		query, e := queryField(ctx, args)
		if e != nil {
			return nil, e
		}
		id, e := fieldString(args, "source_id", 256)
		if e != nil {
			return nil, e
		}
		limit, e := integerField(args, "limit")
		if e != nil {
			return nil, e
		}
		if limit == 0 {
			limit = 3
		}
		if limit < 1 || limit > 8 {
			return nil, errors.New("证据数量必须介于一和八之间")
		}
		return s.buildRetrieval(ctx, func(ctx context.Context, w io.Writer) error {
			stamp, e := s.Store.RetrievalStamp(ctx)
			if e != nil {
				return e
			}
			refs, e := s.Store.RankEvidenceSource(ctx, stamp.Generation, id, query, limit)
			if e != nil {
				return e
			}
			return writeJSON(w, struct {
				Evidence []domain.EvidenceReference `json:"evidence"`
			}{refs})
		})
	}
	id, err := fieldString(args, "id", 2048)
	if err != nil {
		return nil, err
	}
	unit, err := derived.ParseEvidenceUnitID(id)
	if err != nil {
		return nil, err
	}
	return s.buildRetrieval(ctx, func(ctx context.Context, w io.Writer) error {
		meta, err := s.Store.ReadAssetMeta(ctx, unit.SourceID, unit.SourceRevision)
		if err != nil {
			return err
		}
		actual, err := s.rangeReference(ctx, meta, int64(unit.StartRune), int64(unit.EndRune))
		if err != nil {
			return err
		}
		verified, err := derived.ParseEvidenceUnitID(actual.ID)
		if err != nil {
			return err
		}
		r, err := s.Store.OpenRange(ctx, unit.SourceID, unit.SourceRevision, int64(verified.StartByte), int64(verified.EndByte))
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.CopyBuffer(h, r, make([]byte, streamjson.BufferBytes))
		r.Close()
		if err != nil {
			return err
		}
		if (strings.HasPrefix(id, "e2-") && (unit.StartByte != verified.StartByte || unit.EndByte != verified.EndByte)) || !derived.EvidenceStreamMatches(unit, hex.EncodeToString(h.Sum(nil))) {
			return errors.New("证据引用与当前原文不一致")
		}
		var prelude string
		var end int
		if unit.StartRune > 0 {
			body, e := s.Store.OpenContent(ctx, meta.ID, meta.Revision)
			if e != nil {
				return e
			}
			e = derived.WalkEvidenceRanges(meta.ID, meta.Revision, body, func(first derived.EvidenceUnit) error {
				end = min(first.EndRune, unit.StartRune)
				prelude = string([]rune(first.Content)[:end])
				return io.EOF
			})
			body.Close()
			if e != nil && e != io.EOF {
				return e
			}
		}
		if _, err = io.WriteString(w, `{"evidence":{"schema":`); err != nil {
			return err
		}
		if err = writeJSON(w, domain.EvidenceSchema); err != nil {
			return err
		}
		for _, f := range []struct {
			k string
			v any
		}{{"id", id}, {"source_id", meta.ID}, {"source_revision", meta.Revision}, {"start_rune", unit.StartRune}, {"end_rune", unit.EndRune}, {"source_prelude", prelude}, {"source_prelude_start_rune", 0}, {"source_prelude_end_rune", end}} {
			io.WriteString(w, `,"`+f.k+`":`)
			if err = writeJSON(w, f.v); err != nil {
				return err
			}
		}
		io.WriteString(w, `,"content":`)
		r, err = s.Store.OpenRange(ctx, meta.ID, meta.Revision, int64(verified.StartByte), int64(verified.EndByte))
		if err != nil {
			return err
		}
		err = streamjson.WriteString(ctx, w, r)
		r.Close()
		if err != nil {
			return err
		}
		io.WriteString(w, "}")
		hash := sha256.New()
		_, err = s.writeInformation(ctx, hash, meta)
		if err != nil {
			return err
		}
		if err = s.writeOriginal(ctx, w, meta.ID, false); err != nil {
			return err
		}
		return s.writeReadBasisRange(ctx, w, meta, hex.EncodeToString(hash.Sum(nil)), int64(unit.StartRune), int64(unit.EndRune))
	})
}
