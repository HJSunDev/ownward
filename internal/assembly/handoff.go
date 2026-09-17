package assembly

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

// Verify the selected kernel's derived format at the composition boundary;
// the authority store does not depend on any particular kernel implementation.
func ValidateHandoffData(destination string) error {
	if _, e := os.Stat(filepath.Join(destination, "storage.json")); e == nil {
		_, e = ReadControlAt(destination)
		return e
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	path := filepath.Join(destination, "state")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	d, err := derived.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	if d.RecoveredCorruption() {
		return errors.New("迁移派生状态不完整")
	}
	_, err = d.AllWithEmbeddings()
	return err
}

func controlOptions() boundedstore.Options {
	b, _ := resourcebudget.New(16*resourcebudget.MiB, 4*resourcebudget.MiB)
	return boundedstore.Options{Budget: b}
}

func ReadControlAt(root string) (contract.ControlState, error) {
	return ReadControlAtContext(context.Background(), root)
}

func ReadControlAtContext(ctx context.Context, root string) (contract.ControlState, error) {
	if _, e := os.Stat(filepath.Join(root, "storage.json")); e == nil {
		return boundedstore.ReadDeploymentControl(ctx, root, controlOptions())
	} else if !errors.Is(e, os.ErrNotExist) {
		return contract.ControlState{}, e
	}
	if _, e := os.Stat(filepath.Join(root, "migration.json")); e == nil {
		return contract.ControlState{}, errors.New("存储迁移尚未完成")
	}
	return authoritysubstrate.ReadControlAt(root)
}

func CleanRetiredData(ctx context.Context, root string) error {
	if _, e := os.Stat(filepath.Join(root, "storage.json")); e == nil {
		return boundedstore.CleanRetiredDeployment(ctx, root, controlOptions())
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	state, e := authoritysubstrate.ReadControlAt(root)
	if e != nil {
		return e
	}
	if state.Access == nil || state.Access.Handoff == nil || state.Access.Handoff.Phase != "retired" {
		return errors.New("只能清理已退役部署")
	}
	for _, name := range []string{"assets", "state"} {
		if e = os.RemoveAll(filepath.Join(root, name)); e != nil {
			return e
		}
	}
	return nil
}

func ActivateHandoff(root string, permit contract.Handoff) error {
	if _, e := os.Stat(filepath.Join(root, "storage.json")); e == nil {
		return boundedstore.ActivateDeploymentHandoff(context.Background(), root, permit, controlOptions())
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return authoritysubstrate.ActivateHandoff(root, permit)
}

func StageHandoff(path, destination string) (contract.ControlState, error) {
	var zero contract.ControlState
	z, e := zip.OpenReader(path)
	if e != nil {
		return zero, e
	}
	defer z.Close()
	var authority *zip.File
	for _, f := range z.File {
		if f.Name == "authority.zip" {
			if authority != nil {
				return zero, errors.New("重复的权威归档")
			}
			authority = f
		}
	}
	if authority == nil {
		return zero, errors.New("迁移包缺少唯一权威")
	}
	if e = os.MkdirAll(filepath.Dir(destination), 0700); e != nil {
		return zero, e
	}
	f, e := os.CreateTemp(filepath.Dir(destination), ".handoff-inspect-*.zip")
	if e != nil {
		return zero, e
	}
	defer os.Remove(f.Name())
	r, e := authority.Open()
	if e != nil {
		f.Close()
		return zero, e
	}
	_, e = io.CopyBuffer(f, r, make([]byte, 64*1024))
	e = errors.Join(e, r.Close(), f.Close())
	if e != nil {
		return zero, e
	}
	native, e := boundedstore.IsDeploymentArchive(f.Name())
	if e != nil {
		return zero, e
	}
	if !native {
		return authoritysubstrate.StageHandoff(path, destination)
	}
	if len(z.File) != 1 || authority.Mode()&os.ModeSymlink != 0 {
		return zero, errors.New("SQLite迁移包包含非交接文件")
	}
	state, e := boundedstore.RestoreArchive(context.Background(), f.Name(), destination, controlOptions(), true)
	if e != nil {
		return zero, e
	}
	if state.Access == nil || state.Access.Handoff == nil || state.Access.Handoff.Phase != "frozen" {
		return zero, errors.New("迁移包不是冻结中的权威快照")
	}
	return state, nil
}
