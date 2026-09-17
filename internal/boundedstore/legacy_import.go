package boundedstore

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

const legacyCatalogSchema = `CREATE TABLE IF NOT EXISTS migration_assets(id TEXT PRIMARY KEY,revision INTEGER NOT NULL,created TEXT NOT NULL,position INTEGER NOT NULL,bytes INTEGER NOT NULL,deleted INTEGER NOT NULL,imported INTEGER NOT NULL DEFAULT 0) WITHOUT ROWID;
 CREATE TABLE IF NOT EXISTS migration_progress(key TEXT PRIMARY KEY,position INTEGER NOT NULL) WITHOUT ROWID;`

func (s *Store) importLegacy(ctx context.Context, root string, sources map[string]string, space string) error {
	if e := validateSourceNames(sources); e != nil {
		return e
	}
	if _, e := s.writer.ExecContext(ctx, legacyCatalogSchema); e != nil {
		return e
	}
	if sources["assets/information.jsonl"] != "absent" {
		f, e := os.Open(filepath.Join(root, "assets", "information.jsonl"))
		if e != nil {
			return e
		}
		defer f.Close()
		var start int64
		e = s.writer.QueryRowContext(ctx, "SELECT position FROM migration_progress WHERE key='assets'").Scan(&start)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e = scanLegacyLines(ctx, f, start, func(position, length int64) error {
			doc, e := streamjson.Parse(ctx, s.directory, io.NewSectionReader(f, position, length), s.budget, 256*resourcebudget.MiB)
			if e != nil {
				return e
			}
			defer doc.Close()
			return s.catalogLegacyEvent(ctx, doc.Root(), position, length)
		}); e != nil {
			return fmt.Errorf("迁移原文日志: %w", e)
		}
		for {
			var id string
			var position, length int64
			e = s.writer.QueryRowContext(ctx, "SELECT id,position,bytes FROM migration_assets WHERE deleted=0 AND imported=0 ORDER BY id LIMIT 1").Scan(&id, &position, &length)
			if errors.Is(e, sql.ErrNoRows) {
				break
			}
			if e != nil {
				return e
			}
			doc, e := streamjson.Parse(ctx, s.directory, io.NewSectionReader(f, position, length), s.budget, 256*resourcebudget.MiB)
			if e != nil {
				return e
			}
			e = s.importLegacyAsset(ctx, doc.Root())
			doc.Close()
			if e != nil {
				return fmt.Errorf("迁移原文 %s: %w", id, e)
			}
		}
	}
	// Tombstones are durable even when the corresponding raw value no longer exists.
	if e := s.write(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO forget_operations(id,principal,digest,state) VALUES('legacy-forgotten','migration','','complete')"); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO forget_targets SELECT 'legacy-forgotten',id,revision FROM migration_assets WHERE deleted=1")
		return e
	}); e != nil {
		return e
	}
	if e := s.importLegacyControl(ctx, root, sources); e != nil {
		return e
	}
	if _, e := s.writer.ExecContext(ctx, `UPDATE lexical_stats SET documents=(SELECT count(*) FROM live_assets),terms=coalesce((SELECT sum(d.length) FROM lexical_documents d JOIN live_assets a ON a.payload=d.payload),0) WHERE singleton=1`); e != nil {
		return e
	}
	return s.importLegacyDerived(ctx, root, sources, space)
}

// Framing keeps one fixed read buffer even for a multi-gigabyte JSON line.
// An incomplete final event was not committed by the old append-only writer.
func scanLegacyLines(ctx context.Context, f *os.File, start int64, visit func(int64, int64) error) error {
	if _, e := f.Seek(start, io.SeekStart); e != nil {
		return e
	}
	r := bufio.NewReaderSize(f, ChunkBytes)
	position, length := start, int64(0)
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		p, e := r.ReadSlice('\n')
		length += int64(len(p))
		if e == bufio.ErrBufferFull {
			continue
		}
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		if e = visit(position, length); e != nil {
			return e
		}
		position += length
		length = 0
	}
}

func legacyMeta(n streamjson.Node) (contract.AssetMeta, error) {
	var m contract.AssetMeta
	var schema string
	for _, p := range []struct {
		k string
		v any
	}{{"schema", &schema}, {"id", &m.ID}, {"revision", &m.Revision}, {"created_at", &m.CreatedAt}, {"updated_at", &m.UpdatedAt}, {"kind", &m.Kind}} {
		if e := nodeDecode(n, p.k, p.v); e != nil {
			return m, e
		}
	}
	if schema != "ownward.information/v1" || m.ID == "" || m.Revision == 0 || m.CreatedAt.IsZero() || m.UpdatedAt.Before(m.CreatedAt) {
		return m, errors.New("旧资产身份或版本无效")
	}
	if _, e := domain.ParseKind(string(m.Kind)); e != nil {
		return m, e
	}
	return m, nil
}

func (s *Store) catalogLegacyEvent(ctx context.Context, n streamjson.Node, position, length int64) error {
	op, e := nodeString(n, "operation")
	if e != nil {
		return e
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		put := func(v streamjson.Node, expected *uint64) error {
			m, e := legacyMeta(v)
			if e != nil {
				return e
			}
			var rev uint64
			var created string
			var deleted bool
			e = tx.QueryRowContext(ctx, "SELECT revision,created,deleted FROM migration_assets WHERE id=?", m.ID).Scan(&rev, &created, &deleted)
			exists := e == nil
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if deleted {
				return errors.New("已遗忘资产被重新写入")
			}
			if op == "create" && (exists || m.Revision != 1) || op == "snapshot" && exists || op == "update" && (!exists || m.Revision != rev+1 || created != stamp(m.CreatedAt)) {
				return errors.New("旧日志资产版本链不连续")
			}
			if expected != nil && ((*expected == 0 && (exists || m.Revision != 1)) || (*expected != 0 && (!exists || rev != *expected || m.Revision != rev+1 || created != stamp(m.CreatedAt)))) {
				return errors.New("旧日志提交版本不匹配")
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO migration_assets VALUES(?,?,?,?,?,0,0) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,position=excluded.position,bytes=excluded.bytes,imported=0`, m.ID, m.Revision, stamp(m.CreatedAt), position+v.Start, v.End-v.Start)
			return e
		}
		receipt := func() error {
			var r contract.MutationReceipt
			if e := nodeDecode(n, "receipt", &r); e != nil {
				return e
			}
			if e := validOperation(r.Operation); e != nil {
				return e
			}
			if len(r.Results) < 1 || len(r.Results) > 20 {
				return errors.New("旧提交回执无效")
			}
			var generation uint64
			if e := tx.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='operation_generation'").Scan(&generation); e != nil {
				return e
			}
			if r.Operation.Generation != generation {
				return errors.New("旧提交回执世代无效")
			}
			b, e := json.Marshal(r.Results)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, "INSERT INTO operation_receipts VALUES(?,?,?,?,?,?,?)", r.Operation.System, r.Operation.Principal, r.Operation.ID, r.Operation.Generation, r.Operation.Kind, r.Operation.Digest, b)
			return e
		}
		switch op {
		case "create", "update", "snapshot":
			v, ok, e := n.Field("value")
			if e != nil || !ok {
				return errors.New("旧日志缺少资产")
			}
			if e = put(v, nil); e != nil {
				return e
			}
		case "mutation":
			var expected []uint64
			if e = nodeDecode(n, "expected", &expected); e != nil {
				return e
			}
			if len(expected) > 20 {
				return errors.New("旧批次过大")
			}
			i := 0
			if e = visitArray(n, "values", func(v streamjson.Node) error {
				if i >= len(expected) {
					return errors.New("旧批次版本缺失")
				}
				e := put(v, &expected[i])
				i++
				return e
			}); e != nil {
				return e
			}
			if i != len(expected) {
				return errors.New("旧批次数量不匹配")
			}
			if e = receipt(); e != nil {
				return e
			}
		case "operation_receipt":
			if e = receipt(); e != nil {
				return e
			}
		case "operation_generation":
			var gen, old uint64
			if e = nodeDecode(n, "generation", &gen); e != nil {
				return e
			}
			if e = tx.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='operation_generation'").Scan(&old); e != nil {
				return e
			}
			if gen <= old {
				return errors.New("旧操作世代无效")
			}
			if _, e = tx.ExecContext(ctx, "DELETE FROM operation_receipts; UPDATE store_meta SET value=? WHERE key='operation_generation'", gen); e != nil {
				return e
			}
		case "forget", "forgotten":
			if e = visitArray(n, "deleted", func(v streamjson.Node) error {
				var d contract.AssetVersion
				if e := v.DecodeSmall(&d, 65536); e != nil {
					return e
				}
				if d.ID == "" || d.Revision == 0 {
					return errors.New("旧删除记录无效")
				}
				var rev uint64
				var dead bool
				e := tx.QueryRowContext(ctx, "SELECT revision,deleted FROM migration_assets WHERE id=?", d.ID).Scan(&rev, &dead)
				if op == "forget" && (e != nil || rev != d.Revision) || op == "forgotten" && e == nil && (!dead || rev != d.Revision) {
					return errors.New("旧删除版本不匹配")
				}
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				_, e = tx.ExecContext(ctx, "INSERT INTO migration_assets VALUES(?,?, '',0,0,1,0) ON CONFLICT(id) DO UPDATE SET deleted=1", d.ID, d.Revision)
				return e
			}); e != nil {
				return e
			}
		default:
			return fmt.Errorf("未知旧日志操作: %s", op)
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO migration_progress VALUES('assets',?) ON CONFLICT(key) DO UPDATE SET position=excluded.position", position+length)
		return e
	})
}

func (s *Store) importLegacyAsset(ctx context.Context, n streamjson.Node) error {
	m, e := legacyMeta(n)
	if e != nil {
		return e
	}
	body, ok, e := n.Field("content")
	if e != nil || !ok || body.Kind != '"' {
		return errors.New("旧正文无效")
	}
	details, e := streamjson.Build(ctx, s.directory, s.budget, 256*resourcebudget.MiB, func(w io.Writer) error {
		io.WriteString(w, "{")
		first := true
		for _, key := range []string{"contexts", "explicit_relations", "source"} {
			v, ok, e := n.Field(key)
			if e != nil {
				return e
			}
			if !ok {
				continue
			}
			if !first {
				io.WriteString(w, ",")
			}
			first = false
			b, _ := json.Marshal(key)
			w.Write(b)
			io.WriteString(w, ":")
			if e = v.Copy(w); e != nil {
				return e
			}
		}
		_, e := io.WriteString(w, "}")
		return e
	})
	if e != nil {
		return e
	}
	defer details.Close()
	p, e := s.Stage(ctx, "legacy-migration", body, streamjson.RawSource{Node: details.Root()})
	if e != nil {
		return e
	}
	defer s.Abandon(context.WithoutCancel(ctx), p)
	if e = s.prepareLexical(ctx, p, m.ID); e != nil {
		return e
	}
	ordinal := 0
	if e = visitArray(n, "explicit_relations", func(v streamjson.Node) error {
		target, e := nodeString(v, "target_id")
		if e != nil {
			return e
		}
		l := ExplicitLink{Ordinal: int64(ordinal), Target: target}
		ordinal++
		selector, ok, e := v.Field("selector")
		if e != nil {
			return e
		}
		if ok && selector.Kind != 'n' {
			part := func(key string) (contract.ContentSource, error) {
				n, ok, e := selector.Field(key)
				if e != nil {
					return nil, e
				}
				if !ok {
					return StringSource(""), nil
				}
				if n.Kind != '"' {
					return nil, errors.New("原文定位必须为文本")
				}
				return n, nil
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
			l.Qualifies = true
			start, end, e := streamjson.ResolveSelector(ctx, s.directory, body, prefix, exact, suffix)
			if e != nil {
				return e
			}
			l.StartRune, l.EndRune = start, end
		}
		return s.StageLink(ctx, p, l)
	}); e != nil {
		return e
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "INSERT INTO assets(id,revision,created,updated,kind,payload) VALUES(?,?,?,?,?,?)", m.ID, m.Revision, stamp(m.CreatedAt), stamp(m.UpdatedAt), m.Kind, p.ID); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO source_epochs VALUES(?,?)", m.ID, m.Revision); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "UPDATE payloads SET state='published' WHERE id=?", p.ID); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "UPDATE lexical_stats SET documents=documents+1,terms=terms+(SELECT length FROM lexical_documents WHERE payload=?) WHERE singleton=1", p.ID); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO semantic_jobs VALUES(?,?,'migrated')", m.ID, m.Revision); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "UPDATE migration_assets SET imported=1 WHERE id=?", m.ID)
		return e
	})
}

func (s *Store) validateMigration(ctx context.Context, id string) error {
	var integrity string
	if e := s.writer.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); e != nil {
		return e
	}
	if integrity != "ok" {
		return fmt.Errorf("迁移数据库校验失败: %s", integrity)
	}
	var pending int
	if e := s.writer.QueryRowContext(ctx, "SELECT count(*) FROM migration_assets WHERE deleted=0 AND imported=0").Scan(&pending); e != nil {
		return e
	}
	if pending != 0 {
		return errors.New("迁移尚未完成")
	}
	return nil
}
