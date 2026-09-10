// Package codexplugin 包含随发布二进制交付的原生宿主接入定义。
package codexplugin

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:plugins/ownward
var files embed.FS

func Write(root string) error {
	source, err := fs.Sub(files, "plugins/ownward")
	if err != nil {
		return err
	}
	return fs.WalkDir(source, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}
