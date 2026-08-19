package performance

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/candidate"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/retrieval"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/systemmetrics"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Options struct {
	Scale      int
	Dimensions int
	Iterations int
	Thresholds string
	BinaryPath string
	Candidate  string
}

type Check struct {
	Name    string  `json:"name"`
	Passed  bool    `json:"passed"`
	Actual  float64 `json:"actual"`
	Maximum float64 `json:"maximum"`
	Unit    string  `json:"unit"`
}

type Distribution struct {
	Samples int     `json:"samples"`
	P50MS   float64 `json:"p50_ms"`
	P95MS   float64 `json:"p95_ms"`
	P99MS   float64 `json:"p99_ms"`
	MaxMS   float64 `json:"max_ms"`
}

type Report struct {
	Schema          string                  `json:"schema"`
	Candidate       string                  `json:"candidate"`
	BinarySHA256    string                  `json:"release_binary_sha256"`
	BinaryVersion   string                  `json:"release_binary_version"`
	ThresholdSHA256 string                  `json:"thresholds_sha256,omitempty"`
	MeasuredAt      time.Time               `json:"measured_at"`
	OS              string                  `json:"os"`
	Arch            string                  `json:"arch"`
	CPUs            int                     `json:"cpus"`
	Scale           int                     `json:"scale"`
	Dimensions      int                     `json:"dimensions"`
	ReleaseMiB      float64                 `json:"release_binary_mib"`
	IdleRSSMiB      float64                 `json:"idle_rss_mib"`
	RSSMiB          float64                 `json:"rss_mib"`
	HeapAllocMiB    float64                 `json:"heap_alloc_mib"`
	HeapSysMiB      float64                 `json:"heap_sys_mib"`
	IdleCPUPercent  float64                 `json:"idle_cpu_percent"`
	RawAssetMiB     float64                 `json:"raw_asset_mib"`
	VectorMiB       float64                 `json:"vector_mib"`
	DerivedMiB      float64                 `json:"derived_storage_mib"`
	StorageMiB      float64                 `json:"storage_mib_at_scale"`
	DerivedRatio    float64                 `json:"derived_storage_over_raw_plus_vectors_ratio"`
	Latency         map[string]Distribution `json:"latency"`
	Build           map[string]Distribution `json:"build"`
	Thresholds      string                  `json:"thresholds,omitempty"`
	Comparable      bool                    `json:"comparable"`
	Passed          bool                    `json:"passed"`
	Checks          []Check                 `json:"checks,omitempty"`
}

type limits struct {
	Retrieval struct {
		ExplicitObject struct {
			P95 float64 `json:"p95_ms_max_at_100k"`
		} `json:"explicit_object"`
		SemanticIntent struct {
			P95 float64 `json:"p95_ms_max_at_100k_excluding_query_embedding"`
		} `json:"semantic_intent"`
		RelationConstraint struct {
			P95 float64 `json:"p95_ms_max_at_100k"`
		} `json:"relation_constraint"`
		ContextApplicability struct {
			P95 float64 `json:"p95_ms_max_at_100k_excluding_query_embedding"`
		} `json:"context_applicability"`
	} `json:"retrieval"`
	Resources struct {
		ReleaseMiB   float64 `json:"release_binary_mib_max"`
		IdleRSSMiB   float64 `json:"idle_rss_mib_max"`
		RSSMiB       float64 `json:"rss_mib_max_at_100k_384d"`
		IdleCPU      float64 `json:"idle_cpu_percent_max"`
		DerivedRatio float64 `json:"derived_storage_over_raw_plus_vectors_ratio_max"`
	} `json:"resources"`
	Ingestion struct {
		DurableWriteMS    float64 `json:"durable_write_p95_ms_max"`
		BasicSearchableMS float64 `json:"basic_searchable_p95_ms_max"`
	} `json:"ingestion"`
}

func Run(ctx context.Context, options Options) (Report, error) {
	if options.Scale <= 0 {
		options.Scale = 100_000
	}
	if options.Dimensions <= 0 {
		options.Dimensions = 384
	}
	if options.Iterations <= 0 {
		options.Iterations = 100
	}
	if options.Dimensions > 8192 {
		return Report{}, fmt.Errorf("向量维度不能超过 8192")
	}
	if options.Thresholds != "" && strings.TrimSpace(options.BinaryPath) == "" {
		return Report{}, fmt.Errorf("带阈值的性能验收必须提供发布二进制文件")
	}
	releaseMiB := 0.0
	binarySHA256 := ""
	binaryVersion := ""
	if strings.TrimSpace(options.BinaryPath) != "" {
		binary, inspectErr := candidate.Inspect(ctx, options.BinaryPath, options.Candidate)
		if inspectErr != nil {
			return Report{}, inspectErr
		}
		releaseMiB = float64(binary.Size) / (1024 * 1024)
		binarySHA256 = binary.SHA256
		binaryVersion = binary.Version
	}
	var idleRSS, loadedRSS uint64
	var idleCPU float64
	var rawAssetBytes, derivedBytes, storageBytes int64
	if strings.TrimSpace(options.BinaryPath) != "" {
		emptyDir, err := os.MkdirTemp("", "ownward-performance-empty-*")
		if err != nil {
			return Report{}, err
		}
		idleRSS, _, err = measureRelease(ctx, options.BinaryPath, emptyDir)
		_ = os.RemoveAll(emptyDir)
		if err != nil {
			return Report{}, err
		}
		fixtureRoot, err := os.MkdirTemp("", "ownward-performance-loaded-*")
		if err != nil {
			return Report{}, err
		}
		defer os.RemoveAll(fixtureRoot)
		dataDir := filepath.Join(fixtureRoot, "data")
		rawAssetBytes, derivedBytes, storageBytes, err = prepareReleaseFixture(ctx, dataDir, options.Scale, options.Dimensions)
		if err != nil {
			return Report{}, err
		}
		loadedRSS, idleCPU, err = measureRelease(ctx, options.BinaryPath, dataDir)
		if err != nil {
			return Report{}, err
		}
	}
	assets := make([]domain.Information, options.Scale)
	records := make([]derived.Record, options.Scale)
	generatedAt := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	for index := 0; index < options.Scale; index++ {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		assets[index], records[index] = performanceValues(index, options.Dimensions, generatedAt)
		if strings.TrimSpace(options.BinaryPath) == "" {
			encodedAsset, err := json.Marshal(assets[index])
			if err != nil {
				return Report{}, err
			}
			rawAssetBytes += int64(len(encodedAsset) + 1)
			encodedDerived, err := derived.EncodeRecord(records[index])
			if err != nil {
				return Report{}, err
			}
			derivedBytes += int64(len(encodedDerived))
		}
	}
	lexicalBuildStarted := time.Now()
	lexical := retrieval.NewLexical(assets)
	lexicalBuild := time.Since(lexicalBuildStarted)
	queryVector := append([]float32(nil), records[options.Scale/2].Embedding...)
	semanticBuildStarted := time.Now()
	semantic := derived.NewIndex(records)
	semanticBuild := time.Since(semanticBuildStarted)
	assetAuthority := make(map[string]domain.Information, len(assets))
	for _, asset := range assets {
		assetAuthority[asset.ID] = asset
	}
	derivedAuthority := make(map[string]derived.Record, len(records))
	for _, record := range records {
		derivedAuthority[record.AssetID] = record
	}
	assets = nil
	records = nil
	runtime.GC()
	if loadedRSS == 0 {
		currentRSS, err := systemmetrics.RSSBytes()
		if err != nil {
			return Report{}, err
		}
		loadedRSS = currentRSS
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	report := Report{
		Schema: "ownward.performance-report/v4", Candidate: strings.TrimSpace(options.Candidate),
		BinarySHA256: binarySHA256, BinaryVersion: binaryVersion, MeasuredAt: time.Now().UTC(),
		OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), Scale: options.Scale, Dimensions: options.Dimensions,
		ReleaseMiB: releaseMiB, IdleRSSMiB: float64(idleRSS) / (1024 * 1024),
		RSSMiB: float64(loadedRSS) / (1024 * 1024), HeapAllocMiB: float64(memory.HeapAlloc) / (1024 * 1024), HeapSysMiB: float64(memory.HeapSys) / (1024 * 1024),
		RawAssetMiB: float64(rawAssetBytes) / (1024 * 1024), VectorMiB: float64(options.Scale*options.Dimensions*4) / (1024 * 1024),
		DerivedMiB: float64(derivedBytes) / (1024 * 1024), StorageMiB: float64(storageBytes) / (1024 * 1024),
		DerivedRatio: float64(derivedBytes) / float64(rawAssetBytes+int64(options.Scale*options.Dimensions*4)),
		Latency:      make(map[string]Distribution), Build: make(map[string]Distribution),
		IdleCPUPercent: idleCPU,
	}
	report.Build["lexical_index"] = distribution([]time.Duration{lexicalBuild})
	report.Build["semantic_index"] = distribution([]time.Duration{semanticBuild})
	report.Latency["explicit_object"] = measure(options.Iterations*10, func(index int) {
		_ = lexical.Search(fmt.Sprintf("I%06d", (index*7919)%options.Scale), nil, 10)
	})
	report.Latency["lexical_intent"] = measure(options.Iterations, func(index int) {
		_ = lexical.Search(fmt.Sprintf("bucket%d", index%100), nil, 10)
	})
	report.Latency["semantic_intent"] = measure(options.Iterations, func(int) {
		_ = semantic.Search(queryVector, nil, 10)
	})
	report.Latency["context_applicability"] = measure(options.Iterations, func(int) {
		_ = semantic.Search(queryVector, []domain.Context{{Key: "platform", Value: "windows"}}, 10)
	})
	report.Latency["relation_navigation"] = measure(options.Iterations*10, func(index int) {
		start := fmt.Sprintf("I%06d", options.Scale-1-index%1000)
		_ = semantic.Navigate([]string{start}, nil, 3, 50)
	})
	report.Latency["semantic_concurrency_8"] = measureConcurrent(8, options.Iterations, func() {
		_ = semantic.Search(queryVector, nil, 10)
	})
	durableWrite, basicSearchable, persistenceErr := measurePersistence(ctx, options.Iterations)
	if persistenceErr != nil {
		return Report{}, persistenceErr
	}
	report.Latency["durable_write"] = durableWrite
	report.Latency["basic_searchable"] = basicSearchable
	if options.Thresholds != "" {
		encoded, readErr := os.ReadFile(options.Thresholds)
		if readErr != nil {
			return Report{}, fmt.Errorf("读取性能阈值: %w", readErr)
		}
		var thresholds limits
		if unmarshalErr := json.Unmarshal(encoded, &thresholds); unmarshalErr != nil {
			return Report{}, fmt.Errorf("解析性能阈值: %w", unmarshalErr)
		}
		report.Thresholds = options.Thresholds
		digest := sha256.Sum256(encoded)
		report.ThresholdSHA256 = fmt.Sprintf("%x", digest)
		report.Comparable = options.Scale == 100_000 && options.Dimensions == 384
		report.Checks = evaluate(report, thresholds)
		report.Passed = report.Comparable
		for _, check := range report.Checks {
			if !check.Passed {
				report.Passed = false
			}
		}
	}
	runtime.KeepAlive(assetAuthority)
	runtime.KeepAlive(derivedAuthority)
	runtime.KeepAlive(lexical)
	runtime.KeepAlive(semantic)
	return report, nil
}

func measureRelease(ctx context.Context, binaryPath, dataDir string) (uint64, float64, error) {
	if strings.TrimSpace(binaryPath) == "" {
		return 0, 0, nil
	}
	command := exec.CommandContext(ctx, binaryPath, "mcp", "--data-dir", dataDir)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "ownward-performance", Version: "1"}, nil)
	connectCtx, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	session, err := client.Connect(connectCtx, &mcp.CommandTransport{Command: command}, nil)
	cancelConnect()
	if err != nil {
		return 0, 0, fmt.Errorf("初始化发布二进制: %w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	defer func() { _ = session.Close() }()
	if command.Process == nil {
		return 0, 0, errors.New("发布二进制初始化后没有可采样进程")
	}
	rssBefore, cpuBefore, err := systemmetrics.SampleProcess(command.Process.Pid)
	if err != nil {
		return 0, 0, fmt.Errorf("读取发布二进制初始指标: %w", err)
	}
	idleStarted := time.Now()
	select {
	case <-ctx.Done():
		return 0, 0, ctx.Err()
	case <-time.After(3 * time.Second):
	}
	idleWall := time.Since(idleStarted)
	rssAfter, cpuAfter, err := systemmetrics.SampleProcess(command.Process.Pid)
	if err != nil {
		return 0, 0, fmt.Errorf("读取发布二进制空载指标: %w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	if rssAfter > rssBefore {
		rssBefore = rssAfter
	}
	idleCPU := (cpuAfter - cpuBefore).Seconds() / idleWall.Seconds() / float64(runtime.NumCPU()) * 100
	return rssBefore, idleCPU, nil
}

func prepareReleaseFixture(ctx context.Context, dataDir string, scale, dimensions int) (int64, int64, int64, error) {
	assetsDir := filepath.Join(dataDir, "assets")
	stateDir := filepath.Join(dataDir, "state")
	if err := os.MkdirAll(assetsDir, 0o700); err != nil {
		return 0, 0, 0, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return 0, 0, 0, err
	}
	generatedAt := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	manifest, err := json.MarshalIndent(struct {
		Format    string    `json:"format"`
		CreatedAt time.Time `json:"created_at"`
	}{Format: domain.AssetSchema, CreatedAt: generatedAt}, "", "  ")
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
	derivedFile, err := os.OpenFile(filepath.Join(stateDir, derived.LogFileName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
	closeFiles := func() error {
		var first error
		if err := assetWriter.Flush(); err != nil && first == nil {
			first = err
		}
		if err := derivedWriter.Flush(); err != nil && first == nil {
			first = err
		}
		if err := assetFile.Close(); err != nil && first == nil {
			first = err
		}
		if err := derivedFile.Close(); err != nil && first == nil {
			first = err
		}
		closed = true
		return first
	}
	var rawAssetBytes, derivedBytes int64
	for index := 0; index < scale; index++ {
		if err := ctx.Err(); err != nil {
			return 0, 0, 0, err
		}
		asset, record := performanceValues(index, dimensions, generatedAt)
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
		if err := writeJSONLine(assetWriter, entry); err != nil {
			return 0, 0, 0, err
		}
		encodedDerived, err := derived.EncodeRecord(record)
		if err != nil {
			return 0, 0, 0, err
		}
		derivedBytes += int64(len(encodedDerived))
		if _, err := derivedWriter.Write(encodedDerived); err != nil {
			return 0, 0, 0, err
		}
	}
	if err := closeFiles(); err != nil {
		return 0, 0, 0, err
	}
	storageBytes, err := directorySize(dataDir)
	if err != nil {
		return 0, 0, 0, err
	}
	return rawAssetBytes, derivedBytes, storageBytes, nil
}

func directorySize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func performanceValues(index, dimensions int, generatedAt time.Time) (domain.Information, derived.Record) {
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
		analysis.Relations = []semantics.Relation{{Type: "related_to", TargetID: fmt.Sprintf("I%06d", index-1), Confidence: 0.95}}
	}
	record := derived.Record{
		AssetID: id, AssetRevision: 1, GeneratedAt: generatedAt, Provider: "performance-fixture", Status: "ready",
		Analysis: analysis, Embedding: deterministicVector(index, dimensions),
	}
	return asset, record
}

func writeJSONLine(writer *bufio.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := writer.Write(encoded); err != nil {
		return err
	}
	return writer.WriteByte('\n')
}

func evaluate(report Report, thresholds limits) []Check {
	check := func(name string, actual, maximum float64, unit string) Check {
		return Check{Name: name, Passed: maximum > 0 && actual <= maximum, Actual: actual, Maximum: maximum, Unit: unit}
	}
	return []Check{
		check("明确对象检索 P95", report.Latency["explicit_object"].P95MS, thresholds.Retrieval.ExplicitObject.P95, "ms"),
		check("语义意图检索 P95", report.Latency["semantic_intent"].P95MS, thresholds.Retrieval.SemanticIntent.P95, "ms"),
		check("关系导航 P95", report.Latency["relation_navigation"].P95MS, thresholds.Retrieval.RelationConstraint.P95, "ms"),
		check("场景适用性检索 P95", report.Latency["context_applicability"].P95MS, thresholds.Retrieval.ContextApplicability.P95, "ms"),
		check("并发八路语义检索 P95", report.Latency["semantic_concurrency_8"].P95MS, thresholds.Retrieval.SemanticIntent.P95, "ms"),
		check("持久写入 P95", report.Latency["durable_write"].P95MS, thresholds.Ingestion.DurableWriteMS, "ms"),
		check("基础可检索 P95", report.Latency["basic_searchable"].P95MS, thresholds.Ingestion.BasicSearchableMS, "ms"),
		check("发布二进制体积", report.ReleaseMiB, thresholds.Resources.ReleaseMiB, "MiB"),
		check("空载常驻内存", report.IdleRSSMiB, thresholds.Resources.IdleRSSMiB, "MiB"),
		check("十万条 384 维常驻内存", report.RSSMiB, thresholds.Resources.RSSMiB, "MiB"),
		check("空闲 CPU", report.IdleCPUPercent, thresholds.Resources.IdleCPU, "%"),
		check("派生状态存储比", report.DerivedRatio, thresholds.Resources.DerivedRatio, "ratio"),
	}
}

func measurePersistence(ctx context.Context, iterations int) (Distribution, Distribution, error) {
	root, err := os.MkdirTemp("", "ownward-performance-persistence-*")
	if err != nil {
		return Distribution{}, Distribution{}, err
	}
	defer os.RemoveAll(root)

	durableStore, err := assetlog.Open(filepath.Join(root, "durable"))
	if err != nil {
		return Distribution{}, Distribution{}, err
	}
	durable := make([]time.Duration, iterations)
	createdAt := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	for index := 0; index < iterations; index++ {
		if err := ctx.Err(); err != nil {
			_ = durableStore.Close()
			return Distribution{}, Distribution{}, err
		}
		value := domain.Information{
			Schema: domain.AssetSchema, ID: fmt.Sprintf("durable-%06d", index), Revision: 1,
			CreatedAt: createdAt, UpdatedAt: createdAt, Kind: domain.KindKnowledge,
			Content: fmt.Sprintf("持久写入性能样本 %d", index),
		}
		started := time.Now()
		if err := durableStore.Create(value); err != nil {
			_ = durableStore.Close()
			return Distribution{}, Distribution{}, err
		}
		durable[index] = time.Since(started)
	}
	if err := durableStore.Close(); err != nil {
		return Distribution{}, Distribution{}, err
	}

	searchStore, err := assetlog.Open(filepath.Join(root, "searchable"))
	if err != nil {
		return Distribution{}, Distribution{}, err
	}
	service := core.New(searchStore)
	searchable := make([]time.Duration, iterations)
	lastID := ""
	for index := 0; index < iterations; index++ {
		started := time.Now()
		created, createErr := service.Create(ctx, core.CreateInput{Kind: domain.KindKnowledge, Content: fmt.Sprintf("基础可检索性能样本 %d", index)})
		searchable[index] = time.Since(started)
		if createErr != nil {
			_ = service.Close()
			return Distribution{}, Distribution{}, createErr
		}
		lastID = created.Information.ID
	}
	results, searchErr := service.Search(ctx, core.SearchInput{Query: lastID, Limit: 1})
	if searchErr != nil || len(results) != 1 || results[0].ID != lastID {
		_ = service.Close()
		return Distribution{}, Distribution{}, fmt.Errorf("持久信息未达到基础可检索状态")
	}
	if err := service.Close(); err != nil {
		return Distribution{}, Distribution{}, err
	}
	return distribution(durable), distribution(searchable), nil
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

func measure(iterations int, operation func(int)) Distribution {
	values := make([]time.Duration, iterations)
	for index := 0; index < iterations; index++ {
		start := time.Now()
		operation(index)
		values[index] = time.Since(start)
	}
	return distribution(values)
}

func measureConcurrent(workers, iterations int, operation func()) Distribution {
	values := make([]time.Duration, workers*iterations)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for index := 0; index < iterations; index++ {
				start := time.Now()
				operation()
				values[worker*iterations+index] = time.Since(start)
			}
		}(worker)
	}
	wait.Wait()
	return distribution(values)
}

func distribution(values []time.Duration) Distribution {
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	quantile := func(value float64) float64 {
		index := int(math.Ceil(float64(len(values))*value)) - 1
		if index < 0 {
			index = 0
		}
		return float64(values[index].Nanoseconds()) / 1_000_000
	}
	return Distribution{Samples: len(values), P50MS: quantile(0.5), P95MS: quantile(0.95), P99MS: quantile(0.99), MaxMS: quantile(1)}
}
