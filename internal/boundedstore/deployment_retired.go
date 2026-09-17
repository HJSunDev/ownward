package boundedstore

import (
	"context"
	"errors"
	"github.com/HJSunDev/ownward/internal/contract"
)

// CleanRetiredDeployment removes the transferred content through the existing
// bounded forget machinery, retaining the authority and old-binary barrier.
func CleanRetiredDeployment(ctx context.Context, root string, options Options) error {
	s, e := OpenDeployment(ctx, root, DeploymentOptions{Options: options})
	if e != nil {
		return e
	}
	defer s.Close()
	state, e := (&ControlAuthority{s}).ReadSelectedControl(contract.ControlSelection{})
	if e != nil {
		return e
	}
	if state.Access == nil || state.Access.Handoff == nil || state.Access.Handoff.Phase != "retired" {
		return errors.New("只能清理已退役部署")
	}
	for {
		var targets []ForgetTarget
		e = s.view(ctx, func(q queryer) error {
			rows, err := q.QueryContext(ctx, "SELECT id,revision FROM live_assets WHERE deleted=0 ORDER BY id LIMIT 64")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var t ForgetTarget
				if err = rows.Scan(&t.ID, &t.Revision); err != nil {
					return err
				}
				targets = append(targets, t)
			}
			return rows.Err()
		})
		if e != nil {
			return e
		}
		if len(targets) == 0 {
			return s.DrainMaintenance(ctx)
		}
		op := "retired:" + state.Access.Handoff.ID + ":" + targets[0].ID
		if e = s.StageForget(ctx, op, targets); e != nil {
			return e
		}
		if e = s.CommitForget(ctx, op); e != nil {
			return e
		}
		if e = s.DrainMaintenance(ctx); e != nil {
			return e
		}
	}
}
