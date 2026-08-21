package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/retrieval"
	"github.com/HJSunDev/ownward/internal/semantics"
)

const (
	suiteVersion      = "1.0.0"
	repeatabilityRuns = 3
)

type contextValue struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type assetValue struct {
	FixtureID string         `json:"fixture_id"`
	Content   string         `json:"content"`
	Contexts  []contextValue `json:"contexts"`
}

type relationValue struct {
	SourceID string `json:"source_id"`
	Type     string `json:"type"`
	TargetID string `json:"target_id"`
}

type queryValue struct {
	QueryID              string         `json:"query_id"`
	Type                 string         `json:"type"`
	Query                string         `json:"query"`
	Contexts             []contextValue `json:"contexts"`
	ExpectedIDs          []string       `json:"expected_ids"`
	ForbiddenIDs         []string       `json:"forbidden_ids"`
	RequiredRelationPath []string       `json:"required_relation_path"`
}

type dataset struct {
	Schema          string       `json:"schema"`
	Version         string       `json:"version"`
	Scales          []int        `json:"scales"`
	Assets          []assetValue `json:"assets"`
	ChangeSequences []assetValue `json:"change_sequences"`
	Queries         []queryValue `json:"queries"`
	Truth           struct {
		Relations []relationValue `json:"relations"`
	} `json:"truth"`
	FrozenEmbeddings      map[string][]float32 `json:"frozen_embeddings"`
	FrozenQueryEmbeddings map[string][]float32 `json:"frozen_query_embeddings"`
	FrozenSemantics       struct {
		Relations []relationValue           `json:"relations"`
		Contexts  map[string][]contextValue `json:"contexts"`
	} `json:"frozen_semantics"`
	DeterministicVariants struct {
		Seed       int64    `json:"seed"`
		Count      int      `json:"count"`
		Operations []string `json:"operations"`
	} `json:"deterministic_variants"`
}

type metric struct {
	Name               string  `json:"name"`
	Dimension          string  `json:"dimension"`
	Stage              string  `json:"stage"`
	Value              float64 `json:"value"`
	Direction          string  `json:"direction"`
	RepeatabilityError float64 `json:"repeatability_error"`
	Materiality        float64 `json:"materiality"`
	Protected          bool    `json:"protected"`
}

type observation struct {
	Schema              string         `json:"schema"`
	SuiteVersion        string         `json:"suite_version"`
	Candidate           string         `json:"candidate"`
	MaterialsSHA256     string         `json:"materials_sha256"`
	InputManifestSHA256 string         `json:"input_manifest_sha256"`
	Mode                string         `json:"mode"`
	RequestedStages     []string       `json:"requested_stages"`
	Environment         map[string]any `json:"environment"`
	ToolSHA256          string         `json:"tool_sha256"`
	Metrics             []metric       `json:"metrics"`
	StartedAt           time.Time      `json:"started_at"`
	FinishedAt          time.Time      `json:"finished_at"`
}

type sampleSpec struct {
	Dimension string
	Stage     string
	Direction string
	Values    []float64
}

type collector map[string]*sampleSpec

func main() {
	var materials, candidate, mode, environmentSHA, inputSHA, output, repository, stagesCSV string
	var selfCheck bool
	flag.StringVar(&materials, "materials", "", "固定内核基准材料")
	flag.StringVar(&candidate, "candidate", "", "候选提交")
	flag.StringVar(&mode, "mode", "", "targeted 或 full")
	flag.StringVar(&environmentSHA, "environment-sha256", "", "环境摘要")
	flag.StringVar(&inputSHA, "input-manifest-sha256", "", "输入清单摘要")
	flag.StringVar(&output, "output", "", "观察报告")
	flag.StringVar(&repository, "repository", ".", "候选仓库")
	flag.StringVar(&stagesCSV, "stages", "", "定向阶段，逗号分隔")
	flag.BoolVar(&selfCheck, "self-check", false, "允许在未提交工作树上执行非正式体系自检")
	flag.Parse()
	if err := run(materials, candidate, mode, environmentSHA, inputSHA, output, repository, stagesCSV, selfCheck); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-frontier:", err)
		os.Exit(2)
	}
}

func run(materialsPath, candidate, mode, environmentSHA, inputSHA, outputPath, repository, stagesCSV string, selfCheck bool) error {
	if mode != "targeted" && mode != "full" {
		return errors.New("mode 必须是 targeted 或 full")
	}
	if strings.TrimSpace(candidate) == "" || !validSHA(environmentSHA) || !validSHA(inputSHA) || outputPath == "" {
		return errors.New("候选、环境、输入和输出绑定不完整")
	}
	if err := verifyCandidate(repository, candidate); err != nil {
		return err
	}
	if err := verifySelf(candidate, selfCheck); err != nil {
		return err
	}
	encoded, err := os.ReadFile(materialsPath)
	if err != nil {
		return err
	}
	var data dataset
	if err := json.Unmarshal(encoded, &data); err != nil {
		return err
	}
	if err := validateDataset(data); err != nil {
		return err
	}
	requested, err := requestedStages(mode, stagesCSV)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return err
	}
	scratchRoot, err := os.MkdirTemp(filepath.Dir(outputPath), ".frontier-work-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratchRoot)
	started := time.Now().UTC()
	scales := data.Scales
	variants := data.DeterministicVariants.Count
	if mode == "targeted" {
		scales = []int{data.Scales[len(data.Scales)-1]}
		variants = 1
	}
	runs := make([]collector, 0, repeatabilityRuns)
	for repetition := 0; repetition < repeatabilityRuns; repetition++ {
		values := make(collector)
		for _, scale := range scales {
			for variant := 0; variant < variants; variant++ {
				fixture, err := buildFixture(data, scale, variant)
				if err != nil {
					return err
				}
				if err := measureFixture(context.Background(), fixture, requested, values, scratchRoot); err != nil {
					return fmt.Errorf("重复 %d 规模 %d 变体 %d: %w", repetition+1, scale, variant, err)
				}
			}
		}
		runs = append(runs, values)
	}
	metrics, err := metricsFromRuns(runs)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	toolSHA, err := fileSHA(executable)
	if err != nil {
		return err
	}
	report := observation{
		Schema: "ownward.core-frontier-observation/v1", SuiteVersion: suiteVersion, Candidate: candidate,
		MaterialsSHA256: digest(encoded), InputManifestSHA256: inputSHA, Mode: mode,
		RequestedStages: sortedStageNames(requested),
		Environment:     map[string]any{"sha256": environmentSHA, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "cpus": runtime.NumCPU()},
		ToolSHA256:      toolSHA, Metrics: metrics, StartedAt: started, FinishedAt: time.Now().UTC(),
	}
	if len(report.Metrics) == 0 {
		return errors.New("观察报告没有指标")
	}
	return writeJSON(outputPath, report)
}

func sortedStageNames(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

type fixture struct {
	Assets       []domain.Information
	Records      []derived.Record
	Queries      []queryValue
	QueryVectors map[string][]float32
	Truth        []relationValue
	Changes      []assetValue
	OriginalMap  map[string]string
}

func buildFixture(data dataset, scale, variant int) (fixture, error) {
	if scale <= 0 || scale > len(data.Assets) {
		return fixture{}, errors.New("固定规模无效")
	}
	selected := data.Assets[:scale]
	ids := make(map[string]string, scale)
	for _, value := range selected {
		ids[value.FixtureID] = fmt.Sprintf("V%02d-%s", variant+1, value.FixtureID)
	}
	created := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(variant) * 24 * time.Hour)
	assets := make([]domain.Information, 0, scale+2)
	records := make([]derived.Record, 0, scale+2)
	relations := make([]relationValue, 0)
	for _, relation := range data.FrozenSemantics.Relations {
		if ids[relation.SourceID] != "" && ids[relation.TargetID] != "" {
			relations = append(relations, relationValue{SourceID: ids[relation.SourceID], Type: relation.Type, TargetID: ids[relation.TargetID]})
		}
	}
	for _, value := range selected {
		contexts := convertContexts(value.Contexts)
		if variant%2 == 1 {
			reverseContexts(contexts)
		}
		id := ids[value.FixtureID]
		asset := domain.Information{Schema: domain.AssetSchema, ID: id, Revision: 1, CreatedAt: created, UpdatedAt: created, Kind: domain.KindGeneral, Content: value.Content, Contexts: contexts}
		assets = append(assets, asset)
		analysis := semantics.Analysis{Contexts: inferredContexts(data.FrozenSemantics.Contexts[value.FixtureID])}
		for _, relation := range relations {
			if relation.SourceID == id {
				analysis.Relations = append(analysis.Relations, semantics.Relation{Type: relation.Type, TargetID: relation.TargetID, TargetRevision: 1, Confidence: 1, Evidence: "frozen truth"})
			}
		}
		records = append(records, derived.Record{AssetID: id, AssetRevision: 1, GeneratedAt: created, Provider: "frozen-frontier-materials", Status: "ready", Analysis: analysis, EmbeddingSpace: "frozen-frontier-v1", Embedding: append([]float32(nil), data.FrozenEmbeddings[value.FixtureID]...)})
	}
	for index := 0; index < 2; index++ {
		id := fmt.Sprintf("V%02d-D%02d", variant+1, index+1)
		content := fmt.Sprintf("无关干扰信息 %d，只用于验证候选不会污染真实查询。", index+1)
		vector := make([]float32, 16)
		vector[(variant+index)%len(vector)] = 1
		assets = append(assets, domain.Information{Schema: domain.AssetSchema, ID: id, Revision: 1, CreatedAt: created, UpdatedAt: created, Kind: domain.KindGeneral, Content: content})
		records = append(records, derived.Record{AssetID: id, AssetRevision: 1, GeneratedAt: created, Provider: "frozen-frontier-materials", Status: "ready", EmbeddingSpace: "frozen-frontier-v1", Embedding: vector})
	}
	random := rand.New(rand.NewSource(data.DeterministicVariants.Seed + int64(variant)))
	random.Shuffle(len(assets), func(left, right int) { assets[left], assets[right] = assets[right], assets[left] })
	random.Shuffle(len(records), func(left, right int) { records[left], records[right] = records[right], records[left] })
	queries := make([]queryValue, 0, len(data.Queries))
	queryVectors := make(map[string][]float32)
	for _, query := range data.Queries {
		if allMapped(query.ExpectedIDs, ids) && allMapped(query.ForbiddenIDs, ids) && allMapped(query.RequiredRelationPath, ids) {
			query.ExpectedIDs = remap(query.ExpectedIDs, ids)
			query.ForbiddenIDs = remap(query.ForbiddenIDs, ids)
			query.RequiredRelationPath = remap(query.RequiredRelationPath, ids)
			queries = append(queries, query)
			queryVectors[query.Query] = append([]float32(nil), data.FrozenQueryEmbeddings[query.QueryID]...)
		}
	}
	changes := make([]assetValue, 0)
	for _, change := range data.ChangeSequences {
		if mapped := ids[change.FixtureID]; mapped != "" {
			change.FixtureID = mapped
			changes = append(changes, change)
		}
	}
	return fixture{Assets: assets, Records: records, Queries: queries, QueryVectors: queryVectors, Truth: relations, Changes: changes, OriginalMap: ids}, nil
}

func measureFixture(ctx context.Context, value fixture, stages map[string]bool, out collector, scratchRoot string) error {
	organizationStarted := time.Now()
	records := cloneRecords(value.Records)
	derivedBytes := 0
	for _, record := range records {
		encoded, err := derived.EncodeRecord(record)
		if err != nil {
			return err
		}
		derivedBytes += len(encoded)
	}
	organizationElapsed := time.Since(organizationStarted)

	runtime.GC()
	var allocationBefore runtime.MemStats
	runtime.ReadMemStats(&allocationBefore)
	indexStarted := time.Now()
	lexical := retrieval.NewLexical(value.Assets)
	semanticIndex := derived.NewIndex(cloneRecords(records))
	indexElapsed := time.Since(indexStarted)
	var allocationAfter runtime.MemStats
	runtime.ReadMemStats(&allocationAfter)
	indexAllocatedBytes := allocationAfter.TotalAlloc - allocationBefore.TotalAlloc

	if stages["identity"] {
		correct := 0
		for _, asset := range value.Assets {
			results := lexical.Search(asset.ID, nil, 1)
			if len(results) == 1 && results[0].Information.ID == asset.ID {
				correct++
			}
		}
		out.add("identity_stability", "quality", "identity", ratio(correct, len(value.Assets)), "higher")
	}
	if stages["relations"] {
		actual := make(map[string]struct{})
		for _, record := range records {
			stored, ok := semanticIndex.Get(record.AssetID)
			if !ok {
				continue
			}
			for _, relation := range stored.Analysis.Relations {
				actual[relationKey(record.AssetID, relation.Type, relation.TargetID)] = struct{}{}
			}
		}
		expected := make(map[string]struct{}, len(value.Truth))
		for _, relation := range value.Truth {
			expected[relationKey(relation.SourceID, relation.Type, relation.TargetID)] = struct{}{}
		}
		matches := intersection(actual, expected)
		out.add("relation_precision", "quality", "relations", ratio(matches, len(actual)), "higher")
		out.add("relation_recall", "quality", "relations", ratio(matches, len(expected)), "higher")
	}
	if stages["merge_split"] {
		seen := make(map[string]struct{})
		for _, asset := range value.Assets {
			seen[asset.ID] = struct{}{}
		}
		out.add("merge_split_integrity", "quality", "merge_split", boolScore(len(seen) == len(value.Assets) && len(records) == len(value.Assets)), "higher")
	}
	if stages["incremental_consistency"] {
		passed := true
		for _, change := range value.Changes {
			var current domain.Information
			for _, asset := range value.Assets {
				if asset.ID == change.FixtureID {
					current = asset
					break
				}
			}
			if current.ID == "" {
				passed = false
				continue
			}
			current.Revision++
			current.UpdatedAt = current.UpdatedAt.Add(time.Minute)
			current.Content = change.Content
			lexical.Upsert(current)
			vector := append([]float32(nil), value.Records[0].Embedding...)
			semanticIndex.Upsert(derived.Record{AssetID: current.ID, AssetRevision: current.Revision, GeneratedAt: current.UpdatedAt, Provider: "frontier-change", Status: "ready", EmbeddingSpace: "frozen-frontier-v1", Embedding: vector})
			results := lexical.Search(change.Content, nil, 5)
			stored, ok := semanticIndex.Get(current.ID)
			passed = passed && containsResult(results, current.ID) && ok && stored.AssetRevision == current.Revision
		}
		out.add("incremental_consistency", "quality", "incremental_consistency", boolScore(passed), "higher")
	}
	if stages["organization"] {
		out.add("organization_p95_ms", "latency", "organization", milliseconds(organizationElapsed), "lower")
		out.add("derived_bytes", "resources", "organization", float64(derivedBytes), "lower")
	}
	if stages["indexing"] {
		out.add("index_build_ms", "latency", "indexing", milliseconds(indexElapsed), "lower")
		out.add("index_bytes", "resources", "indexing", float64(indexAllocatedBytes), "lower")
	}
	if stages["lexical"] {
		out.add("lexical_recall", "quality", "lexical", averageRecall(value.Queries, func(query queryValue) []string {
			return lexicalIDs(lexical.Search(query.Query, convertContexts(query.Contexts), 10))
		}), "higher")
	}
	if stages["vector"] {
		out.add("vector_recall", "quality", "vector", averageRecall(value.Queries, func(query queryValue) []string {
			vector := value.QueryVectors[query.Query]
			return semanticIDs(semanticIndex.Search(vector, convertContexts(query.Contexts), 10))
		}), "higher")
	}
	if stages["graph"] {
		correct, total := 0, 0
		for _, query := range value.Queries {
			if len(query.RequiredRelationPath) < 2 {
				continue
			}
			total++
			edges := semanticIndex.Navigate(query.RequiredRelationPath[:1], nil, len(query.RequiredRelationPath)-1, 50)
			if pathPresent(edges, query.RequiredRelationPath) {
				correct++
			}
		}
		out.add("graph_recall", "quality", "graph", ratio(correct, total), "higher")
	}
	if stages["context"] || stages["fusion"] {
		root, err := os.MkdirTemp(scratchRoot, "fixture-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(root)
		assets, err := assetlog.Open(filepath.Join(root, "assets"))
		if err != nil {
			return err
		}
		states, err := derived.Open(filepath.Join(root, "state"))
		if err != nil {
			_ = assets.Close()
			return err
		}
		defer assets.Close()
		defer states.Close()
		for _, asset := range value.Assets {
			if err := assets.Create(asset); err != nil {
				return err
			}
		}
		for _, record := range records {
			if err := states.Put(record); err != nil {
				return err
			}
		}
		service, err := core.NewCollaborative(assets, states, frozenProvider{queries: value.QueryVectors})
		if err != nil {
			return err
		}
		contextScores := make([]float64, 0)
		fusionScores := make([]float64, 0, len(value.Queries))
		fusionNDCG := make([]float64, 0, len(value.Queries))
		for _, query := range value.Queries {
			started := time.Now()
			results, err := service.Search(ctx, core.SearchInput{Query: query.Query, Contexts: convertContexts(query.Contexts), Limit: 10})
			if err != nil {
				return err
			}
			out.add("query_p95_ms", "latency", "fusion", milliseconds(time.Since(started)), "lower")
			ids := searchIDs(results)
			fusionScores = append(fusionScores, recall(ids, query.ExpectedIDs))
			fusionNDCG = append(fusionNDCG, ndcg(ids, query.ExpectedIDs))
			if len(query.Contexts) > 0 {
				contextScores = append(contextScores, contextScore(ids, query.ExpectedIDs, query.ForbiddenIDs))
			}
		}
		if stages["context"] {
			out.add("context_precision", "quality", "context", mean(contextScores), "higher")
		}
		if stages["fusion"] {
			out.add("fusion_recall", "quality", "fusion", mean(fusionScores), "higher")
			out.add("fusion_ndcg", "quality", "fusion", mean(fusionNDCG), "higher")
		}
	}
	return nil
}

func (c collector) add(name, dimension, stage string, value float64, direction string) {
	item := c[name]
	if item == nil {
		item = &sampleSpec{Dimension: dimension, Stage: stage, Direction: direction}
		c[name] = item
	}
	item.Values = append(item.Values, value)
}

func metricsFromRuns(runs []collector) ([]metric, error) {
	if len(runs) < 2 {
		return nil, errors.New("前沿观察至少需要两次独立测量")
	}
	result := make([]metric, 0, len(runs[0]))
	for name, first := range runs[0] {
		values := make([]float64, 0, len(runs))
		for _, run := range runs {
			item := run[name]
			if item == nil || item.Dimension != first.Dimension || item.Stage != first.Stage || item.Direction != first.Direction {
				return nil, fmt.Errorf("重复测量的指标 %s 不一致", name)
			}
			values = append(values, aggregate(item))
		}
		value := percentile(values, 0.5)
		errorValue := 0.0
		for _, measured := range values {
			errorValue = math.Max(errorValue, math.Abs(measured-value))
		}
		minimum := 0.005
		if first.Dimension == "latency" {
			minimum = 0.1
		}
		if first.Dimension == "resources" {
			minimum = 1
		}
		materiality := math.Max(minimum, errorValue*2)
		result = append(result, metric{Name: name, Dimension: first.Dimension, Stage: first.Stage, Value: value, Direction: first.Direction, RepeatabilityError: errorValue, Materiality: materiality, Protected: true})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func aggregate(item *sampleSpec) float64 {
	if item.Dimension == "latency" {
		return percentile(item.Values, 0.95)
	}
	if item.Dimension == "resources" {
		return maximum(item.Values)
	}
	return mean(item.Values)
}

func requestedStages(mode, csv string) (map[string]bool, error) {
	known := []string{"identity", "relations", "merge_split", "incremental_consistency", "organization", "indexing", "lexical", "vector", "graph", "context", "fusion"}
	result := make(map[string]bool)
	if mode == "full" {
		for _, stage := range known {
			result[stage] = true
		}
		if strings.TrimSpace(csv) != "" {
			return nil, errors.New("完整模式不能裁剪阶段")
		}
		return result, nil
	}
	for _, stage := range strings.Split(csv, ",") {
		stage = strings.TrimSpace(stage)
		if stage != "" {
			result[stage] = true
		}
	}
	for stage := range result {
		found := false
		for _, value := range known {
			if value == stage {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("未知定向阶段 %s", stage)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("定向模式必须声明阶段")
	}
	return result, nil
}

func validateDataset(value dataset) error {
	if value.Schema != "ownward.core-frontier-materials/v1" || len(value.Assets) == 0 || len(value.Scales) == 0 || value.DeterministicVariants.Count < 2 {
		return errors.New("固定材料无效")
	}
	for _, asset := range value.Assets {
		frozen := value.FrozenEmbeddings[asset.FixtureID]
		if len(frozen) != 16 || !finite(frozen) {
			return fmt.Errorf("%s 冻结向量维度无效", asset.FixtureID)
		}
	}
	for _, query := range value.Queries {
		if vector := value.FrozenQueryEmbeddings[query.QueryID]; len(vector) != 16 || !finite(vector) {
			return fmt.Errorf("%s 冻结查询向量无效", query.QueryID)
		}
	}
	return nil
}

type frozenProvider struct{ queries map[string][]float32 }

func (f frozenProvider) Name() string { return "frozen-frontier-v1" }
func (f frozenProvider) Space() embedding.Space {
	return embedding.Space{ID: "frozen-frontier-v1", Dimensions: 16}
}
func (f frozenProvider) EmbedDocuments(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("固定前沿观察器不生成文档向量")
}
func (f frozenProvider) EmbedQuery(_ context.Context, value string) ([]float32, error) {
	vector := f.queries[value]
	if len(vector) == 0 {
		return nil, errors.New("查询不在固定向量输入中")
	}
	return append([]float32(nil), vector...), nil
}
func (f frozenProvider) Close() error { return nil }

func finite(values []float32) bool {
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

func verifyCandidate(repository, expected string) error {
	command := exec.Command("git", "rev-parse", "HEAD")
	command.Dir = repository
	encoded, err := command.Output()
	if err != nil {
		return fmt.Errorf("读取候选提交: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(encoded)), expected) {
		return errors.New("观察器仓库与候选提交不一致")
	}
	return nil
}

func verifySelf(expected string, selfCheck bool) error {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return errors.New("观察器缺少 Go 构建身份")
	}
	revision, modified := "", ""
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
		if setting.Key == "vcs.modified" {
			modified = setting.Value
		}
	}
	if revision != expected {
		return errors.New("观察器不是由候选提交构建")
	}
	if modified == "true" && !selfCheck {
		return errors.New("正式观察器由含未提交变更的源码构建")
	}
	return nil
}

func validSHA(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 64 && err == nil
}
func digest(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func fileSHA(path string) (string, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return digest(value), nil
}
func writeJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
func convertContexts(values []contextValue) []domain.Context {
	result := make([]domain.Context, len(values))
	for i, v := range values {
		result[i] = domain.Context{Key: v.Key, Value: v.Value}
	}
	return result
}
func inferredContexts(values []contextValue) []semantics.InferredContext {
	result := make([]semantics.InferredContext, len(values))
	for i, v := range values {
		result[i] = semantics.InferredContext{Key: v.Key, Value: v.Value, Confidence: 1, Evidence: "frozen truth"}
	}
	return result
}
func reverseContexts(values []domain.Context) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
func remap(values []string, ids map[string]string) []string {
	result := make([]string, len(values))
	for i, v := range values {
		result[i] = ids[v]
	}
	return result
}
func allMapped(values []string, ids map[string]string) bool {
	for _, v := range values {
		if ids[v] == "" {
			return false
		}
	}
	return true
}
func cloneRecords(values []derived.Record) []derived.Record {
	result := make([]derived.Record, len(values))
	for i, v := range values {
		result[i] = v
		result[i].Embedding = append([]float32(nil), v.Embedding...)
		result[i].Analysis.Contexts = append([]semantics.InferredContext(nil), v.Analysis.Contexts...)
		result[i].Analysis.Relations = append([]semantics.Relation(nil), v.Analysis.Relations...)
	}
	return result
}
func relationKey(source, relation, target string) string {
	return source + "\x00" + relation + "\x00" + target
}
func intersection(left, right map[string]struct{}) int {
	count := 0
	for key := range left {
		if _, ok := right[key]; ok {
			count++
		}
	}
	return count
}
func ratio(value, total int) float64 {
	if total == 0 {
		return 1
	}
	return float64(value) / float64(total)
}
func boolScore(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
func milliseconds(value time.Duration) float64 { return float64(value) / float64(time.Millisecond) }
func containsResult(values []retrieval.Result, id string) bool {
	for _, v := range values {
		if v.Information.ID == id {
			return true
		}
	}
	return false
}
func lexicalIDs(values []retrieval.Result) []string {
	result := make([]string, len(values))
	for i, v := range values {
		result[i] = v.Information.ID
	}
	return result
}
func semanticIDs(values []derived.SemanticHit) []string {
	result := make([]string, len(values))
	for i, v := range values {
		result[i] = v.AssetID
	}
	return result
}
func searchIDs(values []core.SearchResult) []string {
	result := make([]string, len(values))
	for i, v := range values {
		result[i] = v.ID
	}
	return result
}
func averageRecall(queries []queryValue, search func(queryValue) []string) float64 {
	values := make([]float64, 0, len(queries))
	for _, q := range queries {
		values = append(values, recall(search(q), q.ExpectedIDs))
	}
	return mean(values)
}
func recall(returned, expected []string) float64 {
	wanted := make(map[string]struct{}, len(expected))
	for _, id := range expected {
		wanted[id] = struct{}{}
	}
	count := 0
	for _, id := range returned {
		if _, ok := wanted[id]; ok {
			count++
			delete(wanted, id)
		}
	}
	return ratio(count, len(expected))
}
func ndcg(returned, expected []string) float64 {
	wanted := make(map[string]struct{}, len(expected))
	for _, id := range expected {
		wanted[id] = struct{}{}
	}
	actual := 0.0
	for i, id := range returned {
		if _, ok := wanted[id]; ok {
			actual += 1 / math.Log2(float64(i)+2)
		}
	}
	ideal := 0.0
	for i := range expected {
		ideal += 1 / math.Log2(float64(i)+2)
	}
	if ideal == 0 {
		return 1
	}
	return actual / ideal
}
func contextScore(returned, expected, forbidden []string) float64 {
	bad := make(map[string]struct{}, len(forbidden))
	for _, id := range forbidden {
		bad[id] = struct{}{}
	}
	for _, id := range returned {
		if _, ok := bad[id]; ok {
			return 0
		}
	}
	return recall(returned, expected)
}
func pathPresent(edges []derived.Edge, path []string) bool {
	for i := 0; i+1 < len(path); i++ {
		found := false
		for _, edge := range edges {
			if (edge.SourceID == path[i] && edge.TargetID == path[i+1]) || (edge.TargetID == path[i] && edge.SourceID == path[i+1]) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func mean(values []float64) float64 {
	if len(values) == 0 {
		return 1
	}
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total / float64(len(values))
}
func maximum(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	result := values[0]
	for _, v := range values[1:] {
		if v > result {
			result = v
		}
	}
	return result
}
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copied := append([]float64(nil), values...)
	sort.Float64s(copied)
	index := int(math.Ceil(p*float64(len(copied)))) - 1
	if index < 0 {
		index = 0
	}
	return copied[index]
}
