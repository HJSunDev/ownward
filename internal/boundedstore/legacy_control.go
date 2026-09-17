package boundedstore

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

// compactDigest preserves JSON field order and escaping while removing only
// insignificant whitespace, matching the legacy writer's compact state hash.
func compactDigest(r io.Reader) (string, error) {
	h := sha256.New()
	if e := compactJSONTo(h, r); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func compactJSONTo(out io.Writer, r io.Reader) error {
	w := bufio.NewWriterSize(out, ChunkBytes)
	b := bufio.NewReaderSize(r, ChunkBytes)
	quoted, escaped := false, false
	for {
		c, e := b.ReadByte()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if quoted {
			if e = w.WriteByte(c); e != nil {
				return e
			}
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
			continue
		}
		if c == '"' {
			quoted = true
		}
		if e = w.WriteByte(c); e != nil {
			return e
		}
	}
	return w.Flush()
}

func (s *Store) importLegacyControl(ctx context.Context, root string, sources map[string]string) error {
	if sources["authority/control.json"] == "absent" {
		return nil
	}
	var exists bool
	if e := s.writer.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='authority_header')").Scan(&exists); e != nil {
		return e
	}
	if exists {
		if e := s.writer.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM authority_header)").Scan(&exists); e != nil {
			return e
		}
		if exists {
			return nil
		}
	}
	if e := s.writer.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM control_records WHERE key='authority')").Scan(&exists); e != nil {
		return e
	}
	if exists {
		return nil
	}
	f, e := os.Open(filepath.Join(root, "authority", "control.json"))
	if e != nil {
		return e
	}
	defer f.Close()
	doc, e := streamjson.Parse(ctx, s.directory, f, s.budget, 256*resourcebudget.MiB)
	if e != nil {
		return e
	}
	defer doc.Close()
	schema, e := nodeString(doc.Root(), "schema")
	if e != nil || schema != "ownward.control-state-envelope/v1" {
		return errors.New("旧控制状态格式无效")
	}
	state, ok, e := doc.Root().Field("state")
	if e != nil || !ok {
		return errors.New("旧控制状态缺失")
	}
	want, e := nodeString(doc.Root(), "sha256")
	if e != nil {
		return e
	}
	actual, e := compactDigest(state.Raw())
	if e != nil {
		return e
	}
	if actual != want {
		return errors.New("旧控制状态摘要不匹配")
	}
	var revision uint64
	if e = nodeDecode(state, "revision", &revision); e != nil || revision == 0 {
		return errors.New("旧控制状态修订无效")
	}
	p, e := s.Stage(ctx, "legacy-control", streamjson.RawSource{Node: state}, nil)
	if e != nil {
		return e
	}
	defer s.Abandon(context.WithoutCancel(ctx), p)
	// Principal and deletion projections are completed while the gate is closed.
	// Their authority remains the exactly preserved control state.
	info, hasInfo, e := state.Field("information_control")
	if e != nil {
		return e
	}
	if hasInfo && info.Kind != 'n' {
		system, e := nodeString(info, "system_id")
		if e != nil || system == "" {
			return errors.New("旧控制体系身份缺失")
		}
		var epoch uint64
		if e = nodeDecode(info, "deletion_revision", &epoch); e != nil {
			return e
		}
		access, _, e := state.Field("access")
		if e != nil {
			return e
		}
		phase := ""
		if access.Kind == '{' {
			handoff, _, e := access.Field("handoff")
			if e != nil {
				return e
			}
			if handoff.Kind == '{' {
				phase, e = nodeString(handoff, "phase")
				if e != nil {
					return e
				}
			}
		}
		stopping := false
		if e = visitArray(info, "operations", func(n streamjson.Node) error {
			status, e := nodeString(n, "status")
			if e != nil {
				return e
			}
			if status == "stopping" {
				stopping = true
			}
			request, _, e := n.Field("request")
			if e != nil {
				return e
			}
			kind, e := nodeString(request, "operation")
			if e != nil {
				return e
			}
			if kind != "forget" || (status != "cleaning" && status != "completed" && status != "stopping") {
				return nil
			}
			id, e := nodeString(request, "id")
			if e != nil || id == "" {
				return errors.New("旧遗忘决定缺少身份")
			}
			state := "cleaning"
			if status == "completed" {
				state = "complete"
			}
			if e = s.write(ctx, func(tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO forget_operations(id,principal,digest,state) VALUES(?,'migration','',?)", id, state)
				return e
			}); e != nil {
				return e
			}
			// Only explicitly forgotten originals become tombstones. Affected also
			// contains dependent originals whose derived data must be invalidated.
			return visitArray(request, "targets", func(v streamjson.Node) error {
				var target contract.AssetVersion
				if e := v.DecodeSmall(&target, 65536); e != nil {
					return e
				}
				return s.write(ctx, func(tx *sql.Tx) error {
					_, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO forget_targets VALUES(?,?,?)", id, target.ID, target.Revision)
					return e
				})
			})
		}); e != nil {
			return e
		}
		if e = visitArray(info, "principals", func(n streamjson.Node) error {
			var v contract.Principal
			if e := n.DecodeSmall(&v, 65536); e != nil {
				return e
			}
			if v.ID == "" || v.Revision == 0 {
				return errors.New("旧主体身份无效")
			}
			return s.write(ctx, func(tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, "INSERT INTO access_principals VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,credential=excluded.credential,permissions=excluded.permissions", v.ID, v.Revision, storageCredential(v.ID, v.CredentialDigest), permissionMask(v.Permissions))
				return e
			})
		}); e != nil {
			return e
		}
		if e = s.write(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, "INSERT OR REPLACE INTO access_header VALUES(1,?,?,?,?,?,?)", system, revision, epoch, phase == "frozen", phase == "retired", stopping)
			return e
		}); e != nil {
			return e
		}
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "INSERT INTO control_records VALUES('authority',?,?)", revision, p.ID); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "UPDATE payloads SET state='published' WHERE id=?", p.ID)
		return e
	})
}
