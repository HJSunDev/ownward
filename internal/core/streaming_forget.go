package core

import (
	"context"
	"errors"

	"github.com/HJSunDev/ownward/internal/contract"
)

func (s *StreamingAssets) StopUsing(targets, recovered []contract.AssetVersion, operation string, persist func([]contract.AssetVersion) error) error {
	// The SQLite control authority checks target versions and publishes the
	// stop-use barrier in the same transaction as persist's control decision.
	if len(targets) == 0 || operation == "" || persist == nil {
		return errors.New("遗忘决定不完整")
	}
	return persist(targets)
}

func (s *StreamingAssets) CleanForgotten() error {
	return s.Store.DrainMaintenance(context.Background())
}
