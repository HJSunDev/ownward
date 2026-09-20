package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
)

// Execution metadata extends semantic_jobs; it is not a second work queue.
const organizationJobsSchema = `
CREATE TABLE IF NOT EXISTS organization_execution(
 asset TEXT PRIMARY KEY REFERENCES assets(id) ON DELETE CASCADE,
 request_id TEXT NOT NULL DEFAULT '', token TEXT NOT NULL DEFAULT '', system TEXT NOT NULL DEFAULT '', principal TEXT NOT NULL DEFAULT '',
 principal_revision INTEGER NOT NULL DEFAULT 0, deletion_epoch INTEGER NOT NULL DEFAULT 0,
 revision INTEGER NOT NULL DEFAULT 0, generation TEXT NOT NULL DEFAULT '', expires INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS organization_execution_attempt ON organization_execution(system,principal,request_id);
`

var ErrOrganizationLease = errors.New("组织工作需有效执行凭据；请重新领取或接续当前持有者")

type organizationExecution struct{ asset, token string }
type organizationExecutionKey struct{}

func WithOrganizationExecution(ctx context.Context, asset, token string) context.Context {
	return context.WithValue(ctx, organizationExecutionKey{}, organizationExecution{asset, token})
}

func EnsureOrganizationExecution(ctx context.Context, asset string) context.Context {
	if _, ok := ctx.Value(organizationExecutionKey{}).(organizationExecution); ok {
		return ctx
	}
	return WithOrganizationExecution(ctx, asset, "")
}

func checkOrganizationExecution(ctx context.Context, q querier, asset string) error {
	return checkOrganizationCredential(ctx, q, asset, false)
}

func checkOrganizationCredential(ctx context.Context, q querier, asset string, completedReplay bool) error {
	guard, guarded := ctx.Value(organizationExecutionKey{}).(organizationExecution)
	if !guarded {
		return nil
	} // Internal rebuilds are not external execution attempts.
	if guard.asset != asset {
		return ErrOrganizationLease
	}
	var token, system, principal, generation string
	var rev, principalRev, epoch uint64
	var expires int64
	e := q.QueryRowContext(ctx, `SELECT token,system,principal,principal_revision,deletion_epoch,revision,generation,expires FROM organization_execution WHERE asset=?`, asset).Scan(&token, &system, &principal, &principalRev, &epoch, &rev, &generation, &expires)
	if errors.Is(e, sql.ErrNoRows) {
		if guard.token != "" {
			return ErrOrganizationLease
		}
		return nil
	}
	if e != nil {
		return e
	}
	a, ok := ctx.Value(accessKey{}).(accessLease)
	if !ok || guard.token == "" || token != guard.token || system != a.system || principal != a.principal || principalRev != a.revision || epoch != a.epoch {
		return ErrOrganizationLease
	}
	if expires <= time.Now().UnixMilli() {
		if !completedReplay || expires != 0 {
			return ErrOrganizationLease
		}
		var pending bool
		if e = q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM semantic_jobs WHERE asset=?)", asset).Scan(&pending); e != nil {
			return e
		}
		if pending {
			return ErrOrganizationLease
		}
	}
	var currentRev uint64
	var currentGeneration string
	if e = q.QueryRowContext(ctx, `SELECT a.revision,d.generation FROM live_assets a CROSS JOIN derived_state d WHERE a.id=? AND a.deleted=0 AND d.singleton=1`, asset).Scan(&currentRev, &currentGeneration); e != nil {
		return ErrOrganizationLease
	}
	if rev != currentRev || generation != currentGeneration {
		return ErrOrganizationLease
	}
	return checkAccess(ctx, q)
}

func (s *Store) CheckOrganizationExecution(ctx context.Context, asset string) error {
	return s.view(ctx, func(q queryer) error { return checkOrganizationExecution(ctx, q, asset) })
}

// 完成凭据只允许核对同一已接受结果，不再授予执行、续期或发布资格。
func (s *Store) CheckOrganizationReplay(ctx context.Context, asset string) error {
	return s.view(ctx, func(q queryer) error { return checkOrganizationCredential(ctx, q, asset, true) })
}

// Legacy work cannot consume jobs opted into leased execution, even while unclaimed.
func (s *Store) DeferredOrganization(ctx context.Context, asset string) (bool, error) {
	var n int
	e := s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, `SELECT count(*) FROM organization_execution WHERE asset=?`, asset).Scan(&n)
	})
	return n != 0, e
}

const availableOrganization = ` FROM semantic_jobs j JOIN live_assets a ON a.id=j.asset AND a.revision=j.revision AND a.deleted=0
 JOIN organization_execution x ON x.asset=j.asset CROSS JOIN derived_state d
 WHERE d.singleton=1 AND (?='' OR j.asset=?) AND
 (x.token='' OR x.expires<=? OR x.revision<>a.revision OR x.generation<>d.generation
 OR NOT EXISTS(SELECT 1 FROM access_principals p JOIN access_header h ON h.singleton=1 WHERE p.id=x.principal AND p.revision=x.principal_revision AND (p.permissions&2)=2 AND h.system=x.system AND h.deletion_epoch=x.deletion_epoch))`

func (s *Store) ClaimOrganization(ctx context.Context, asset, requestID string, seconds int) (*contract.OrganizationLease, error) {
	if seconds == 0 {
		seconds = 300
	}
	if seconds < 1 || seconds > 600 || requestID == "" || len(requestID) > 128 {
		return nil, errors.New("执行凭据期限必须介于1和600秒")
	}
	a, ok := ctx.Value(accessKey{}).(accessLease)
	if !ok || a.permission&2 == 0 {
		return nil, ErrAccess
	}
	var out *contract.OrganizationLease
	e := s.write(ctx, func(tx *sql.Tx) error {
		if e := checkAccess(ctx, tx); e != nil {
			return e
		}
		v := &contract.OrganizationLease{}
		var expires int64
		e := tx.QueryRowContext(ctx, `SELECT x.asset,x.revision,x.generation,x.token,x.expires FROM organization_execution x JOIN semantic_jobs j ON j.asset=x.asset JOIN live_assets a ON a.id=x.asset AND a.revision=x.revision CROSS JOIN derived_state d WHERE x.system=? AND x.principal=? AND x.principal_revision=? AND x.deletion_epoch=? AND x.request_id=? AND x.expires>? AND x.token<>'' AND x.generation=d.generation AND (?='' OR x.asset=?) LIMIT 1`, a.system, a.principal, a.revision, a.epoch, requestID, time.Now().UnixMilli(), asset, asset).Scan(&v.AssetID, &v.Revision, &v.Generation, &v.Lease, &expires)
		if e == nil {
			v.ExpiresAt = time.UnixMilli(expires).UTC()
			out = v
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		e = tx.QueryRowContext(ctx, `SELECT j.asset,a.revision,d.generation`+availableOrganization+` ORDER BY a.updated,j.asset LIMIT 1`, asset, asset, time.Now().UnixMilli()).Scan(&v.AssetID, &v.Revision, &v.Generation)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		v.Lease, e = newID()
		if e != nil {
			return e
		}
		v.ExpiresAt = time.Now().UTC().Add(time.Duration(seconds) * time.Second)
		_, e = tx.ExecContext(ctx, `UPDATE organization_execution SET request_id=?,token=?,system=?,principal=?,principal_revision=?,deletion_epoch=?,revision=?,generation=?,expires=? WHERE asset=?`, requestID, v.Lease, a.system, a.principal, a.revision, a.epoch, v.Revision, v.Generation, v.ExpiresAt.UnixMilli(), v.AssetID)
		if e == nil {
			out = v
		}
		return e
	})
	return out, e
}

func (s *Store) ChangeOrganizationLease(ctx context.Context, asset, token string, seconds int, release bool) (*contract.OrganizationLease, error) {
	if seconds == 0 {
		seconds = 300
	}
	if seconds < 1 || seconds > 600 || token == "" || asset == "" {
		return nil, ErrOrganizationLease
	}
	ctx = WithOrganizationExecution(ctx, asset, token)
	var out *contract.OrganizationLease
	e := s.write(ctx, func(tx *sql.Tx) error {
		if e := checkOrganizationExecution(ctx, tx, asset); e != nil {
			return e
		}
		if release {
			_, e := tx.ExecContext(ctx, `UPDATE organization_execution SET token='',expires=0 WHERE asset=?`, asset)
			return e
		}
		v := &contract.OrganizationLease{AssetID: asset, Lease: token, ExpiresAt: time.Now().UTC().Add(time.Duration(seconds) * time.Second)}
		if e := tx.QueryRowContext(ctx, `SELECT revision,generation FROM organization_execution WHERE asset=?`, asset).Scan(&v.Revision, &v.Generation); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `UPDATE organization_execution SET expires=? WHERE asset=?`, v.ExpiresAt.UnixMilli(), asset)
		if e == nil {
			out = v
		}
		return e
	})
	return out, e
}

func (s *Store) signalOrganizationJobs() {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	if s.jobsWaiters == 0 {
		return
	}
	if s.jobsChanged != nil {
		close(s.jobsChanged)
	}
	s.jobsChanged = make(chan struct{})
}

// Register before checking. No database snapshot, work permit or delivery lock
// is held while waiting; expiry is also a wakeup even without another write.
func (s *Store) WaitOrganization(ctx context.Context, asset string, seconds int) (bool, error) {
	if seconds < 0 || seconds > 30 {
		return false, errors.New("等待期限必须介于0和30秒")
	}
	s.jobsMu.Lock()
	s.jobsWaiters++
	s.jobsMu.Unlock()
	defer func() { s.jobsMu.Lock(); s.jobsWaiters--; s.jobsMu.Unlock() }()
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		s.jobsMu.Lock()
		changed := s.jobsChanged
		s.jobsMu.Unlock()
		var available bool
		var expiry sql.NullInt64
		e := s.view(ctx, func(q queryer) error {
			if e := checkAccess(ctx, q); e != nil {
				return e
			}
			if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+availableOrganization+` LIMIT 1)`, asset, asset, time.Now().UnixMilli()).Scan(&available); e != nil {
				return e
			}
			return q.QueryRowContext(ctx, `SELECT min(x.expires) FROM organization_execution x JOIN semantic_jobs j ON j.asset=x.asset WHERE x.expires>? AND (?='' OR x.asset=?)`, time.Now().UnixMilli(), asset, asset).Scan(&expiry)
		})
		if e != nil || available || !time.Now().Before(deadline) {
			return available, e
		}
		until := deadline
		if expiry.Valid && time.UnixMilli(expiry.Int64).Before(until) {
			until = time.UnixMilli(expiry.Int64)
		}
		timer := time.NewTimer(time.Until(until))
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}
