package localowner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
)

// Vault 属于部署适配，不进入资产备份；Windows 使用当前用户 DPAPI 保护秘密。
type Vault struct{ Root string }

func Default() (Vault, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return Vault{}, err
	}
	return Vault{Root: filepath.Join(dir, "Ownward", "connections")}, nil
}

func (v Vault) path(system, connection string) (string, error) {
	if system == "" || connection == "" || !filepath.IsAbs(v.Root) {
		return "", errors.New("连接凭据位置无效")
	}
	sum := sha256.Sum256([]byte(system + "\x00" + connection))
	return filepath.Join(v.Root, hex.EncodeToString(sum[:])+".key"), nil
}

func (v Vault) Load(system, connection string) (string, error) {
	path, err := v.path(system, connection)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
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
	data, err := protect([]byte(credential))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(v.Root, 0o700); err != nil {
		return err
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
