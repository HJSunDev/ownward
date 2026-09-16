package boundedstore

import (
	"context"
	"database/sql"
	"errors"
)

// WorkSet 使用同一固定写连接的临时B树去重，可变长列表不成为Go内存集合。
// 临时表不承载权威状态；关闭连接即丢弃，正式资产仍通过Publish原子提交。
type WorkSet struct {
	store *Store
	id    string
}

func (s *Store) NewWorkSet(ctx context.Context) (*WorkSet, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	return &WorkSet{s, id}, nil
}
func (w *WorkSet) Add(ctx context.Context, key []byte) (bool, error) {
	if len(key) != 32 {
		return false, errors.New("去重摘要长度无效")
	}
	added := false
	err := w.store.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO temp.work_keys VALUES(?,?)", w.id, key)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		added = n == 1
		return err
	})
	return added, err
}
func (w *WorkSet) Close() error {
	return w.store.write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM temp.work_keys WHERE scope=?", w.id)
		return err
	})
}
