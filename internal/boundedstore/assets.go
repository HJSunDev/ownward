package boundedstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

var _ contract.BoundedAssets = (*Store)(nil)
var ErrNotFound = errors.New("信息不存在或版本已失效")

type AssetWrite struct {
	Meta             contract.AssetMeta
	Payload          Staged
	ExpectedRevision uint64
}

// Publish 将可见版本、后续组织工作、回收任务和回执一同提交。
// 权限层通过既有 CommitGuard 在短事务外保持授权与撤销的顺序。
func (s *Store) Publish(ctx context.Context, receipt contract.MutationReceipt, values []AssetWrite) error {
	if len(values) > 20 || len(receipt.Results) == 0 || len(receipt.Results) > 20 {
		return errors.New("批量变更数量无效")
	}
	if err := validOperation(receipt.Operation); err != nil {
		return err
	}
	if err := s.awaitReclaim(ctx); err != nil {
		return err
	}
	for _, v := range values {
		if err := s.prepareLexical(ctx, v.Payload, v.Meta.ID); err != nil {
			return err
		}
	}
	return contract.Commit(ctx, func() error {
		return s.write(ctx, func(tx *sql.Tx) error {
			if err := checkAccess(ctx, tx); err != nil {
				return err
			}
			_, found, err := lookupReceipt(ctx, tx, receipt.Operation)
			if err != nil || found {
				return err
			}
			var count int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM operation_receipts").Scan(&count); err != nil {
				return err
			}
			if count >= 4096 {
				return contract.ErrOperationExpired
			}
			seen := map[string]bool{}
			position := 0
			for _, outcome := range receipt.Results {
				if outcome.Error != "" {
					continue
				}
				if position >= len(values) || outcome.Asset.ID != values[position].Meta.ID || outcome.Asset.Revision != values[position].Meta.Revision {
					return errors.New("回执与提交资产不一致")
				}
				position++
			}
			if position != len(values) {
				return errors.New("回执缺失成功结果")
			}
			for _, v := range values {
				m := v.Meta
				if m.ID == "" || seen[m.ID] || m.Revision == 0 || m.CreatedAt.IsZero() || m.UpdatedAt.Before(m.CreatedAt) {
					return errors.New("资产身份或时间无效")
				}
				if _, err = domain.ParseKind(string(m.Kind)); err != nil {
					return err
				}
				seen[m.ID] = true
				var forgotten bool
				if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM forget_targets t JOIN forget_operations f ON f.id=t.operation WHERE t.asset=? AND f.state<>'staging')", m.ID).Scan(&forgotten); err != nil {
					return err
				}
				if forgotten {
					return ErrNotFound
				}

				var state, operation string
				var bytes int64
				var hash string
				if err = tx.QueryRowContext(ctx, "SELECT state,operation,content_bytes,digest FROM payloads WHERE id=?", v.Payload.ID).Scan(&state, &operation, &bytes, &hash); err != nil {
					return err
				}
				if state != "ready" || operation != v.Payload.Operation || operation != operationKey(receipt.Operation) {
					return errors.New("暂存不属于当前操作或尚未完成")
				}
				var invalid int
				if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM explicit_links l LEFT JOIN live_assets a ON a.id=l.target AND a.deleted=0 WHERE l.payload=? AND l.target<>? AND a.id IS NULL", v.Payload.ID, m.ID).Scan(&invalid); err != nil {
					return err
				}
				if invalid > 0 {
					return errors.New("明确关系的目标已不可用")
				}
				var rev uint64
				var created, oldPayload string
				var deleted bool
				err = tx.QueryRowContext(ctx, "SELECT revision,created,payload,deleted FROM live_assets WHERE id=?", m.ID).Scan(&rev, &created, &oldPayload, &deleted)
				if v.ExpectedRevision == 0 {
					if !errors.Is(err, sql.ErrNoRows) || m.Revision != 1 {
						return errors.New("新建资产已存在或版本无效")
					}
					if _, err = tx.ExecContext(ctx, "UPDATE lexical_stats SET documents=documents+1 WHERE singleton=1"); err != nil {
						return err
					}
				} else {
					if err != nil {
						return err
					}
					if deleted || rev != v.ExpectedRevision || m.Revision != rev+1 || created != stamp(m.CreatedAt) {
						return errors.New("信息版本已变化，不能覆盖")
					}
					if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO reclaim_jobs(payload,reason) VALUES(?,'superseded')", oldPayload); err != nil {
						return err
					}
					if _, err = tx.ExecContext(ctx, "UPDATE lexical_stats SET terms=terms-(SELECT length FROM lexical_documents WHERE payload=?) WHERE singleton=1", oldPayload); err != nil {
						return err
					}
				}
				if _, err = tx.ExecContext(ctx, "UPDATE lexical_stats SET terms=terms+(SELECT length FROM lexical_documents WHERE payload=?) WHERE singleton=1", v.Payload.ID); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO source_epochs(id,revision) SELECT id,1 FROM (SELECT ? AS id UNION SELECT target FROM explicit_links WHERE payload IN (?,?) AND qualifies=1) WHERE true ON CONFLICT(id) DO UPDATE SET revision=source_epochs.revision+1", m.ID, oldPayload, v.Payload.ID); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO assets(id,revision,created,updated,kind,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,updated=excluded.updated,kind=excluded.kind,payload=excluded.payload", m.ID, m.Revision, stamp(m.CreatedAt), stamp(m.UpdatedAt), m.Kind, v.Payload.ID); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "UPDATE payloads SET state='published' WHERE id=?", v.Payload.ID); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO semantic_jobs VALUES(?,?,'asset_changed') ON CONFLICT(asset) DO UPDATE SET revision=excluded.revision,reason=excluded.reason", m.ID, m.Revision); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO invalidation_jobs(asset,revision,snapshot,forget) VALUES(?,?,'',0)`, m.ID, m.Revision); err != nil {
					return err
				}
			}
			if _, err = tx.ExecContext(ctx, "UPDATE store_meta SET value=value+1 WHERE key='asset_epoch'"); err != nil {
				return err
			}
			encoded, err := json.Marshal(receipt.Results)
			if err != nil {
				return err
			}
			op := receipt.Operation
			_, err = tx.ExecContext(ctx, "INSERT INTO operation_receipts VALUES(?,?,?,?,?,?,?)", op.System, op.Principal, op.ID, op.Generation, op.Kind, op.Digest, encoded)
			return err
		})
	})
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func validOperation(op contract.OperationIdentity) error {
	if op.ID == "" || op.System == "" || op.Principal == "" || op.Kind == "" || op.Digest == "" || op.Generation == 0 {
		return errors.New("操作身份不完整")
	}
	return nil
}
func operationKey(op contract.OperationIdentity) string {
	b, _ := json.Marshal([]string{op.System, op.Principal, op.ID})
	return string(b)
}
func OperationKey(op contract.OperationIdentity) (string, error) {
	if err := validOperation(op); err != nil {
		return "", err
	}
	return operationKey(op), nil
}

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func lookupReceipt(ctx context.Context, q querier, op contract.OperationIdentity) (contract.MutationReceipt, bool, error) {
	r := contract.MutationReceipt{Operation: op}
	var data []byte
	err := q.QueryRowContext(ctx, "SELECT generation,kind,digest,results FROM operation_receipts WHERE system=? AND principal=? AND id=?", op.System, op.Principal, op.ID).Scan(&r.Operation.Generation, &r.Operation.Kind, &r.Operation.Digest, &data)
	if err == nil {
		if r.Operation != op {
			return r, false, errors.New("同一操作不能更换内容")
		}
		err = json.Unmarshal(data, &r.Results)
		return r, err == nil, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return r, false, err
	}
	var generation uint64
	if err = q.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='operation_generation'").Scan(&generation); err != nil {
		return r, false, err
	}
	if generation != op.Generation {
		return r, false, contract.ErrOperationExpired
	}
	return r, false, nil
}
func (s *Store) MutationReceipt(ctx context.Context, op contract.OperationIdentity) (contract.MutationReceipt, bool, error) {
	if err := validOperation(op); err != nil {
		return contract.MutationReceipt{}, false, err
	}
	c, release, err := s.reader(ctx)
	if err != nil {
		return contract.MutationReceipt{}, false, err
	}
	defer release()
	return lookupReceipt(ctx, c, op)
}

const assetColumns = "a.id,a.revision,a.created,a.updated,a.kind,p.content_bytes,p.digest"

type scanner interface{ Scan(...any) error }

func scanMeta(row scanner) (contract.AssetMeta, error) {
	var m contract.AssetMeta
	var created, updated string
	err := row.Scan(&m.ID, &m.Revision, &created, &updated, &m.Kind, &m.ContentBytes, &m.ContentSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	m.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return m, err
	}
	m.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return m, err
}
func (s *Store) ReadAssetMeta(ctx context.Context, id string, revision uint64) (contract.AssetMeta, error) {
	c, release, err := s.snapshotReader(ctx)
	if err != nil {
		return contract.AssetMeta{}, err
	}
	defer release()
	return scanMeta(c.QueryRowContext(ctx, "SELECT "+assetColumns+" FROM live_assets a JOIN payloads p ON p.id=a.payload WHERE a.id=? AND a.deleted=0 AND (?=0 OR a.revision=?)", id, revision, revision))
}

func (s *Store) ScanAssets(ctx context.Context, after string, pageBudget int) (contract.AssetPage, error) {
	var out contract.AssetPage
	if pageBudget < 1024 || pageBudget > 1024*1024 {
		return out, errors.New("页面预算必须在1KiB至1MiB之间")
	}
	done, err := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, int64(pageBudget), false)
	if err != nil {
		return out, err
	}
	defer done()
	c, release, err := s.reader(ctx)
	if err != nil {
		return out, err
	}
	defer release()
	tx, err := c.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var epoch uint64
	if err = tx.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='asset_epoch'").Scan(&epoch); err != nil {
		return out, err
	}
	cursor := assetCursor{Epoch: epoch, Principal: contract.AuthenticationDigest(ctx)}
	if after != "" {
		if len(after) > 1400 {
			return out, errors.New("列表游标无效")
		}
		encoded, e := base64.RawURLEncoding.DecodeString(after)
		if e != nil || len(encoded) > 1024 || json.Unmarshal(encoded, &cursor) != nil || cursor.Epoch != epoch || cursor.Principal != contract.AuthenticationDigest(ctx) {
			return out, errors.New("列表游标已失效，请重新读取")
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+assetColumns+" FROM live_assets a JOIN payloads p ON p.id=a.payload WHERE a.id>? AND a.deleted=0 ORDER BY a.id", cursor.Last)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	used := 0
	for rows.Next() {
		m, e := scanMeta(rows)
		if e != nil {
			return out, e
		}
		size := len(m.ID) + len(m.ContentSHA256) + len(m.Kind) + 192
		if used+size > pageBudget {
			if len(out.Items) == 0 {
				return out, errors.New("单项元数据超过页面预算")
			}
			cursor.Last = out.Items[len(out.Items)-1].ID
			encoded, _ := json.Marshal(cursor)
			out.Next = base64.RawURLEncoding.EncodeToString(encoded)
			break
		}
		out.Items = append(out.Items, m)
		used += size
	}
	return out, rows.Err()
}

type assetCursor struct {
	Last      string
	Epoch     uint64
	Principal string
}

type chunkReader struct {
	ctx     context.Context
	rows    *sql.Rows
	current []byte
	release func()
	closed  bool
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.closed {
		return 0, io.EOF
	}
	if err := r.ctx.Err(); err != nil {
		r.Close()
		return 0, err
	}
	for len(r.current) == 0 {
		if !r.rows.Next() {
			err := r.rows.Err()
			r.Close()
			if err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		if err := r.rows.Scan(&r.current); err != nil {
			r.Close()
			return 0, err
		}
	}
	n := copy(p, r.current)
	r.current = r.current[n:]
	return n, nil
}
func (r *chunkReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.current = nil
	err := r.rows.Close()
	r.release()
	return err
}

func (s *Store) openPart(ctx context.Context, id string, revision uint64, part int) (io.ReadCloser, error) {
	budgetDone, err := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 2*ChunkBytes, false)
	if err != nil {
		return nil, err
	}
	tx, release, err := s.snapshotReader(ctx)
	if err != nil {
		budgetDone()
		return nil, err
	}
	finish := func() { release(); budgetDone() }
	var payload string
	err = tx.QueryRowContext(ctx, "SELECT payload FROM live_assets WHERE id=? AND deleted=0 AND (?=0 OR revision=?)", id, revision, revision).Scan(&payload)
	if err != nil {
		finish()
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT bytes FROM content_chunks WHERE payload=? AND part=? ORDER BY ordinal", payload, part)
	if err != nil {
		finish()
		return nil, err
	}
	return &chunkReader{ctx: ctx, rows: rows, release: finish}, nil
}
func (s *Store) OpenContent(ctx context.Context, id string, revision uint64) (io.ReadCloser, error) {
	return s.openPart(ctx, id, revision, 0)
}
func (s *Store) OpenDetails(ctx context.Context, id string, revision uint64) (io.ReadCloser, error) {
	return s.openPart(ctx, id, revision, 1)
}
func (s *Store) ReadRanges(ctx context.Context, id string, revision uint64, ranges []contract.ContentRange, w io.Writer) error {
	budgetDone, err := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 2*ChunkBytes, false)
	if err != nil {
		return err
	}
	defer budgetDone()
	tx, release, err := s.snapshotReader(ctx)
	if err != nil {
		return err
	}
	defer release()
	var payload string
	var length int64
	if err = tx.QueryRowContext(ctx, "SELECT a.payload,p.content_bytes FROM live_assets a JOIN payloads p ON p.id=a.payload WHERE a.id=? AND a.deleted=0 AND (?=0 OR a.revision=?)", id, revision, revision).Scan(&payload, &length); err != nil {
		return err
	}
	var end int64
	for _, span := range ranges {
		if span.Offset < end || span.Length < 0 || span.Offset > length || span.Length > length-span.Offset {
			return errors.New("原文区间无效或顺序错误")
		}
		end = span.Offset + span.Length
	}
	for _, span := range ranges {
		for offset, remaining := span.Offset, span.Length; remaining > 0; {
			n := min(remaining, int64(ChunkBytes)-offset%int64(ChunkBytes))
			var block []byte
			if err = tx.QueryRowContext(ctx, "SELECT substr(bytes,?,?) FROM content_chunks WHERE payload=? AND part=0 AND ordinal=?", offset%int64(ChunkBytes)+1, n, payload, offset/int64(ChunkBytes)).Scan(&block); err != nil {
				return err
			}
			if int64(len(block)) != n {
				return io.ErrUnexpectedEOF
			}
			written, e := w.Write(block)
			if e != nil {
				return e
			}
			if written != len(block) {
				return io.ErrShortWrite
			}
			offset += n
			remaining -= n
		}
	}
	return nil
}
