package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/HJSunDev/ownward/internal/performance"
)

func main() {
	defaultBinary := "bin/ownward"
	if runtime.GOOS == "windows" {
		defaultBinary += ".exe"
	}
	scale := flag.Int("scale", 100_000, "信息数量")
	dimensions := flag.Int("dimensions", 384, "语义向量维度")
	iterations := flag.Int("iterations", 100, "主要时延测量次数")
	thresholds := flag.String("thresholds", "benchmarks/acceptance/v3/thresholds.json", "固定性能阈值")
	binary := flag.String("binary", defaultBinary, "待验收的发布二进制文件")
	output := flag.String("output", "", "可选的性能报告输出文件")
	candidate := flag.String("candidate", "", "当前候选版本标识；最终验收应使用 Git 提交哈希")
	flag.Parse()
	report, err := performance.Run(context.Background(), performance.Options{
		Scale: *scale, Dimensions: *dimensions, Iterations: *iterations,
		Thresholds: *thresholds, BinaryPath: *binary, Candidate: *candidate,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ownward-performance:", err)
		os.Exit(1)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ownward-performance:", err)
		os.Exit(1)
	}
	encoded = append(encoded, '\n')
	if *output != "" {
		if err := os.WriteFile(*output, encoded, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "ownward-performance:", err)
			os.Exit(1)
		}
	}
	if _, err := os.Stdout.Write(encoded); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-performance:", err)
		os.Exit(1)
	}
	if !report.Passed {
		os.Exit(1)
	}
}
