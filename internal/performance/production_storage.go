package performance

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/candidate"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	_ "modernc.org/sqlite"
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
	binaryInfo, err := candidate.Inspect(ctx, options.BinaryPath, options.Candidate)
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
		BinarySHA256: binaryInfo.SHA256, BinaryVersion: binaryInfo.Version, ThresholdSHA256: fmt.Sprintf("%x", thresholdDigest),
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

// prepareReleaseFixture 直接构造现役 boundedstore SQLite 生产格式部署（512 维），
// 并以完整 SQLite 库与剥离派生缓存后的权威备份库之差精确计量派生状态存储字节。
func prepareReleaseFixture(ctx context.Context, dataDir string, scale, dimensions int) (int64, int64, int64, error) {
	if dimensions != 512 {
		return 0, 0, 0, errors.New("现役 SQLite 生产存储仅支持 512 维向量")
	}
	assetsDir := filepath.Join(dataDir, "assets")
	genDir := filepath.Join(dataDir, "state", "generations", "gen-production")
	if err := os.MkdirAll(assetsDir, 0o700); err != nil {
		return 0, 0, 0, err
	}
	if err := os.MkdirAll(genDir, 0o700); err != nil {
		return 0, 0, 0, err
	}
	generatedAt := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	manifest, err := json.MarshalIndent(struct {
		Format    string    `json:"format"`
		CreatedAt time.Time `json:"created_at"`
	}{Format: "ownward.asset-log/v3", CreatedAt: generatedAt}, "", "  ")
	if err != nil {
		return 0, 0, 0, err
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "manifest.json"), append(manifest, '\n'), 0o600); err != nil {
		return 0, 0, 0, err
	}
	assetFile, err := os.OpenFile(filepath.Join(assetsDir, "information.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, 0, 0, err
	}
	derivedLogPath := filepath.Join(genDir, "organization.binlog")
	derivedFile, err := os.OpenFile(derivedLogPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = assetFile.Close()
		return 0, 0, 0, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = assetFile.Close()
			_ = derivedFile.Close()
		}
	}()
	assetWriter := bufio.NewWriterSize(assetFile, 1024*1024)
	derivedWriter := bufio.NewWriterSize(derivedFile, 1024*1024)
	derivedHasher := sha256.New()
	var derivedSealedBytes int64
	var rawAssetBytes int64
	const spaceID = "production-storage-space"
	vecBuf := make([]byte, dimensions*4)

	for index := 0; index < scale; index++ {
		if err := ctx.Err(); err != nil {
			return 0, 0, 0, err
		}
		asset, record := performanceValues(index, dimensions, spaceID, generatedAt)
		encodedAsset, err := json.Marshal(asset)
		if err != nil {
			return 0, 0, 0, err
		}
		rawAssetBytes += int64(len(encodedAsset) + 1)
		entry := struct {
			Operation string             `json:"operation"`
			Recorded  time.Time          `json:"recorded_at"`
			Value     domain.Information `json:"value"`
		}{Operation: "create", Recorded: generatedAt, Value: asset}
		encodedLine, err := json.Marshal(entry)
		if err != nil {
			return 0, 0, 0, err
		}
		if _, err := assetWriter.Write(encodedLine); err != nil {
			return 0, 0, 0, err
		}
		if err := assetWriter.WriteByte('\n'); err != nil {
			return 0, 0, 0, err
		}

		vectors := record.Embedding
		record.Schema = "ownward.derived/v5"
		record.Embedding = nil
		metaBytes, err := json.Marshal(record)
		if err != nil {
			return 0, 0, 0, err
		}
		for i, v := range vectors {
			binary.LittleEndian.PutUint32(vecBuf[i*4:], math.Float32bits(v))
		}
		crc := crc32.NewIEEE()
		_, _ = crc.Write(metaBytes)
		_, _ = crc.Write(vecBuf)
		var head [16]byte
		copy(head[0:4], "OWD3")
		binary.LittleEndian.PutUint32(head[4:8], uint32(len(metaBytes)))
		binary.LittleEndian.PutUint32(head[8:12], uint32(len(vecBuf)))
		binary.LittleEndian.PutUint32(head[12:16], crc.Sum32())
		for _, part := range [][]byte{head[:], metaBytes, vecBuf, []byte("DONE")} {
			if _, err := derivedWriter.Write(part); err != nil {
				return 0, 0, 0, err
			}
			_, _ = derivedHasher.Write(part)
			derivedSealedBytes += int64(len(part))
		}
	}
	if err := errors.Join(assetWriter.Flush(), derivedWriter.Flush(), assetFile.Close(), derivedFile.Close()); err != nil {
		return 0, 0, 0, err
	}
	closed = true

	genManifest, err := json.Marshal(map[string]any{
		"schema":       "ownward.derived-generation/v1",
		"generation":   "gen-production",
		"sealed_bytes": derivedSealedBytes,
		"log_sha256":   hex.EncodeToString(derivedHasher.Sum(nil)),
	})
	if err != nil {
		return 0, 0, 0, err
	}
	if err := os.WriteFile(filepath.Join(genDir, "manifest.json"), genManifest, 0o600); err != nil {
		return 0, 0, 0, err
	}
	genManifestSum := sha256.Sum256(genManifest)
	currentBytes, err := json.Marshal(map[string]any{
		"schema":          "ownward.derived-current/v1",
		"generation":      "gen-production",
		"manifest_sha256": hex.EncodeToString(genManifestSum[:]),
	})
	if err != nil {
		return 0, 0, 0, err
	}
	if err := os.WriteFile(filepath.Join(dataDir, "state", "current.json"), currentBytes, 0o600); err != nil {
		return 0, 0, 0, err
	}

	budget, err := resourcebudget.New(20*resourcebudget.MiB, 4*resourcebudget.MiB)
	if err != nil {
		return 0, 0, 0, err
	}
	initControl := contract.ControlState{
		Schema: contract.ControlStateSchema, Revision: 1,
		ActiveComposition: "production-storage-fixture", ActiveKernelGeneration: "production-storage-kernel",
	}
	store, err := boundedstore.OpenDeployment(ctx, dataDir, boundedstore.DeploymentOptions{
		Options:     boundedstore.Options{Budget: budget},
		VectorSpace: spaceID,
		Initialize: func(ctx context.Context, s *boundedstore.Store, _ string) error {
			if _, err := s.OpenControlAuthority(ctx, initControl); err != nil {
				return err
			}
			return s.InitializeDeploymentSpace(ctx, spaceID)
		},
	})
	if err != nil {
		return 0, 0, 0, err
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "state")); !errors.Is(statErr, os.ErrNotExist) {
		_ = store.Close()
		if statErr != nil {
			return 0, 0, 0, statErr
		}
		return 0, 0, 0, errors.New("迁移完成后仍残留旧派生目录 state/，拒绝双重计入存储体积")
	}
	if err := store.DrainMaintenance(ctx); err != nil {
		_ = store.Close()
		return 0, 0, 0, err
	}
	for {
		packed, err := store.PackVectors(ctx, spaceID, false)
		if err != nil {
			_ = store.Close()
			return 0, 0, 0, err
		}
		if packed == 0 {
			break
		}
	}
	if _, err := store.PackVectors(ctx, spaceID, true); err != nil {
		_ = store.Close()
		return 0, 0, 0, err
	}
	if err := store.DrainMaintenance(ctx); err != nil {
		_ = store.Close()
		return 0, 0, 0, err
	}
	archiveDir, err := os.MkdirTemp(filepath.Dir(dataDir), "production-storage-authority-*")
	if err != nil {
		_ = store.Close()
		return 0, 0, 0, err
	}
	defer os.RemoveAll(archiveDir)
	fullArchivePath := filepath.Join(archiveDir, "deployment.zip")
	if err := store.ExportDeploymentArchive(ctx, fullArchivePath); err != nil {
		_ = store.Close()
		return 0, 0, 0, err
	}
	archivePath := filepath.Join(archiveDir, "authority.zip")
	if err := store.ExportArchive(ctx, archivePath); err != nil {
		_ = store.Close()
		return 0, 0, 0, err
	}
	if err := store.Close(); err != nil {
		return 0, 0, 0, err
	}
	fullSQLiteBytes, err := compactedArchiveSQLiteBytes(ctx, fullArchivePath, filepath.Join(archiveDir, "deployment-compacted.sqlite"))
	if err != nil {
		return 0, 0, 0, err
	}
	authorityBytes, err := archiveSQLiteBytes(archivePath)
	if err != nil {
		return 0, 0, 0, err
	}
	auxBytes, err := nonSQLiteDirectorySize(dataDir)
	if err != nil {
		return 0, 0, 0, err
	}
	storageBytes := fullSQLiteBytes + auxBytes
	derivedBytes := fullSQLiteBytes - authorityBytes
	if derivedBytes < 0 {
		derivedBytes = 0
	}
	return rawAssetBytes, derivedBytes, storageBytes, nil
}

func nonSQLiteDirectorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		base := entry.Name()
		if base == "ownward.sqlite" || base == "ownward.sqlite-wal" || base == "ownward.sqlite-shm" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func compactedArchiveSQLiteBytes(ctx context.Context, archivePath, scratchDBPath string) (int64, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	var found bool
	for _, f := range zr.File {
		if f.Name == "ownward.sqlite" {
			rc, err := f.Open()
			if err != nil {
				return 0, err
			}
			out, err := os.OpenFile(scratchDBPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				_ = rc.Close()
				return 0, err
			}
			_, copyErr := io.Copy(out, rc)
			err = errors.Join(copyErr, rc.Close(), out.Sync(), out.Close())
			if err != nil {
				return 0, err
			}
			found = true
			break
		}
	}
	if !found {
		return 0, errors.New("部署备份缺少 SQLite 数据文件")
	}
	db, err := sql.Open("sqlite", scratchDBPath)
	if err != nil {
		return 0, err
	}
	db.SetMaxOpenConns(1)
	_, execErr := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE); VACUUM; PRAGMA wal_checkpoint(TRUNCATE);")
	closeErr := db.Close()
	if err := errors.Join(execErr, closeErr); err != nil {
		return 0, err
	}
	info, err := os.Stat(scratchDBPath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func archiveSQLiteBytes(path string) (int64, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name == "ownward.sqlite" {
			return int64(f.UncompressedSize64), nil
		}
	}
	return 0, errors.New("权威备份缺少 SQLite 数据文件")
}

func performanceValues(index, dimensions int, spaceID string, generatedAt time.Time) (domain.Information, derived.Record) {
	id := fmt.Sprintf("I%06d", index)
	contexts := []domain.Context(nil)
	if index%10 == 0 {
		platform := "windows"
		if index%20 == 0 {
			platform = "linux"
		}
		contexts = []domain.Context{{Key: "platform", Value: platform}}
	}
	asset := domain.Information{
		Schema: domain.AssetSchema, ID: id, Revision: 1, CreatedAt: generatedAt, UpdatedAt: generatedAt, Kind: domain.KindKnowledge,
		Content: fmt.Sprintf("长期个人信息 %d，主题 bucket%d，包含可复用的经验、方法和解决路径。", index, index%100), Contexts: contexts,
	}
	inferredContexts := make([]semantics.InferredContext, 0, len(contexts))
	for _, context := range contexts {
		inferredContexts = append(inferredContexts, semantics.InferredContext{Key: context.Key, Value: context.Value, Confidence: 1, Evidence: "performance fixture"})
	}
	analysis := semantics.Analysis{Contexts: inferredContexts}
	if index > 0 {
		analysis.Relations = []semantics.Relation{{Type: "related_to", TargetID: fmt.Sprintf("I%06d", index-1), Confidence: 0.95, Evidence: "performance fixture"}}
	}
	record := derived.Record{
		AssetID: id, AssetRevision: 1, GeneratedAt: generatedAt, Provider: "performance-fixture", Status: "ready",
		InputsKnown: true, Analysis: analysis, EmbeddingSpace: spaceID, Embedding: deterministicVector(index, dimensions),
	}
	return asset, record
}

func deterministicVector(seed, dimensions int) []float32 {
	vector := make([]float32, dimensions)
	state := uint64(seed + 1)
	length := float64(0)
	for index := range vector {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		value := float32(int64(state%2_000_001)-1_000_000) / 1_000_000
		vector[index] = value
		length += float64(value * value)
	}
	divisor := float32(math.Sqrt(length))
	for index := range vector {
		vector[index] /= divisor
	}
	return vector
}
