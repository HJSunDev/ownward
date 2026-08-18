package candidate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Binary struct {
	Version string
	SHA256  string
	Size    int64
}

func Inspect(ctx context.Context, path, expectedVersion string) (Binary, error) {
	expectedVersion = strings.TrimSpace(expectedVersion)
	if expectedVersion == "" {
		return Binary{}, fmt.Errorf("候选版本不能为空")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Binary{}, fmt.Errorf("读取发布二进制文件: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Binary{}, fmt.Errorf("发布二进制路径不是普通文件")
	}
	file, err := os.Open(path)
	if err != nil {
		return Binary{}, fmt.Errorf("读取发布二进制文件: %w", err)
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if copyErr != nil {
		return Binary{}, fmt.Errorf("计算发布二进制摘要: %w", copyErr)
	}
	if closeErr != nil {
		return Binary{}, closeErr
	}
	output, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return Binary{}, fmt.Errorf("读取发布二进制版本: %w: %s", err, strings.TrimSpace(string(output)))
	}
	version := strings.TrimSpace(string(output))
	if version != expectedVersion {
		return Binary{}, fmt.Errorf("发布二进制版本为 %q，与候选版本 %q 不一致", version, expectedVersion)
	}
	return Binary{Version: version, SHA256: fmt.Sprintf("%x", digest.Sum(nil)), Size: info.Size()}, nil
}
