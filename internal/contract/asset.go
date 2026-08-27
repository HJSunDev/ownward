package contract

import (
	"errors"
	"strings"

	"github.com/HJSunDev/ownward/internal/domain"
)

const AssetChangeScopeSchema = "ownward.asset-change-scope/v1"

// AssetAuthority is the current stable authority port. ChangeScope makes each
// accepted mutation's affected asset versions explicit without inventing a
// second history or global sequence before the authority-substrate migration.
type AssetAuthority interface {
	CreateAsset(domain.Information) (ChangeScope, error)
	CreateAssets([]domain.Information) (ChangeScope, error)
	UpdateAsset(domain.Information, uint64) (ChangeScope, error)
	ReadCurrent(string) (domain.Information, bool)
	ReadVersion(string, uint64) (domain.Information, bool)
	ListCurrent() []domain.Information
	Sync() error
	Backup(string) error
}

type AssetVersion struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

type ChangeScope struct {
	Schema string         `json:"schema"`
	Assets []AssetVersion `json:"assets"`
}

func (s ChangeScope) Validate() error {
	if s.Schema != AssetChangeScopeSchema || len(s.Assets) == 0 {
		return errors.New("变化范围格式无效或为空")
	}
	seen := make(map[string]struct{}, len(s.Assets))
	for _, asset := range s.Assets {
		if strings.TrimSpace(asset.ID) == "" || asset.Revision == 0 {
			return errors.New("变化范围包含无效资产版本")
		}
		if _, exists := seen[asset.ID]; exists {
			return errors.New("变化范围包含重复资产")
		}
		seen[asset.ID] = struct{}{}
	}
	return nil
}

// AssetRestore is a function because restore creates a new authority store and
// must not mutate an already-open authority.
type AssetRestore func(archivePath, destination string) error
