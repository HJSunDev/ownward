package contract

import (
	"context"
	"io"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

// AssetMeta 不携带正文或可随正文增长的场景、来源和关系列表。
type AssetMeta struct {
	ID            string
	Revision      uint64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Kind          domain.InformationKind
	ContentBytes  int64
	ContentSHA256 string
}

type AssetPage struct {
	Items []AssetMeta
	Next  string
}

// BoundedAssets 与旧全量端口分离，防止兼容包装再次收集整库。
type BoundedAssets interface {
	ReadAssetMeta(context.Context, string, uint64) (AssetMeta, error)
	OpenContent(context.Context, string, uint64) (io.ReadCloser, error)
	OpenDetails(context.Context, string, uint64) (io.ReadCloser, error)
	ReadRanges(context.Context, string, uint64, []ContentRange, io.Writer) error
	ScanAssets(context.Context, string, int) (AssetPage, error)
}
