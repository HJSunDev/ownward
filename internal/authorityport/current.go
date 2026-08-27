package authorityport

import (
	"errors"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

// Current maps the existing asset log to the stable authority contract. The
// authority substrate owns this adapter and the concrete store lifecycle.
type Current struct {
	store *assetlog.Store
}

var _ contract.AssetAuthority = (*Current)(nil)

func Bind(store *assetlog.Store) (*Current, error) {
	if store == nil {
		return nil, errors.New("资产权威存储不能为空")
	}
	return &Current{store: store}, nil
}

func (c *Current) CreateAsset(value domain.Information) (contract.ChangeScope, error) {
	store, err := c.requireStore()
	if err != nil {
		return contract.ChangeScope{}, err
	}
	if err := store.Create(value); err != nil {
		return contract.ChangeScope{}, err
	}
	return scope(value), nil
}

func (c *Current) CreateAssets(values []domain.Information) (contract.ChangeScope, error) {
	store, err := c.requireStore()
	if err != nil {
		return contract.ChangeScope{}, err
	}
	if err := store.CreateBatch(values); err != nil {
		return contract.ChangeScope{}, err
	}
	result := contract.ChangeScope{Schema: contract.AssetChangeScopeSchema, Assets: make([]contract.AssetVersion, len(values))}
	for index, value := range values {
		result.Assets[index] = contract.AssetVersion{ID: value.ID, Revision: value.Revision}
	}
	return result, result.Validate()
}

func (c *Current) UpdateAsset(value domain.Information, expectedRevision uint64) (contract.ChangeScope, error) {
	store, err := c.requireStore()
	if err != nil {
		return contract.ChangeScope{}, err
	}
	if err := store.Update(value, expectedRevision); err != nil {
		return contract.ChangeScope{}, err
	}
	return scope(value), nil
}

func (c *Current) ReadCurrent(id string) (domain.Information, bool) {
	if c == nil || c.store == nil {
		return domain.Information{}, false
	}
	return c.store.Get(id)
}

func (c *Current) ReadVersion(id string, revision uint64) (domain.Information, bool) {
	value, exists := c.ReadCurrent(id)
	return value, exists && value.Revision == revision
}

func (c *Current) ListCurrent() []domain.Information {
	if c == nil || c.store == nil {
		return nil
	}
	return c.store.All()
}

func (c *Current) Sync() error {
	store, err := c.requireStore()
	if err != nil {
		return err
	}
	return store.Sync()
}

func (c *Current) Compact() error {
	store, err := c.requireStore()
	if err != nil {
		return err
	}
	return store.Compact()
}

func (c *Current) Backup(destination string) error {
	store, err := c.requireStore()
	if err != nil {
		return err
	}
	return store.Backup(destination)
}

func (c *Current) requireStore() (*assetlog.Store, error) {
	if c == nil || c.store == nil {
		return nil, errors.New("资产权威端口尚未绑定存储")
	}
	return c.store, nil
}

func scope(value domain.Information) contract.ChangeScope {
	result := contract.ChangeScope{
		Schema: contract.AssetChangeScopeSchema,
		Assets: []contract.AssetVersion{{ID: value.ID, Revision: value.Revision}},
	}
	return result
}

var Restore contract.AssetRestore = assetlog.Restore
