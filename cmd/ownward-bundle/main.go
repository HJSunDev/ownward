package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/HJSunDev/ownward/internal/embedding"
)

func main() {
	model := flag.String("model", "", "锁定的 EmbeddingGemma GGUF 文件")
	runtimeArchive := flag.String("runtime-archive", "", "锁定的 llama.cpp Windows x64 CPU 压缩包")
	legalRoot := flag.String("legal-root", "third_party", "随包交付的第三方许可材料目录")
	output := flag.String("output", "", "向量能力包输出目录")
	flag.Parse()
	bundle, err := embedding.BuildSelectedBundle(embedding.BuildOptions{
		ModelPath: *model, RuntimeArchive: *runtimeArchive, LegalRoot: *legalRoot, Output: *output,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ownward-bundle:", err)
		os.Exit(1)
	}
	fmt.Println(bundle.Manifest.Space.ID)
}
