package embedding

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractRuntimeKeepsOnlyServerDependencyClosure(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "runtime.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name := range selectedRuntimeFiles {
		entry, createErr := archive.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write([]byte(name)); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	extra, err := archive.Create("llama-cli.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extra.Write([]byte("unused")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "runtime")
	files, err := extractRuntime(archivePath, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(selectedRuntimeFiles) {
		t.Fatalf("运行时闭包文件数错误: got %d want %d", len(files), len(selectedRuntimeFiles))
	}
	if _, exists := files["runtime/llama-cli.exe"]; exists {
		t.Fatal("发布包包含不参与向量服务运行的 llama-cli.exe")
	}
	if _, err := os.Stat(filepath.Join(target, "llama-cli.exe")); !os.IsNotExist(err) {
		t.Fatal("无关运行时工具被解压到发布包")
	}
}

func TestExtractRuntimeRejectsIncompleteServerDependencyClosure(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "runtime.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("llama-server.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("server")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := extractRuntime(archivePath, filepath.Join(t.TempDir(), "runtime")); err == nil {
		t.Fatal("不完整的服务依赖闭包被接受")
	}
}
