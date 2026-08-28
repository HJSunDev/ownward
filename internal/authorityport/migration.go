//go:build ownward_migration

package authorityport

import "github.com/HJSunDev/ownward/internal/domain"

func (c *Current) CaptureCurrentForMigration() []domain.Information {
	if c == nil || c.store == nil {
		return nil
	}
	return c.store.CaptureCurrentForMigration()
}
