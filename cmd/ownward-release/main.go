package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/HJSunDev/ownward/internal/releasebundle"
)

func main() {
	binary := flag.String("binary", "", "写入候选版本的 Ownward Windows 发布二进制")
	embeddingDir := flag.String("embedding", "", "已校验的第一版向量能力包")
	license := flag.String("license", "LICENSE", "Ownward 许可证")
	readme := flag.String("readme", "README.md", "发布包使用说明")
	output := flag.String("output", "", "完整发布包输出目录")
	flag.Parse()
	manifest, err := releasebundle.Assemble(releasebundle.Options{
		Binary: *binary, EmbeddingDir: *embeddingDir, License: *license, Readme: *readme, Output: *output,
	})
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
