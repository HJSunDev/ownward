package boundedstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func (s *Store) verifyMigrationSources(ctx context.Context, root string, sources map[string]string) error {
	for name, want := range sources {
		actual, e := sourceDigest(ctx, filepath.Join(root, filepath.FromSlash(name)))
		if e != nil {
			return e
		}
		if actual != want {
			return errors.New("迁移来源在构建后改变")
		}
	}
	if sources["assets/information.jsonl"] != "absent" {
		f, e := os.Open(filepath.Join(root, "assets", "information.jsonl"))
		if e != nil {
			return e
		}
		defer f.Close()
		after := ""
		for {
			var id, kind, digest string
			var pos, length, size int64
			var revision uint64
			e := s.writer.QueryRowContext(ctx, `SELECT m.id,m.position,m.bytes,a.revision,a.kind,p.digest,p.content_bytes FROM migration_assets m JOIN assets a ON a.id=m.id JOIN payloads p ON p.id=a.payload WHERE m.deleted=0 AND m.id>? ORDER BY m.id LIMIT 1`, after).Scan(&id, &pos, &length, &revision, &kind, &digest, &size)
			if errors.Is(e, sql.ErrNoRows) {
				break
			}
			if e != nil {
				return e
			}
			d, e := streamjson.Parse(ctx, s.directory, io.NewSectionReader(f, pos, length), s.budget, 256*resourcebudget.MiB)
			if e != nil {
				return e
			}
			meta, e := legacyMeta(d.Root())
			if e == nil && (meta.ID != id || meta.Revision != revision || string(meta.Kind) != kind) {
				e = errors.New("迁移资产身份或版本不匹配")
			}
			if e == nil {
				body, _, er := d.Root().Field("content")
				e = er
				if e == nil {
					r, er := body.Open(ctx)
					e = er
					if e == nil {
						h := sha256.New()
						n, er := copyContext(ctx, h, r)
						r.Close()
						e = er
						if e == nil && (n != size || hex.EncodeToString(h.Sum(nil)) != digest) {
							e = errors.New("迁移原文校验不一致")
						}
					}
				}
			}
			d.Close()
			if e != nil {
				return e
			}
			after = id
		}
	}
	var hasDerived bool
	if e := s.writer.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='migration_derived')").Scan(&hasDerived); e != nil {
		return e
	}
	if !hasDerived {
		return nil
	}
	var pending int
	if e := s.writer.QueryRowContext(ctx, `SELECT count(*) FROM migration_derived d JOIN live_assets a ON a.id=d.asset AND a.revision=d.revision WHERE d.imported=0`).Scan(&pending); e != nil {
		return e
	}
	if pending != 0 {
		return errors.New("有效组织材料迁移不完整")
	}
	for name, hash := range sources {
		if hash == "absent" || !strings.HasSuffix(name, "/organization.binlog") {
			continue
		}
		f, e := os.Open(filepath.Join(root, filepath.FromSlash(name)))
		if e != nil {
			return e
		}
		e = func() error {
			after := ""
			for {
				var id string
				var pos, meta, size int64
				var got []byte
				e := s.writer.QueryRowContext(ctx, `SELECT d.asset,d.position,d.metadata,d.vector,v.data FROM migration_derived d JOIN organization_current c ON c.asset=d.asset JOIN vectors v ON v.organization=c.organization WHERE d.imported=1 AND d.vector>0 AND d.asset>? ORDER BY d.asset LIMIT 1`, after).Scan(&id, &pos, &meta, &size, &got)
				if errors.Is(e, sql.ErrNoRows) {
					return nil
				}
				if e != nil {
					return e
				}
				want := make([]byte, size)
				if _, e = f.ReadAt(want, pos+meta); e != nil {
					return e
				}
				if !bytes.Equal(want, got) {
					return errors.New("迁移向量字节不一致")
				}
				after = id
			}
		}()
		f.Close()
		if e != nil {
			return e
		}
	}
	return nil
}
