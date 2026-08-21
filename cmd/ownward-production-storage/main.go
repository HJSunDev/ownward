package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/HJSunDev/ownward/internal/performance"
)

func main() {
	binary := flag.String("binary", "", "候选发布二进制")
	candidate := flag.String("candidate", "", "完整候选提交哈希")
	scale := flag.Int("scale", 100_000, "生产信息数量")
	dimensions := flag.Int("dimensions", 512, "第一版向量维度")
	thresholds := flag.String("thresholds", "benchmarks/acceptance/suite/adapters/product_resource/thresholds.json", "完整交付资源阈值")
	workspace := flag.String("workspace", "", "位于非系统盘的临时工作目录")
	output := flag.String("output", "", "报告输出文件")
	flag.Parse()
	if *output == "" {
		fmt.Fprintln(os.Stderr, "ownward-production-storage: 必须提供 --output")
		os.Exit(2)
	}
	report, err := performance.RunProductionStorage(context.Background(), performance.ProductionStorageOptions{
		Scale: *scale, Dimensions: *dimensions, Thresholds: *thresholds, BinaryPath: *binary, Candidate: *candidate, Workspace: *workspace,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ownward-production-storage:", err)
		os.Exit(2)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ownward-production-storage:", err)
		os.Exit(2)
	}
	encoded = append(encoded, '\n')
	temporary := *output + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-production-storage:", err)
		os.Exit(2)
	}
	if err := os.Rename(temporary, *output); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-production-storage:", err)
		os.Exit(2)
	}
	fmt.Print(string(encoded))
	if !report.Passed {
		os.Exit(1)
	}
}
