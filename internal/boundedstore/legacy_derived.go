package boundedstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func (s *Store) importLegacyDerived(ctx context.Context, root string, sources map[string]string, desired string) error {
	var name string
	for n, d := range sources {
		if d != "absent" && strings.HasSuffix(n, "/organization.binlog") {
			name = n
		}
	}
	if name == "" {
		if sources["state/organization.jsonl"] != "" && sources["state/organization.jsonl"] != "absent" {
			name = "state/organization.jsonl"
		} else {
			return nil
		}
	}
	var f *os.File
	var e error
	if strings.HasSuffix(name, ".jsonl") {
		f, e = s.legacyJSONFile(ctx, filepath.Join(root, filepath.FromSlash(name)))
	} else {
		f, e = os.Open(filepath.Join(root, filepath.FromSlash(name)))
	}
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = s.writer.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS migration_derived(asset TEXT PRIMARY KEY,revision INTEGER NOT NULL,position INTEGER NOT NULL,metadata INTEGER NOT NULL,vector INTEGER NOT NULL,imported INTEGER NOT NULL DEFAULT 0) WITHOUT ROWID;`); e != nil {
		return e
	}
	var start int64
	e = s.writer.QueryRowContext(ctx, "SELECT position FROM migration_progress WHERE key='derived'").Scan(&start)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	info, e := f.Stat()
	if e != nil {
		return e
	}
	var sealed int64
	if strings.Contains(name, "/generations/") {
		var m struct {
			SealedBytes int64  `json:"sealed_bytes"`
			LogSHA      string `json:"log_sha256"`
		}
		if e = readSmallJSON(filepath.Join(filepath.Dir(f.Name()), "manifest.json"), &m); e != nil {
			return e
		}
		if m.SealedBytes < 0 || m.SealedBytes > info.Size() {
			return errors.New("旧派生封存长度无效")
		}
		h := sha256.New()
		if _, e = copyContext(ctx, h, io.NewSectionReader(f, 0, m.SealedBytes)); e != nil {
			return e
		}
		if hex.EncodeToString(h.Sum(nil)) != m.LogSHA {
			return errors.New("旧派生封存内容已改变")
		}
		sealed = m.SealedBytes
	}
	for start < info.Size() {
		var head [16]byte
		n, e := f.ReadAt(head[:], start)
		if e == io.EOF && n < 16 {
			if start < sealed {
				return errors.New("旧派生封存区帧不完整")
			}
			break
		}
		if e != nil {
			return e
		}
		if string(head[:4]) != "OWD3" {
			return errors.New("旧派生日志头无效")
		}
		metadata, vectors := int64(binary.LittleEndian.Uint32(head[4:8])), int64(binary.LittleEndian.Uint32(head[8:12]))
		if metadata <= 0 || vectors%4 != 0 || vectors > 8192*4 {
			return errors.New("旧派生日志长度无效")
		}
		end := start + 16 + metadata + vectors + 4
		if end > info.Size() {
			if start < sealed {
				return errors.New("旧派生封存区帧不完整")
			}
			break
		}
		var tail [4]byte
		if _, e = f.ReadAt(tail[:], end-4); e != nil {
			return e
		}
		if string(tail[:]) != "DONE" {
			return errors.New("旧派生日志提交标记无效")
		}
		crc := crc32.NewIEEE()
		if _, e = copyContext(ctx, crc, io.NewSectionReader(f, start+16, metadata+vectors)); e != nil {
			return e
		}
		if crc.Sum32() != binary.LittleEndian.Uint32(head[12:]) {
			return errors.New("旧派生日志校验失败")
		}
		doc, e := streamjson.Parse(ctx, s.directory, io.NewSectionReader(f, start+16, metadata), s.budget, 256*resourcebudget.MiB)
		if e != nil {
			return e
		}
		asset, e := nodeString(doc.Root(), "asset_id")
		var rev uint64
		if e == nil {
			e = nodeDecode(doc.Root(), "asset_revision", &rev)
		}
		doc.Close()
		if e != nil || asset == "" || rev == 0 {
			return errors.New("旧派生资产身份无效")
		}
		e = s.write(ctx, func(tx *sql.Tx) error {
			var old uint64
			err := tx.QueryRowContext(ctx, "SELECT revision FROM migration_derived WHERE asset=?", asset).Scan(&old)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if old > rev {
				return errors.New("旧派生版本倒退")
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO migration_derived VALUES(?,?,?,?,?,0) ON CONFLICT(asset) DO UPDATE SET revision=excluded.revision,position=excluded.position,metadata=excluded.metadata,vector=excluded.vector,imported=0", asset, rev, start+16, metadata, vectors); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO migration_progress VALUES('derived',?) ON CONFLICT(key) DO UPDATE SET position=excluded.position", end)
			return err
		})
		if e != nil {
			return e
		}
		start = end
	}
	generation := "legacy-migration"
	for {
		var asset string
		var position, metadata, vectors int64
		e = s.writer.QueryRowContext(ctx, `SELECT d.asset,d.position,d.metadata,d.vector FROM migration_derived d JOIN live_assets a ON a.id=d.asset AND a.revision=d.revision WHERE d.imported=0 ORDER BY d.asset LIMIT 1`).Scan(&asset, &position, &metadata, &vectors)
		if errors.Is(e, sql.ErrNoRows) {
			break
		}
		if e != nil {
			return e
		}
		if e = s.importLegacyOrganization(ctx, f, generation, position, metadata, vectors, desired); e != nil {
			return fmt.Errorf("迁移组织 %s: %w", asset, e)
		}
	}
	// Activate only after every current record is in place. Cross-source and
	// cyclic dependencies must see the same complete source snapshot.
	e = s.write(ctx, func(tx *sql.Tx) error {
		var exists bool
		if e := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM generations WHERE id=?)", generation).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return nil
		}
		if _, e := tx.ExecContext(ctx, "UPDATE generations SET state='active' WHERE id=?", generation); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_state VALUES(1,?,1)", generation); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO organization_publications(organization) SELECT organization FROM organization_current WHERE generation=? ORDER BY asset", generation); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim SELECT space,'vector_pack','migration' FROM vector_delta GROUP BY space"); e != nil {
			return e
		}
		return nil
	})
	if e != nil {
		return e
	}
	var cursor string
	for {
		var id, asset string
		e = s.writer.QueryRowContext(ctx, "SELECT organization,asset FROM organization_current WHERE generation=? AND asset>? ORDER BY asset LIMIT 1", generation, cursor).Scan(&id, &asset)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		v, err := s.CurrentOrganization(ctx, generation, asset)
		if errors.Is(err, ErrNotFound) {
			e = s.write(ctx, func(tx *sql.Tx) error { return s.retireOrganization(ctx, tx, id) })
		} else if err != nil {
			return err
		} else {
			h, err := s.RecordHeader(ctx, v)
			if err != nil {
				return err
			}
			if !h.HasPendingSemanticWork() {
				_, e = s.writer.ExecContext(ctx, "DELETE FROM semantic_jobs WHERE asset=? AND revision=?", v.Asset, v.Revision)
			}
		}
		if e != nil {
			return e
		}
		cursor = asset
	}
}

func (s *Store) importLegacyOrganization(ctx context.Context, f *os.File, generation string, position, metadata, vectors int64, desired string) error {
	doc, e := streamjson.Parse(ctx, s.directory, io.NewSectionReader(f, position, metadata), s.budget, 256*resourcebudget.MiB)
	if e != nil {
		return e
	}
	defer doc.Close()
	schema, e := nodeString(doc.Root(), "schema")
	if e != nil {
		return e
	}
	if schema != "ownward.derived/v5" && schema != "ownward.derived/v4" && schema != "ownward.derived/v3" && schema != "ownward.derived/v2" {
		return fmt.Errorf("需要兼容转换的旧派生格式: %s", schema)
	}
	work, receipt, e := s.legacySemanticReferences(ctx, doc.Root())
	if e != nil {
		return e
	}
	space, e := nodeString(doc.Root(), "embedding_space")
	if e != nil {
		return e
	}
	if space == "" {
		space = "unassigned"
	}
	if desired != "" {
		if vectors > 0 && !embedding.CompatibleSpace(space, desired) {
			return errors.New("旧向量空间未经兼容验证")
		}
		space = desired
	}
	if e = s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO generations VALUES(?,?,'building')", generation, space)
		return e
	}); e != nil {
		return e
	}
	value, e := streamjson.Build(ctx, s.directory, s.budget, 256*resourcebudget.MiB, func(w io.Writer) error {
		io.WriteString(w, "{")
		cursor := doc.Root().Children()
		first := true
		for {
			n, e := cursor.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			key, e := n.Key()
			if e != nil {
				return e
			}
			if key == "embedding" || key == "embedding_f32le" || key == "semantic_work" || key == "semantic_result" {
				continue
			}
			if !first {
				io.WriteString(w, ",")
			}
			first = false
			b, _ := json.Marshal(key)
			w.Write(b)
			io.WriteString(w, ":")
			if key == "schema" {
				if e = json.NewEncoder(w).Encode("ownward.derived/v5"); e != nil {
					return e
				}
				continue
			}
			if key == "embedding_space" {
				if e = json.NewEncoder(w).Encode(space); e != nil {
					return e
				}
				continue
			}
			if e = n.Copy(w); e != nil {
				return e
			}
		}
		if work != nil {
			io.WriteString(w, ",\"semantic_work_reference\":")
			if e = json.NewEncoder(w).Encode(work); e != nil {
				return e
			}
		}
		if receipt != nil {
			io.WriteString(w, ",\"semantic_receipt\":")
			if e = json.NewEncoder(w).Encode(receipt); e != nil {
				return e
			}
		}
		if vectors > 0 {
			if vectors != 512*4 {
				return errors.New("旧向量维度不兼容")
			}
			io.WriteString(w, ",\"embedding\":[")
			var b [4]byte
			for i := int64(0); i < vectors; i += 4 {
				if _, e := f.ReadAt(b[:], position+metadata+i); e != nil {
					return e
				}
				x := math.Float32frombits(binary.LittleEndian.Uint32(b[:]))
				if i != 0 {
					io.WriteString(w, ",")
				}
				p, e := json.Marshal(x)
				if e != nil {
					return e
				}
				if _, e = w.Write(p); e != nil {
					return e
				}
			}
			io.WriteString(w, "]")
		}
		_, e := io.WriteString(w, "}")
		return e
	})
	if e != nil {
		return e
	}
	defer value.Close()
	v, e := s.StageOrganization(ctx, generation, streamjson.RawSource{Node: value.Root()})
	if e != nil {
		return e
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		for _, query := range []string{"UPDATE organizations SET state='active' WHERE id=?", "INSERT INTO organization_current SELECT generation,asset,id FROM organizations WHERE id=?", "INSERT INTO organization_heads SELECT generation,asset,id FROM organizations WHERE id=?", "INSERT OR IGNORE INTO vector_delta SELECT organization,space FROM vectors WHERE organization=?"} {
			if _, e := tx.ExecContext(ctx, query, v.ID); e != nil {
				return e
			}
		}
		_, e := tx.ExecContext(ctx, "UPDATE migration_derived SET imported=1 WHERE asset=?", v.Asset)
		return e
	})
}
