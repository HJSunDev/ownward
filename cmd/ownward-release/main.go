package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/HJSunDev/ownward/internal/releasebundle"
)

func main() {
	binary := flag.String("binary", "", "写入候选版本的 Ownward 目标平台发布二进制")
	embeddingDir := flag.String("embedding", "", "已校验的第一版向量能力包")
	license := flag.String("license", "LICENSE", "Ownward 许可证")
	readme := flag.String("readme", "README.md", "发布包使用说明")
	output := flag.String("output", "", "完整发布包输出目录")
	build := flag.Bool("build", false, "用实际平台向量包封存组合并构建程序")
	repository := flag.String("repository", ".", "产品源码目录")
	version := flag.String("version", "", "构建发布版本")
	flag.Parse()
	options := releasebundle.Options{
		Binary: *binary, EmbeddingDir: *embeddingDir, License: *license, Readme: *readme, Output: *output,
	}
	var manifest releasebundle.Manifest
	var err error
	if *build {
		manifest, err = releasebundle.Build(context.Background(), *repository, *version, options, os.Stderr)
	} else {
		manifest, err = releasebundle.Assemble(options)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ownward-release:", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(manifest); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-release:", err)
		os.Exit(1)
	}
}
