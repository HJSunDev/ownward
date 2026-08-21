package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	DataDir string
}

func Load(override string) (Config, error) {
	dir := strings.TrimSpace(override)
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv("OWNWARD_DATA_DIR"))
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
