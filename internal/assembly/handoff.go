package assembly

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func ValidateHandoffData(destination string) error {
	_, e := ReadControlAt(destination)
	return e
}

func controlOptions() boundedstore.Options {
	b, _ := resourcebudget.New(16*resourcebudget.MiB, 4*resourcebudget.MiB)
	return boundedstore.Options{Budget: b}
}

func ReadControlAt(root string) (contract.ControlState, error) {
	return ReadControlAtContext(context.Background(), root)
}

// ReadLocalIdentity 用于本机入口定位；不争夺活动服务的写锁，不提供授权依据。
func ReadLocalIdentity(ctx context.Context, root string) (contract.ControlState, error) {
	if _, err := os.Stat(filepath.Join(root, "storage.json")); err == nil {
		return boundedstore.ReadDeploymentIdentity(ctx, root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return contract.ControlState{}, err
	}
	return ReadControlAtContext(ctx, root)
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
	return readLegacyControlFile(root)
}

func readLegacyControlFile(root string) (contract.ControlState, error) {
	data, err := os.ReadFile(filepath.Join(root, "authority", "control.json"))
	if err != nil {
		return contract.ControlState{}, err
	}
	var envelope struct {
		Schema string                `json:"schema"`
		State  contract.ControlState `json:"state"`
		SHA256 string                `json:"sha256"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return contract.ControlState{}, fmt.Errorf("解析权威控制状态: %w", err)
	}
	if envelope.Schema != "ownward.control-state-envelope/v1" || envelope.State.Validate() != nil {
		return contract.ControlState{}, errors.New("权威控制状态格式无效")
	}
	stateJSON, err := json.Marshal(envelope.State)
	if err != nil {
		return contract.ControlState{}, err
	}
	digest := sha256.Sum256(stateJSON)
	if envelope.SHA256 != hex.EncodeToString(digest[:]) {
		return contract.ControlState{}, errors.New("权威控制状态完整性校验失败")
	}
	return envelope.State, nil
}

func CleanRetiredData(ctx context.Context, root string) error {
	if _, e := os.Stat(filepath.Join(root, "storage.json")); e == nil {
		return boundedstore.CleanRetiredDeployment(ctx, root, controlOptions())
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	state, e := readLegacyControlFile(root)
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
	if _, e := os.Stat(filepath.Join(root, "storage.json")); e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return errors.New("迁移接收目录缺少 SQLite 部署存储")
		}
		return e
	}
	return boundedstore.ActivateDeploymentHandoff(context.Background(), root, permit, controlOptions())
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
	if len(z.File) != 1 || authority.Mode()&os.ModeSymlink != 0 {
		return zero, errors.New("SQLite迁移包包含非交接文件")
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
		return zero, errors.New("不支持旧目录格式交接包")
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

func WriteHandoffArchive(root string, destination io.Writer) error {
	archive := zip.NewWriter(destination)
	buf := make([]byte, 64*1024)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("迁移快照包含非普通文件")
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		if name != "authority.zip" && !strings.HasPrefix(name, "state/") {
			return errors.New("迁移快照包含未声明文件")
		}
		writer, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.CopyBuffer(writer, f, buf)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	if err != nil {
		_ = archive.Close()
		return err
	}
	return archive.Close()
}
