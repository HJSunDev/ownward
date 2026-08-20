package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	DataDir          string
	RuntimeDir       string
	DisableRelations bool
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
	runtimeDir := strings.TrimSpace(os.Getenv("OWNWARD_RUNTIME_DIR"))
	if runtimeDir == "" {
		base, runtimeErr := os.UserConfigDir()
		if runtimeErr != nil {
			return Config{}, fmt.Errorf("确定默认运行状态目录: %w", runtimeErr)
		}
		runtimeDir = filepath.Join(base, "Ownward", "runtime")
	}
	absoluteRuntime, err := filepath.Abs(runtimeDir)
	if err != nil {
		return Config{}, fmt.Errorf("解析运行状态目录: %w", err)
	}
	disableRelations := false
	if raw := strings.TrimSpace(os.Getenv("OWNWARD_DISABLE_RELATIONS")); raw != "" {
		value, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return Config{}, errors.New("OWNWARD_DISABLE_RELATIONS 必须是布尔值")
		}
		disableRelations = value
	}
	return Config{
		DataDir:          absolute,
		RuntimeDir:       absoluteRuntime,
		DisableRelations: disableRelations,
	}, nil
}
