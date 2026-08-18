package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HJSunDev/ownward/internal/semantics"
)

type Config struct {
	DataDir             string
	ModelBaseURL        string
	ModelAPIKey         string
	ChatModel           string
	EmbeddingModel      string
	EmbeddingDimensions int
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
	dimensions := 0
	if raw := strings.TrimSpace(os.Getenv("OWNWARD_EMBEDDING_DIMENSIONS")); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 0 || value > 8192 {
			return Config{}, errors.New("OWNWARD_EMBEDDING_DIMENSIONS 必须介于零和 8192 之间")
		}
		dimensions = value
	}
	return Config{
		DataDir:             absolute,
		ModelBaseURL:        strings.TrimSpace(os.Getenv("OWNWARD_MODEL_BASE_URL")),
		ModelAPIKey:         strings.TrimSpace(os.Getenv("OWNWARD_MODEL_API_KEY")),
		ChatModel:           strings.TrimSpace(os.Getenv("OWNWARD_CHAT_MODEL")),
		EmbeddingModel:      strings.TrimSpace(os.Getenv("OWNWARD_EMBEDDING_MODEL")),
		EmbeddingDimensions: dimensions,
	}, nil
}

func (c Config) SemanticProvider(requireModel bool) (semantics.Provider, error) {
	if c.ModelBaseURL == "" {
		if requireModel {
			return nil, errors.New("验收需要配置 OWNWARD_MODEL_BASE_URL、OWNWARD_CHAT_MODEL 和 OWNWARD_EMBEDDING_MODEL")
		}
		return semantics.Heuristic{}, nil
	}
	return semantics.NewOpenAI(semantics.OpenAIConfig{
		BaseURL:             c.ModelBaseURL,
		APIKey:              c.ModelAPIKey,
		ChatModel:           c.ChatModel,
		EmbeddingModel:      c.EmbeddingModel,
		EmbeddingDimensions: c.EmbeddingDimensions,
	})
}
