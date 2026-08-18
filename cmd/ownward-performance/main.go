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
	scale := flag.Int("scale", 100_000, "信息数量")
	dimensions := flag.Int("dimensions", 384, "语义向量维度")
	iterations := flag.Int("iterations", 100, "主要时延测量次数")
	thresholds := flag.String("thresholds", "benchmarks/acceptance/v3/thresholds.json", "固定性能阈值")
	flag.Parse()
	report, err := performance.Run(context.Background(), performance.Options{Scale: *scale, Dimensions: *dimensions, Iterations: *iterations, Thresholds: *thresholds})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ownward-performance:", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-performance:", err)
		os.Exit(1)
	}
	if !report.Passed {
		os.Exit(1)
	}
}
