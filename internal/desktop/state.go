// Package desktop 管理本机安装与位置元数据，不保存凭据或决定访问权限。
package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const Schema = "ownward.desktop/v1"

type Paths struct {
	Root, State, Data, Programs, Desktop string
}

func DefaultPaths() (Paths, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, err
	}
	root, err := installRoot(config)
	if err != nil {
		return Paths{}, err
	}
	programs, desk, err := shortcutFolders()
	if err != nil {
		return Paths{}, err
	}
	return Paths{Root: root, State: filepath.Join(config, "Ownward", "desktop.json"), Data: filepath.Join(config, "Ownward"), Programs: programs, Desktop: desk}, nil
}

func StatePath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Ownward", "desktop.json"), nil
}

func installRoot(config string) (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", errors.New("无法确定用户安装目录")
		}
		return filepath.Join(base, "Programs", "Ownward"), nil
	case "darwin":
		return filepath.Join(config, "Ownward", "program"), nil
	default:
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, ".local", "share")
		}
		if !filepath.IsAbs(base) {
			return "", errors.New("用户数据目录必须是绝对路径")
		}
		return filepath.Join(base, "ownward", "program"), nil
	}
}

type Binding struct {
	System string `json:"system"`
	Data   string `json:"data"`
}

type State struct {
	Revision   uint64   `json:"revision,omitempty"`
	Schema     string   `json:"schema"`
	Release    string   `json:"release"`
	Executable string   `json:"executable"`
	Binding    *Binding `json:"binding,omitempty"`
	Desktop    bool     `json:"desktop"`
}

func Load(path string) (State, error) {
	f, err := os.Open(path)
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	var state State
	d := json.NewDecoder(io.LimitReader(f, 16<<10))
	d.DisallowUnknownFields()
	if err = d.Decode(&state); err != nil {
		return state, fmt.Errorf("安装记录不可读: %w", err)
	}
	if state.Schema != Schema || !filepath.IsAbs(state.Executable) || state.Release == "" {
		return state, errors.New("安装记录无效")
	}
	if state.Binding != nil && (state.Binding.System == "" || !filepath.IsAbs(state.Binding.Data)) {
		return state, errors.New("资料位置记录无效")
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return state, errors.New("安装记录包含多余内容")
	}
	return state, nil
}

func Save(path string, state State) error {
	state.Schema = Schema
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, append(data, '\n'), 0600)
}

// 写入或替换失败时保留上一份完整记录。
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".ownward-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return replace(f.Name(), path)
}

// 下载目录与持久安装目录共用正式制品结构。
func ReleaseRoot(executable string) (string, error) {
	root := filepath.Dir(filepath.Dir(executable))
	if filepath.Base(filepath.Dir(executable)) != "bin" {
		return "", errors.New("请使用完整的 Ownward 发布制品")
	}
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); err != nil {
		return "", errors.New("当前程序不是完整发布制品，请取得正式安装包")
	}
	return root, nil
}
