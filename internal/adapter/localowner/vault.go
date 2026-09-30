package localowner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/HJSunDev/ownward/internal/desktop"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Vault 属于部署适配，不进入资产备份；Windows 使用当前用户 DPAPI 保护秘密。
type Vault struct {
	Root    string
	Machine bool
	Scope   string
}

// ForData isolates restored instances while preserving read access to legacy
// account-scoped connections. Callers must still authenticate every value.
func ForData(dataDir string) (Vault, error) {
	v, err := Default()
	if err != nil {
		return v, err
	}
	v.Scope, err = DataScope(dataDir)
	return v, err
}

func DataScope(dataDir string) (string, error) {
	path, err := filepath.Abs(dataDir)
	if err != nil {
		return "", err
	}
	if resolved, e := filepath.EvalSymlinks(path); e == nil {
		path = resolved
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	sum := sha256.Sum256([]byte(path))
	return "local-recovery:" + hex.EncodeToString(sum[:]), nil
}

func Default() (Vault, error) {
	dir, err := desktop.AccountRoot()
	if err != nil {
		return Vault{}, err
	}
	root := filepath.Join(dir, "connections")
	if _, e := os.Stat(root); errors.Is(e, os.ErrNotExist) {
		if legacy, e := os.UserConfigDir(); e == nil {
			legacy = filepath.Join(legacy, "Ownward", "connections")
			if _, e = os.Stat(legacy); e == nil {
				if e = desktop.ValidateSharedDirectory(legacy); e != nil {
					return Vault{}, e
				}
				root = legacy
			}
		}
	}
	return Vault{Root: root}, nil
}

func (v Vault) path(system, connection string) (string, error) {
	if system == "" || connection == "" || !filepath.IsAbs(v.Root) {
		return "", errors.New("连接凭据位置无效")
	}
	key := system + "\x00" + connection
	if v.Scope != "" {
		key += "\x00instance\x00" + v.Scope
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(v.Root, hex.EncodeToString(sum[:])+".key"), nil
}

func (v Vault) Load(system, connection string) (string, error) {
	path, err := v.path(system, connection)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && v.Scope != "" {
		legacy := v
		legacy.Scope = ""
		return legacy.Load(system, connection)
	}
	if err != nil {
		return "", err
	}
	plain, err := unprotect(data)
	if err != nil {
		return "", errors.New("无法恢复当前用户的连接凭据")
	}
	return string(plain), nil
}

func (v Vault) Save(system, connection, credential string) error {
	path, err := v.path(system, connection)
	if err != nil {
		return err
	}
	if credential == "" {
		return errors.New("连接凭据不能为空")
	}
	var data []byte
	if v.Machine {
		data, err = protectMachine([]byte(credential))
	} else {
		data, err = protect([]byte(credential))
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(v.Root, 0o700); err != nil {
		return err
	}
	if v.Machine {
		if err := ProtectServiceDirectory(v.Root); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(v.Root, ".connection-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replace(tmp.Name(), path)
}
