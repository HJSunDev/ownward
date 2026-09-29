package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/HJSunDev/ownward/internal/embedding"
)

func main() {
	model := flag.String("model", "", "锁定的 EmbeddingGemma GGUF 文件")
	runtimeArchive := flag.String("runtime-archive", "", "锁定的 llama.cpp CPU 运行时压缩包")
	runtimeBuild := flag.String("runtime-build", "", "Linux/macOS 锁定源码构建记录；省略时使用既有 Windows 制品")
	legalRoot := flag.String("legal-root", "third_party", "随包交付的第三方许可材料目录")
	output := flag.String("output", "", "向量能力包输出目录")
	flag.Parse()
	bundle, err := embedding.BuildSelectedBundle(embedding.BuildOptions{
		ModelPath: *model, RuntimeArchive: *runtimeArchive, LegalRoot: *legalRoot, Output: *output, RuntimeBuild: *runtimeBuild,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ownward-bundle:", err)
		os.Exit(1)
	}
	fmt.Println(bundle.Manifest.Space.ID)
}
