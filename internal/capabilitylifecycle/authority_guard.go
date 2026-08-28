//go:build ownward_migration

package capabilitylifecycle

import (
	"errors"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

// ActiveAuthority prevents a retained rollback source from becoming a second
// writer. It is used only by the migration/candidate runtime; the normal
// product request path remains unchanged.
type ActiveAuthority struct {
	Assets      contract.AssetAuthority
	Control     contract.ControlAuthority
	Composition string
}

var _ contract.AssetAuthority = (*ActiveAuthority)(nil)

func (guard *ActiveAuthority) writable() error {
	if guard == nil || guard.Assets == nil || guard.Control == nil || guard.Control.ReadControl().ActiveComposition != guard.Composition {
		return errors.New("该权威持久化实现不是唯一活动写入方")
	}
	return nil
}

func (guard *ActiveAuthority) CreateAsset(value domain.Information) (contract.ChangeScope, error) {
	if err := guard.writable(); err != nil {
		return contract.ChangeScope{}, err
	}
	return guard.Assets.CreateAsset(value)
}

func (guard *ActiveAuthority) CreateAssets(values []domain.Information) (contract.ChangeScope, error) {
	if err := guard.writable(); err != nil {
		return contract.ChangeScope{}, err
	}
	return guard.Assets.CreateAssets(values)
}

func (guard *ActiveAuthority) UpdateAsset(value domain.Information, revision uint64) (contract.ChangeScope, error) {
	if err := guard.writable(); err != nil {
		return contract.ChangeScope{}, err
	}
	return guard.Assets.UpdateAsset(value, revision)
}

func (guard *ActiveAuthority) ReadCurrent(id string) (domain.Information, bool) {
	return guard.Assets.ReadCurrent(id)
}
func (guard *ActiveAuthority) ReadVersion(id string, revision uint64) (domain.Information, bool) {
	return guard.Assets.ReadVersion(id, revision)
}
func (guard *ActiveAuthority) ListCurrent() []domain.Information { return guard.Assets.ListCurrent() }
func (guard *ActiveAuthority) Sync() error {
	if err := guard.writable(); err != nil {
		return err
	}
	return guard.Assets.Sync()
}
func (guard *ActiveAuthority) Compact() error {
	if err := guard.writable(); err != nil {
		return err
	}
	return guard.Assets.Compact()
}
func (guard *ActiveAuthority) Backup(path string) error {
	if err := guard.writable(); err != nil {
		return err
	}
	if candidate, ok := guard.Assets.(interface {
		BackupAuthority(string, contract.ControlState) error
	}); ok {
		return candidate.BackupAuthority(path, guard.Control.ReadControl())
	}
	return guard.Assets.Backup(path)
}
