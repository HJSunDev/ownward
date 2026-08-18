package performance

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/retrieval"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/systemmetrics"
)

type Options struct {
	Scale      int
	Dimensions int
	Iterations int
	Thresholds string
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
	Schema         string                  `json:"schema"`
	MeasuredAt     time.Time               `json:"measured_at"`
	OS             string                  `json:"os"`
	Arch           string                  `json:"arch"`
	CPUs           int                     `json:"cpus"`
	Scale          int                     `json:"scale"`
	Dimensions     int                     `json:"dimensions"`
	RSSMiB         float64                 `json:"rss_mib"`
	HeapAllocMiB   float64                 `json:"heap_alloc_mib"`
	HeapSysMiB     float64                 `json:"heap_sys_mib"`
	IdleCPUPercent float64                 `json:"idle_cpu_percent"`
	RawAssetMiB    float64                 `json:"raw_asset_mib"`
	VectorMiB      float64                 `json:"vector_mib"`
	DerivedMiB     float64                 `json:"derived_storage_mib"`
	DerivedRatio   float64                 `json:"derived_storage_over_raw_plus_vectors_ratio"`
	Latency        map[string]Distribution `json:"latency"`
	Build          map[string]Distribution `json:"build"`
	Thresholds     string                  `json:"thresholds,omitempty"`
	Comparable     bool                    `json:"comparable"`
	Passed         bool                    `json:"passed"`
	Checks         []Check                 `json:"checks,omitempty"`
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
		RSSMiB       float64 `json:"rss_mib_max_at_100k_384d"`
		IdleCPU      float64 `json:"idle_cpu_percent_max"`
		DerivedRatio float64 `json:"derived_storage_over_raw_plus_vectors_ratio_max"`
	} `json:"resources"`
	Ingestion struct {
		DurableWriteMS    float64 `json:"durable_write_p95_ms_max"`
		BasicSearchableMS float64 `json:"basic_searchable_p95_ms_max"`
	} `json:"ingestion"`
}

type persistedDerivedApproximation struct {
	Schema        string             `json:"schema"`
	AssetID       string             `json:"asset_id"`
	AssetRevision uint64             `json:"asset_revision"`
	GeneratedAt   time.Time          `json:"generated_at"`
	Provider      string             `json:"provider"`
	Status        string             `json:"status"`
	Analysis      semantics.Analysis `json:"analysis"`
	Embedding     []byte             `json:"embedding_f32le"`
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
	assets := make([]domain.Information, options.Scale)
	records := make([]derived.Record, options.Scale)
	generatedAt := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	var rawAssetBytes, derivedBytes int64
	for index := 0; index < options.Scale; index++ {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		id := fmt.Sprintf("I%06d", index)
		contexts := []domain.Context(nil)
		if index%10 == 0 {
			platform := "windows"
			if index%20 == 0 {
				platform = "linux"
			}
			contexts = []domain.Context{{Key: "platform", Value: platform}}
		}
		assets[index] = domain.Information{
			Schema: domain.AssetSchema, ID: id, Revision: 1, CreatedAt: generatedAt, UpdatedAt: generatedAt, Kind: domain.KindKnowledge,
			Content:  fmt.Sprintf("长期个人信息 %d，主题 bucket%d，包含可复用的经验、方法和解决路径。", index, index%100),
			Contexts: contexts,
		}
		analysis := semantics.Analysis{Kind: domain.KindKnowledge, Contexts: contexts}
		if index > 0 {
			analysis.Relations = []semantics.Relation{{Type: "related_to", TargetID: fmt.Sprintf("I%06d", index-1), Confidence: 0.95}}
		}
		records[index] = derived.Record{AssetID: id, AssetRevision: 1, GeneratedAt: generatedAt, Provider: "performance-fixture", Status: "ready", Analysis: analysis, Embedding: deterministicVector(index, options.Dimensions)}
		encodedAsset, err := json.Marshal(assets[index])
		if err != nil {
			return Report{}, err
		}
		rawAssetBytes += int64(len(encodedAsset) + 1)
		vectorBytes := make([]byte, len(records[index].Embedding)*4)
		for position, value := range records[index].Embedding {
			binary.LittleEndian.PutUint32(vectorBytes[position*4:], math.Float32bits(value))
		}
		encodedDerived, err := json.Marshal(persistedDerivedApproximation{
			Schema: "ownward.derived/v2", AssetID: id, AssetRevision: 1, GeneratedAt: generatedAt,
			Provider: "performance-fixture", Status: "ready", Analysis: analysis, Embedding: vectorBytes,
		})
		if err != nil {
			return Report{}, err
		}
		derivedBytes += int64(len(encodedDerived) + 1)
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
	debug.FreeOSMemory()
	rss, err := systemmetrics.RSSBytes()
	if err != nil {
		return Report{}, err
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	report := Report{
		Schema: "ownward.performance-report/v1", MeasuredAt: time.Now().UTC(),
		OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), Scale: options.Scale, Dimensions: options.Dimensions,
		RSSMiB: float64(rss) / (1024 * 1024), HeapAllocMiB: float64(memory.HeapAlloc) / (1024 * 1024), HeapSysMiB: float64(memory.HeapSys) / (1024 * 1024),
		RawAssetMiB: float64(rawAssetBytes) / (1024 * 1024), VectorMiB: float64(options.Scale*options.Dimensions*4) / (1024 * 1024),
		DerivedMiB: float64(derivedBytes) / (1024 * 1024), DerivedRatio: float64(derivedBytes) / float64(rawAssetBytes+int64(options.Scale*options.Dimensions*4)),
		Latency: make(map[string]Distribution), Build: make(map[string]Distribution),
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
	cpuStart, err := systemmetrics.CPUTime()
	if err != nil {
		return Report{}, err
	}
	idleStart := time.Now()
	time.Sleep(3 * time.Second)
	idleWall := time.Since(idleStart)
	cpuEnd, err := systemmetrics.CPUTime()
	if err != nil {
		return Report{}, err
	}
	report.IdleCPUPercent = (cpuEnd - cpuStart).Seconds() / idleWall.Seconds() / float64(runtime.NumCPU()) * 100
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
