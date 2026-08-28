//go:build ownward_migration

package authoritysubstrate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/authorityport"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

// OpenExistingControlForMigration gives the offline migration boundary access
// to the one existing control owner without opening or initializing an asset
// store. The caller must already own the selected store's exclusive lock.
func OpenExistingControlForMigration(dataDir string, allowed ...contract.ControlState) (contract.ControlAuthority, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, errors.New("权威基座数据目录必须是绝对路径")
	}
	path := filepath.Join(filepath.Clean(dataDir), controlDirectory, controlFile)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("权威控制状态尚未建立，迁移命令禁止初始化")
	}
	if len(allowed) == 0 {
		return nil, errors.New("迁移命令缺少允许的权威控制身份")
	}
	control, err := openControl(filepath.Dir(path), allowed[0])
	if err != nil {
		return nil, err
	}
	current := control.ReadControl()
	for _, candidate := range allowed {
		if candidate.Validate() == nil && current.ActiveComposition == candidate.ActiveComposition && current.ActiveKernelGeneration == candidate.ActiveKernelGeneration {
			return control, nil
		}
	}
	return nil, fmt.Errorf("权威控制状态不属于迁移计划允许的 baseline/target: %s", current.ActiveComposition)
}

// OpenInactiveBaselineForMigration is the sole concrete legacy-store opening
// seam used to catch a read-only rollback source up while the candidate is the
// active authority. It remains outside the product request graph.
func OpenInactiveBaselineForMigration(dataDir string) (contract.AssetAuthority, func() error, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, nil, errors.New("权威基座数据目录必须是绝对路径")
	}
	store, err := assetlog.Open(filepath.Join(filepath.Clean(dataDir), "assets"))
	if err != nil {
		return nil, nil, err
	}
	port, err := authorityport.Bind(store)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return port, store.Close, nil
}

// CaptureForMigration returns one control-consistent shallow asset snapshot.
// The long candidate copy starts only after the caller closes the substrate.
func CaptureForMigration(substrate *Substrate) ([]domain.Information, contract.ControlState, error) {
	if substrate == nil || substrate.assets == nil || substrate.control == nil {
		return nil, contract.ControlState{}, errors.New("权威基座尚未打开")
	}
	for attempt := 0; attempt < 2; attempt++ {
		before := substrate.control.ReadControl()
		assets := substrate.assets.CaptureCurrentForMigration()
		after := substrate.control.ReadControl()
		if before == after {
			return assets, after, nil
		}
	}
	return nil, contract.ControlState{}, errors.New("捕获资产期间权威控制持续变化")
}
