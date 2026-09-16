package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

// ControlRecord 保存按身份读取的控制决定，不随启动加载全部授权和回执。
type ControlRecord struct {
	Key      string
	Revision uint64
	Payload  Staged
}

func (s *Store) PublishControl(ctx context.Context, record ControlRecord, expected uint64) error {
	if record.Key == "" || record.Revision == 0 || record.Revision != expected+1 {
		return errors.New("控制记录身份或修订无效")
	}
	return contract.Commit(ctx, func() error {
		return s.write(ctx, func(tx *sql.Tx) error {
			var state, operation string
			if err := tx.QueryRowContext(ctx, "SELECT state,operation FROM payloads WHERE id=?", record.Payload.ID).Scan(&state, &operation); err != nil {
				return err
			}
			if state != "ready" || operation != record.Payload.Operation {
				return errors.New("控制负载未就绪或操作不符")
			}
			var oldPayload string
			var current uint64
			err := tx.QueryRowContext(ctx, "SELECT revision,payload FROM control_records WHERE key=?", record.Key).Scan(&current, &oldPayload)
			if expected == 0 {
				if !errors.Is(err, sql.ErrNoRows) {
					return errors.New("控制记录已经存在")
				}
			} else {
				if err != nil {
					return err
				}
				if current != expected {
					return errors.New("控制状态已变化")
				}
				if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO reclaim_jobs(payload,reason) VALUES(?,'control_superseded')", oldPayload); err != nil {
					return err
				}
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO control_records VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET revision=excluded.revision,payload=excluded.payload", record.Key, record.Revision, record.Payload.ID); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "UPDATE payloads SET state='published' WHERE id=?", record.Payload.ID)
			return err
		})
	})
}

func (s *Store) OpenControl(ctx context.Context, key string) (uint64, io.ReadCloser, error) {
	done, err := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 2*ChunkBytes, true)
	if err != nil {
		return 0, nil, err
	}
	c, release, err := s.reader(ctx)
	if err != nil {
		done()
		return 0, nil, err
	}
	tx, err := c.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		release()
		done()
		return 0, nil, err
	}
	finish := func() { tx.Rollback(); release(); done() }
	var revision uint64
	var payload string
	err = tx.QueryRowContext(ctx, "SELECT revision,payload FROM control_records WHERE key=?", key).Scan(&revision, &payload)
	if err != nil {
		finish()
		return 0, nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT bytes FROM content_chunks WHERE payload=? AND part=0 ORDER BY ordinal", payload)
	if err != nil {
		finish()
		return 0, nil, err
	}
	return revision, &chunkReader{ctx: ctx, rows: rows, release: finish}, nil
}

func (s *Store) OperationGeneration(ctx context.Context) (uint64, error) {
	var generation uint64
	err := s.write(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM operation_receipts").Scan(&count); err != nil {
			return err
		}
		if count >= 4096 {
			if _, err := tx.ExecContext(ctx, "UPDATE store_meta SET value=value+1 WHERE key='operation_generation'"); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM operation_receipts"); err != nil {
				return err
			}
		}
		return tx.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='operation_generation'").Scan(&generation)
	})
	return generation, err
}
