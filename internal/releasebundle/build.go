package releasebundle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/embedding"
)

// Build 将实际平台资源封存进二进制；构建覆盖层不改工作区组合，避免平台间覆盖。
func Build(ctx context.Context, repository, version string, options Options, log io.Writer) (Manifest, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`).MatchString(version) {
		return Manifest{}, errors.New("发布版本必须为简单的版本标识")
	}
	repository, err := filepath.Abs(repository)
	if err != nil {
		return Manifest{}, err
	}
	options.EmbeddingDir, err = filepath.Abs(options.EmbeddingDir)
	if err != nil {
		return Manifest{}, err
	}
	bundle, err := embedding.LoadDistributionBundle(options.EmbeddingDir)
	if err != nil {
		return Manifest{}, err
	}
	if err = embedding.VerifyRuntimeTarget(bundle, runtime.GOOS, runtime.GOARCH); err != nil {
		return Manifest{}, err
	}
	manifestPath := filepath.Join(repository, "manifests", "compositions", "v1", "current-collaborative.json")
	manifest, err := composition.Load(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	manifest, err = bindPlatformBundle(repository, manifest, bundle)
	if err != nil {
		return Manifest{}, err
	}
	sealed, err := composition.Seal(repository, manifest)
	if err != nil {
		return Manifest{}, err
	}
	tmp, err := os.MkdirTemp("", "ownward-release-build-*")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tmp)
	f, err := os.Create(filepath.Join(tmp, "composition.json"))
	if err != nil {
		return Manifest{}, err
	}
	err = composition.WriteJSON(f, sealed)
	err = errors.Join(err, f.Close())
	if err != nil {
		return Manifest{}, err
	}
	overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{manifestPath: filepath.Join(tmp, "composition.json")}})
	if err = os.WriteFile(filepath.Join(tmp, "overlay.json"), overlay, 0600); err != nil {
		return Manifest{}, err
	}
	options.Binary = filepath.Join(tmp, ExecutableName(runtime.GOOS))
	flags := "-s -w -X main.version=" + version
	if runtime.GOOS == "windows" {
		flags += " -H=windowsgui"
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-overlay", filepath.Join(tmp, "overlay.json"), "-trimpath", "-ldflags="+flags, "-o", options.Binary, "./cmd/ownward")
	cmd.Dir = repository
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOAMD64=v1", "GOARM64=v8.0")
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Run(); err != nil {
		return Manifest{}, err
	}
	return Assemble(options)
}

func bindPlatformBundle(repository string, manifest composition.Manifest, bundle embedding.Bundle) (composition.Manifest, error) {
	for i := range manifest.Components {
		c := &manifest.Components[i]
		if c.Role != "vector" {
			continue
		}
		oldRoot := ""
		for _, v := range c.Content {
			if v.Name == "manifest.json" {
				oldRoot = filepath.ToSlash(filepath.Dir(v.Path)) + "/"
			}
		}
		if oldRoot == "" {
			return manifest, errors.New("组合清单缺少向量包位置")
		}
		kept := make([]composition.Content, 0, len(c.Content))
		for _, v := range c.Content {
			if !strings.HasPrefix(filepath.ToSlash(v.Path), oldRoot) {
				kept = append(kept, v)
			}
		}
		files := []string{"manifest.json", bundle.Manifest.Model.Path}
		for file := range bundle.Manifest.Runtime.Files {
			files = append(files, file)
		}
		sort.Strings(files)
		for _, name := range files {
			path, err := filepath.Rel(repository, filepath.Join(bundle.Root, filepath.FromSlash(name)))
			if err != nil || !filepath.IsLocal(path) {
				return manifest, errors.New("构建用向量包须位于当前源码目录内")
			}
			kept = append(kept, composition.Content{Name: strings.ReplaceAll(name, "/", "-"), Path: filepath.ToSlash(path)})
		}
		c.Content = kept
		c.Config["space"] = bundle.Manifest.Space.ID
		c.Config["bundle_schema"] = bundle.Manifest.Schema
		c.Config["capability"] = bundle.Manifest.Capability
		c.Config["dimensions"] = bundle.Manifest.Space.Dimensions
		return manifest, nil
	}
	return manifest, errors.New("组合清单缺少向量组件")
}
