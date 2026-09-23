package core

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

type runeCounter struct {
	io.Reader
	count int64
}

// Retained evidence is discoverable on every read, but its potentially large
// payload is streamed only on request. The enclosing delivery guards both
// representations with the same source epoch and current authorization.
func (s *StreamingAssets) writeOriginal(ctx context.Context, w io.Writer, id string, include bool) error {
	revision, err := s.Store.OriginalRevision(ctx, id)
	if err != nil || revision == 0 {
		return err
	}
	if _, err = io.WriteString(w, `,"original":{"revision":`); err != nil {
		return err
	}
	if err = writeJSON(w, revision); err != nil {
		return err
	}
	if include {
		rev, body, e := s.Store.OpenOriginal(ctx, id, false)
		if e != nil {
			return e
		}
		if rev != revision {
			body.Close()
			return errors.New("原件已变化，请重新读取")
		}
		_, err = io.WriteString(w, `,"content":`)
		if err == nil {
			err = streamjson.WriteString(ctx, w, body)
		}
		body.Close()
		if err != nil {
			return err
		}
		rev, metadata, e := s.Store.OpenOriginal(ctx, id, true)
		if e != nil {
			return e
		}
		if rev != revision {
			metadata.Close()
			return errors.New("原件已变化，请重新读取")
		}
		doc, e := streamjson.Parse(ctx, s.Scratch, metadata, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes)
		metadata.Close()
		if e != nil {
			return e
		}
		defer doc.Close()
		source, found, e := doc.Root().Field("source")
		if e != nil {
			return e
		}
		if found {
			if _, err = io.WriteString(w, `,"source":`); err != nil {
				return err
			}
			if err = source.Copy(w); err != nil {
				return err
			}
		}
	}
	_, err = io.WriteString(w, "}")
	return err
}

func (r *runeCounter) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	for _, v := range b[:n] {
		if v&0xc0 != 0x80 {
			r.count++
		}
	}
	return n, err
}

func (s *StreamingAssets) writeInformation(ctx context.Context, w io.Writer, m contract.AssetMeta) (int64, error) {
	header := domain.Information{Schema: domain.AssetSchema, ID: m.ID, Revision: m.Revision, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, Kind: m.Kind}
	// 固定元数据很小；完整正文和可变长字段始终按流写出。
	raw, _ := json.Marshal(header)
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	if _, err := io.WriteString(w, "{"); err != nil {
		return 0, err
	}
	for i, key := range []string{"schema", "id", "revision", "created_at", "updated_at", "kind"} {
		if i > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return 0, err
			}
		}
		if err := writeJSON(w, key); err != nil {
			return 0, err
		}
		if _, err := io.WriteString(w, ":"); err != nil {
			return 0, err
		}
		if _, err := w.Write(fields[key]); err != nil {
			return 0, err
		}
	}
	if _, err := io.WriteString(w, `,"content":`); err != nil {
		return 0, err
	}
	content, err := s.Store.OpenContent(ctx, m.ID, m.Revision)
	if err != nil {
		return 0, err
	}
	counter := &runeCounter{Reader: content}
	err = streamjson.WriteString(ctx, w, counter)
	content.Close()
	if err != nil {
		return 0, err
	}
	detailSource, err := s.Store.OpenDetails(ctx, m.ID, m.Revision)
	if err != nil {
		return 0, err
	}
	details, err := streamjson.Parse(ctx, s.Scratch, detailSource, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes)
	detailSource.Close()
	if err != nil {
		return 0, err
	}
	defer details.Close()
	for _, key := range []string{"contexts", "explicit_relations", "source"} {
		value, ok, err := details.Root().Field(key)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		if _, err = io.WriteString(w, ","); err != nil {
			return 0, err
		}
		if err = writeJSON(w, key); err != nil {
			return 0, err
		}
		if _, err = io.WriteString(w, ":"); err != nil {
			return 0, err
		}
		if err = value.Copy(w); err != nil {
			return 0, err
		}
	}
	_, err = io.WriteString(w, "}")
	return counter.count, err
}

func (s *StreamingAssets) writeReadBasis(ctx context.Context, w io.Writer, m contract.AssetMeta, fingerprint string, runes int64) error {
	return s.writeReadBasisRange(ctx, w, m, fingerprint, 0, runes)
}
func (s *StreamingAssets) writeReadBasisRange(ctx context.Context, w io.Writer, m contract.AssetMeta, fingerprint string, start, end int64) error {
	if _, err := io.WriteString(w, `,"clarifications":[`); err != nil {
		return err
	}
	notesHash := sha256.New()
	notes, err := streamjson.NewSortedStrings(ctx, s.Scratch)
	if err != nil {
		return err
	}
	defer notes.Close()
	first := true
	err = s.Store.VisitQualifiers(ctx, m.ID, func(q boundedstore.Qualifier) error {
		meta, err := s.Store.ReadAssetMeta(ctx, q.Meta.ID, q.Meta.Revision)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = s.writeInformation(ctx, h, meta)
		if err != nil {
			return err
		}
		source, err := s.Store.OpenDetails(ctx, meta.ID, meta.Revision)
		if err != nil {
			return err
		}
		doc, err := streamjson.Parse(ctx, s.Scratch, source, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes)
		source.Close()
		if err != nil {
			return err
		}
		defer doc.Close()
		relations, _, err := doc.Root().Field("explicit_relations")
		if err != nil {
			return err
		}
		cursor := relations.Children()
		var relation streamjson.Node
		for i := int64(0); i <= q.Link.Ordinal; i++ {
			relation, err = cursor.Next()
			if err != nil {
				return err
			}
		}
		selector, has, err := relation.Field("selector")
		if err != nil {
			return err
		}
		has = has && selector.Kind != 'n'
		clarification := contract.Clarification{SourceID: meta.ID, SourceRevision: meta.Revision, Covered: start >= 0 && meta.ID == m.ID}
		if has {
			clarification.Covered = clarification.Covered && start <= q.Link.StartRune && end >= q.Link.EndRune
			ref, err := s.rangeReference(ctx, meta, q.Link.StartRune, q.Link.EndRune)
			if err != nil {
				return err
			}
			clarification.Evidence = &ref
		}
		if !first {
			if _, err = io.WriteString(w, ","); err != nil {
				return err
			}
		}
		first = false
		if err = writeJSON(w, clarification); err != nil {
			return err
		}
		note, err := streamjson.Build(ctx, s.Scratch, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes, func(out io.Writer) error {
			if _, err := io.WriteString(out, `{"ID":`); err != nil {
				return err
			}
			if err := writeJSON(out, meta.ID); err != nil {
				return err
			}
			if _, err := io.WriteString(out, `,"Hash":`); err != nil {
				return err
			}
			if err := writeJSON(out, hex.EncodeToString(h.Sum(nil))); err != nil {
				return err
			}
			if _, err := io.WriteString(out, `,"Selector":`); err != nil {
				return err
			}
			if has {
				if err := writeStringFields(ctx, out, selector, "exact", "prefix", "suffix"); err != nil {
					return err
				}
			} else {
				if _, err := io.WriteString(out, "null"); err != nil {
					return err
				}
			}
			_, err := io.WriteString(out, "}")
			return err
		})
		if err != nil {
			return err
		}
		defer note.Close()
		return notes.Add(note.Root().Raw())
	})
	if err != nil {
		return err
	}
	if err = notes.WriteJSON(notesHash); err != nil {
		return err
	}
	basis, _ := json.Marshal(informationBasis{contract.BasisSchema, contract.InformationSystem(ctx), m.ID, m.Revision, fingerprint, hex.EncodeToString(notesHash.Sum(nil)), int(start), int(end)})
	if _, err = io.WriteString(w, `],"basis":`); err != nil {
		return err
	}
	if err = writeJSON(w, "b1-"+base64.RawURLEncoding.EncodeToString(basis)); err != nil {
		return err
	}
	_, err = io.WriteString(w, "}")
	return err
}

func (s *StreamingAssets) rangeReference(ctx context.Context, m contract.AssetMeta, start, end int64) (domain.EvidenceReference, error) {
	r, err := s.Store.OpenContent(ctx, m.ID, m.Revision)
	if err != nil {
		return domain.EvidenceReference{}, err
	}
	defer r.Close()
	b := bufio.NewReaderSize(r, streamjson.BufferBytes)
	var offset, startByte, endByte int64
	h := sha256.New()
	for index := int64(0); index < end; index++ {
		v, size, err := b.ReadRune()
		if err != nil {
			return domain.EvidenceReference{}, err
		}
		if index == start {
			startByte = offset
		}
		if index >= start {
			io.WriteString(h, string(v))
		}
		offset += int64(size)
	}
	endByte = offset
	return derived.StreamEvidenceReference(m.ID, m.Revision, int(start), int(end), int(startByte), int(endByte), hex.EncodeToString(h.Sum(nil)))
}
