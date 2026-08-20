package performance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/candidate"
)

const ProductionStorageReportSchema = "ownward.production-storage-report/v1"

type ProductionStorageOptions struct {
	Scale      int
	Dimensions int
	Thresholds string
	BinaryPath string
	Candidate  string
	Workspace  string
}

type ProductionStorageCheck struct {
	Name    string  `json:"name"`
	Passed  bool    `json:"passed"`
	Actual  float64 `json:"actual"`
	Maximum float64 `json:"maximum,omitempty"`
	Unit    string  `json:"unit,omitempty"`
}

type ProductionStorageReport struct {
	Schema          string                   `json:"schema"`
	Candidate       string                   `json:"candidate"`
	BinarySHA256    string                   `json:"release_binary_sha256"`
	BinaryVersion   string                   `json:"release_binary_version"`
	ThresholdSHA256 string                   `json:"thresholds_sha256"`
	MeasuredAt      time.Time                `json:"measured_at"`
	OS              string                   `json:"os"`
	Arch            string                   `json:"arch"`
	Scale           int                      `json:"scale"`
	Dimensions      int                      `json:"dimensions"`
	RawAssetMiB     float64                  `json:"raw_asset_mib"`
	VectorMiB       float64                  `json:"vector_mib"`
	DerivedMiB      float64                  `json:"derived_storage_mib"`
	StorageMiB      float64                  `json:"storage_mib_at_scale"`
	DerivedRatio    float64                  `json:"derived_storage_over_raw_plus_vectors_ratio"`
	Checks          []ProductionStorageCheck `json:"checks"`
	Passed          bool                     `json:"passed"`
}

func RunProductionStorage(ctx context.Context, options ProductionStorageOptions) (ProductionStorageReport, error) {
	if options.Scale <= 0 || options.Dimensions <= 0 {
		return ProductionStorageReport{}, errors.New("生产存储规模和向量维度必须为正数")
	}
	if strings.TrimSpace(options.BinaryPath) == "" || strings.TrimSpace(options.Candidate) == "" || strings.TrimSpace(options.Thresholds) == "" {
		return ProductionStorageReport{}, errors.New("生产存储验收必须绑定候选二进制、提交和阈值")
	}
	binary, err := candidate.Inspect(ctx, options.BinaryPath, options.Candidate)
	if err != nil {
		return ProductionStorageReport{}, err
	}
	thresholdBytes, err := os.ReadFile(options.Thresholds)
	if err != nil {
		return ProductionStorageReport{}, fmt.Errorf("读取完整交付资源阈值: %w", err)
	}
	var thresholdDocument struct {
		Schema string `json:"schema"`
		Limits struct {
			DerivedRatio float64 `json:"derived_storage_over_raw_plus_vectors_ratio_max"`
		} `json:"limits"`
		Workload struct {
			Scale      int `json:"production_scale"`
			Dimensions int `json:"production_dimensions"`
		} `json:"workload"`
	}
	if err := json.Unmarshal(thresholdBytes, &thresholdDocument); err != nil {
		return ProductionStorageReport{}, fmt.Errorf("解析完整交付资源阈值: %w", err)
	}
	if thresholdDocument.Schema != "ownward.delivery-resource-thresholds/v1" || thresholdDocument.Limits.DerivedRatio <= 0 ||
		options.Scale != thresholdDocument.Workload.Scale || options.Dimensions != thresholdDocument.Workload.Dimensions {
		return ProductionStorageReport{}, errors.New("生产存储工作负载与冻结阈值不一致")
	}
	workspace, err := filepath.Abs(strings.TrimSpace(options.Workspace))
	if err != nil || strings.TrimSpace(options.Workspace) == "" {
		return ProductionStorageReport{}, errors.New("生产存储工作目录无效")
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return ProductionStorageReport{}, err
	}
	dataDir, err := os.MkdirTemp(workspace, "production-storage-*")
	if err != nil {
		return ProductionStorageReport{}, err
	}
	defer os.RemoveAll(dataDir)
	rawBytes, derivedBytes, storageBytes, err := prepareReleaseFixture(ctx, dataDir, options.Scale, options.Dimensions)
	if err != nil {
		return ProductionStorageReport{}, err
	}
	vectorBytes := int64(options.Scale * options.Dimensions * 4)
	ratio := float64(derivedBytes) / float64(rawBytes+vectorBytes)
	thresholdDigest := sha256.Sum256(thresholdBytes)
	report := ProductionStorageReport{
		Schema: ProductionStorageReportSchema, Candidate: strings.TrimSpace(options.Candidate),
		BinarySHA256: binary.SHA256, BinaryVersion: binary.Version, ThresholdSHA256: fmt.Sprintf("%x", thresholdDigest),
		MeasuredAt: time.Now().UTC(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		Scale: options.Scale, Dimensions: options.Dimensions,
		RawAssetMiB: float64(rawBytes) / (1024 * 1024), VectorMiB: float64(vectorBytes) / (1024 * 1024),
		DerivedMiB: float64(derivedBytes) / (1024 * 1024), StorageMiB: float64(storageBytes) / (1024 * 1024), DerivedRatio: ratio,
	}
	report.Checks = []ProductionStorageCheck{
		{Name: "production-record-count", Passed: options.Scale == thresholdDocument.Workload.Scale, Actual: float64(options.Scale), Maximum: float64(thresholdDocument.Workload.Scale), Unit: "items"},
		{Name: "production-vector-dimensions", Passed: options.Dimensions == thresholdDocument.Workload.Dimensions, Actual: float64(options.Dimensions), Maximum: float64(thresholdDocument.Workload.Dimensions), Unit: "dimensions"},
		{Name: "bounded-derived-storage", Passed: ratio <= thresholdDocument.Limits.DerivedRatio, Actual: ratio, Maximum: thresholdDocument.Limits.DerivedRatio, Unit: "ratio"},
	}
	report.Passed = true
	for _, check := range report.Checks {
		if !check.Passed {
			report.Passed = false
		}
	}
	return report, nil
}
