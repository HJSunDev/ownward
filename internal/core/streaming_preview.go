package core

import (
	"bufio"
	"context"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

// PreviewInformation supplies the owner's existing deletion confirmation. It
// reads only its visible excerpt rather than materializing the whole source.
func (s *StreamingAssets) PreviewInformation(ctx context.Context, id string, revision uint64) (domain.Information, error) {
	ctx, e := s.Store.BeginAccess(ctx, contract.AuthenticationDigest(ctx), contract.ManagePermission)
	if e != nil {
		return domain.Information{}, e
	}
	m, e := s.Store.ReadAssetMeta(ctx, id, revision)
	if e != nil {
		return domain.Information{}, e
	}
	if m.Revision != revision {
		return domain.Information{}, errors.New("资料版本已改变")
	}
	stamp, e := s.Store.RetrievalStamp(ctx)
	if e != nil {
		return domain.Information{}, e
	}
	r, e := s.Store.OpenContent(ctx, id, revision)
	if e != nil {
		return domain.Information{}, e
	}
	text, e := previewText(r, 241)
	r.Close()
	if e != nil {
		return domain.Information{}, e
	}
	out := domain.Information{ID: id, Revision: revision, Kind: m.Kind, CreatedAt: m.CreatedAt, Content: text}
	r, e = s.Store.OpenDetails(ctx, id, revision)
	if e != nil {
		return out, e
	}
	d, e := streamjson.Parse(ctx, s.Scratch, r, s.Budget, s.DiskBytes)
	r.Close()
	if e != nil {
		return out, e
	}
	defer d.Close()
	source, ok, e := d.Root().Field("source")
	if e != nil {
		return out, e
	}
	if ok && source.Kind == '{' {
		for _, f := range []struct {
			k string
			v *string
		}{{"actor", &out.Source.Actor}, {"ref", &out.Source.Ref}} {
			n, ok, e := source.Field(f.k)
			if e != nil {
				return out, e
			}
			if !ok {
				continue
			}
			r, e := n.Open(ctx)
			if e != nil {
				return out, e
			}
			*f.v, e = previewText(r, 240)
			r.Close()
			if e != nil {
				return out, e
			}
		}
	}
	return out, s.Store.AuthorizeRetrieval(ctx, stamp)
}
func previewText(r io.Reader, limit int) (string, error) {
	b := bufio.NewReaderSize(r, 4096)
	text := make([]rune, 0, limit)
	for len(text) < limit {
		c, _, e := b.ReadRune()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
		text = append(text, c)
	}
	return string(text), nil
}
