package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// 插件默认使用本地体系；用户选择的公开连接材料决定跨地点接入。
func codexConnectionPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Ownward", "codex-connection.json"), nil
}
func runCodexConnect(ctx context.Context, stdout, stderr io.Writer) error {
	path, err := codexConnectionPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		material, err := readMaterial(path)
		if err != nil {
			return err
		}
		return runRemoteConnector(ctx, material)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return run(ctx, []string{"mcp"}, stdout, stderr)
}

// 只选择公开位置；真实接入仍由现有连接器申请授权。
func configureCodex(args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("codex-configure", flag.ContinueOnError)
	f.SetOutput(stderr)
	connection := f.String("connection", "", "可信公开连接材料；不提供时选择本机信息体系")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("不支持的位置参数")
	}
	path, err := codexConnectionPath()
	if err != nil {
		return err
	}
	if *connection == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		material, err := readMaterial(*connection)
		if err != nil {
			return err
		}
		data, err := json.Marshal(material)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(path), ".codex-connection-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		_, err = f.Write(data)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err = os.Rename(f.Name(), path); err != nil {
			return err
		}
	}
	fmt.Fprintln(stdout, "位置已保存；重新打开 Codex 后，沿用该位置的原授权接入。")
	return nil
}
