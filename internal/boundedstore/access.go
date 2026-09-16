package boundedstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/HJSunDev/ownward/internal/contract"
)

// AccessHeader 与主体记录分开定向读取，不将全部授权列表加载到运行内存。
type AccessHeader struct {
	System                    string
	Revision, DeletionEpoch   uint64
	Frozen, Retired, Stopping bool
}

func permissionMask(values []contract.Permission) int {
	mask := 0
	for _, value := range values {
		switch value {
		case contract.ReadPermission:
			mask |= 1
		case contract.MaintainPermission:
			mask |= 2
		case contract.ManagePermission:
			mask |= 4
		}
	}
	return mask
}

// PublishAccess 接收控制层已经决定的变更，和控制修订同时生效；不作为外部工具。
func (s *Store) PublishAccess(ctx context.Context, h AccessHeader, expected uint64, changed []contract.Principal) error {
	if h.System == "" || h.Revision != expected+1 || len(changed) > 64 {
		return errors.New("控制修订或变更批次无效")
	}
	return contract.Commit(ctx, func() error {
		return s.write(ctx, func(tx *sql.Tx) error {
			var revision uint64
			var system string
			err := tx.QueryRowContext(ctx, "SELECT system,revision FROM access_header WHERE singleton=1").Scan(&system, &revision)
			if expected == 0 {
				if !errors.Is(err, sql.ErrNoRows) {
					return errors.New("信息体系已经初始化")
				}
			} else if err != nil || revision != expected || system != h.System {
				return errors.New("控制状态已变化")
			}
			for _, p := range changed {
				if p.ID == "" || p.Revision == 0 || len(p.CredentialDigest) != 64 {
					return errors.New("主体身份无效")
				}
				var before uint64
				err = tx.QueryRowContext(ctx, "SELECT revision FROM access_principals WHERE id=?", p.ID).Scan(&before)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				if p.Revision != before+1 {
					return errors.New("主体修订必须连续")
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO access_principals VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,credential=excluded.credential,permissions=excluded.permissions", p.ID, p.Revision, p.CredentialDigest, permissionMask(p.Permissions)); err != nil {
					return err
				}
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO access_header VALUES(1,?,?,?,?,?,?) ON CONFLICT(singleton) DO UPDATE SET revision=excluded.revision,deletion_epoch=excluded.deletion_epoch,frozen=excluded.frozen,retired=excluded.retired,stopping=excluded.stopping", h.System, h.Revision, h.DeletionEpoch, h.Frozen, h.Retired, h.Stopping)
			return err
		})
	})
}

type accessLease struct {
	system, principal, credential string
	revision, epoch               uint64
	permission                    int
}
type accessKey struct{}

var ErrAccess = errors.New("该连接未获准执行此操作，或信息体系状态已经变化")

// BeginAccess 使用可信连接层提供的凭据摘要。操作参数不能指定认证主体。
func (s *Store) BeginAccess(ctx context.Context, credential string, permission contract.Permission) (context.Context, error) {
	if len(credential) != 64 {
		return nil, ErrAccess
	}
	c, done, err := s.reader(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	lease := accessLease{credential: credential, permission: permissionMask([]contract.Permission{permission})}
	var permissions int
	var frozen, retired, stopping bool
	err = c.QueryRowContext(ctx, "SELECT h.system,h.deletion_epoch,h.frozen,h.retired,h.stopping,p.id,p.revision,p.permissions FROM access_header h JOIN access_principals p ON p.credential=? WHERE h.singleton=1", credential).Scan(&lease.system, &lease.epoch, &frozen, &retired, &stopping, &lease.principal, &lease.revision, &permissions)
	if err != nil {
		return nil, ErrAccess
	}
	if retired || stopping || lease.permission == 0 || permissions&lease.permission != lease.permission || (frozen && permission == contract.MaintainPermission) {
		return nil, ErrAccess
	}
	if op, ok := contract.Operation(ctx); ok {
		op.System = lease.system
		op.Principal = lease.principal
		ctx = contract.WithOperation(ctx, op)
	}
	ctx = contract.WithInformationSystem(ctx, lease.system)
	return context.WithValue(ctx, accessKey{}, lease), nil
}

func checkAccess(ctx context.Context, q querier) error {
	lease, ok := ctx.Value(accessKey{}).(accessLease)
	if !ok {
		return nil
	}
	var revision, epoch uint64
	var permissions int
	var frozen, retired, stopping bool
	err := q.QueryRowContext(ctx, "SELECT p.revision,p.permissions,h.deletion_epoch,h.frozen,h.retired,h.stopping FROM access_header h JOIN access_principals p ON p.id=? AND p.credential=? WHERE h.singleton=1 AND h.system=?", lease.principal, lease.credential, lease.system).Scan(&revision, &permissions, &epoch, &frozen, &retired, &stopping)
	if err != nil || revision != lease.revision || epoch != lease.epoch || retired || stopping || (frozen && lease.permission&2 != 0) || permissions&lease.permission != lease.permission {
		return ErrAccess
	}
	return nil
}
func (s *Store) CheckAccess(ctx context.Context) error {
	c, done, err := s.reader(ctx)
	if err != nil {
		return err
	}
	defer done()
	return checkAccess(ctx, c)
}

// AuthorizeDelivery 将开始交付与控制／资产发布排序；完成后不持事务等待网络。
func (s *Store) AuthorizeDelivery(ctx context.Context, sources map[string]uint64) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errors.New("存储已关闭")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var c *sql.Conn
	select {
	case c = <-s.readers:
		defer func() { s.readers <- c }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := checkAccess(ctx, c); err != nil {
		return err
	}
	for id, before := range sources {
		var after uint64
		err := c.QueryRowContext(ctx, "SELECT revision FROM source_epochs WHERE id=?", id).Scan(&after)
		if err != nil {
			return err
		}
		if after != before {
			return errors.New("材料生成后资料已变化，请按原操作接续")
		}
	}
	return ctx.Err()
}
