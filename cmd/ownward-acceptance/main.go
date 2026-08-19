package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/HJSunDev/ownward/internal/acceptance"
)

func main() {
	defaultBinary := "bin/ownward"
	if runtime.GOOS == "windows" {
		defaultBinary += ".exe"
	}
	baseline := flag.String("baseline", "benchmarks/acceptance/v5/baseline.json", "固定验收基线描述文件")
	binary := flag.String("binary", defaultBinary, "待验收的发布二进制文件")
	output := flag.String("output", "", "可选的验收报告输出文件")
	dataDir := flag.String("data-dir", "", "可选的空白验收数据目录；默认使用临时目录")
	candidate := flag.String("candidate", "", "当前候选版本标识；最终验收应使用 Git 提交哈希")
	flag.Parse()
	report, err := acceptance.Run(context.Background(), acceptance.Options{BaselinePath: *baseline, OutputPath: *output, DataDir: *dataDir, Candidate: *candidate, BinaryPath: *binary})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ownward-acceptance:", err)
		os.Exit(2)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-acceptance:", err)
		os.Exit(2)
	}
	if !report.Passed {
		os.Exit(1)
	}
}
