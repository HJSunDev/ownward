//go:build ownward_migration

package assetlog

import (
	"sort"

	"github.com/HJSunDev/ownward/internal/domain"
)

// CaptureCurrentForMigration holds the write mutex only while copying the
// current immutable value headers. Large strings are not copied under the
// lock; sorting and defensive slice cloning happen after release. Store writes
// replace values rather than mutating their referenced slices in place.
func (s *Store) CaptureCurrentForMigration() []domain.Information {
	s.mu.RLock()
	values := make([]domain.Information, 0, len(s.items))
	for _, value := range s.items {
		values = append(values, value)
	}
	s.mu.RUnlock()
	for index := range values {
		values[index] = clone(values[index])
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values
}
