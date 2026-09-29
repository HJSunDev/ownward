package config

import (
	"errors"
	"fmt"
	"github.com/HJSunDev/ownward/internal/desktop"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	DataDir string
	System  string
}

func Load(override string) (Config, error) {
	dir := strings.TrimSpace(override)
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv("OWNWARD_DATA_DIR"))
	}
	if dir == "" {
		path, err := desktop.StatePath()
		if err != nil {
			return Config{}, err
		}
		state, err := desktop.Load(path)
		if err == nil && state.Binding != nil {
			return Config{DataDir: state.Binding.Data, System: state.Binding.System}, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("日常入口记录需要修复: %w", err)
		}
	}
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Config{}, fmt.Errorf("确定默认数据目录: %w", err)
		}
		dir = filepath.Join(base, "Ownward")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, fmt.Errorf("解析数据目录: %w", err)
	}
	return Config{DataDir: absolute}, nil
}
