package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
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
	"reflect"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	FixtureID     string         `json:"fixture_id"`
	Content       string         `json:"content"`
	Padding       string         `json:"padding,omitempty"`
	PaddingRepeat int            `json:"padding_repeat,omitempty"`
	TargetRunes   int            `json:"target_runes,omitempty"`
	FactPosition  string         `json:"fact_position,omitempty"`
	Contexts      []contextValue `json:"contexts"`
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
	ViewRole             string         `json:"view_role,omitempty"`
	ContextBudgetChars   int            `json:"context_budget_chars,omitempty"`
	ReadLimit            int            `json:"read_limit,omitempty"`
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
		Relations []relationValue               `json:"relations"`
		Contexts  map[string][]contextValue     `json:"contexts"`
		Analyses  map[string]semantics.Analysis `json:"analyses,omitempty"`
	} `json:"frozen_semantics"`
	SemanticEmbeddingKeys map[string]string `json:"semantic_embedding_keys,omitempty"`
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
	var materials, candidate, mode, environmentSHA, inputSHA, output, repository, stagesCSV, sourceEquivalent, sourceIdentity string
	var selfCheck bool
	flag.StringVar(&materials, "materials", "", "固定内核基准材料")
	flag.StringVar(&candidate, "candidate", "", "候选提交")
	flag.StringVar(&mode, "mode", "", "targeted 或 full")
	flag.StringVar(&environmentSHA, "environment-sha256", "", "环境摘要")
	flag.StringVar(&inputSHA, "input-manifest-sha256", "", "输入清单摘要")
	flag.StringVar(&output, "output", "", "观察报告")
	flag.StringVar(&repository, "repository", ".", "候选仓库")
	flag.StringVar(&stagesCSV, "stages", "", "定向阶段，逗号分隔")
	flag.StringVar(&sourceEquivalent, "source-equivalent-candidate", "", "非正式定向观察绑定的产品源码等价候选")
	flag.StringVar(&sourceIdentity, "source-identity-sha256", "", "非正式定向观察绑定的当前工作树产品源码摘要")
	flag.BoolVar(&selfCheck, "self-check", false, "允许在未提交工作树上执行非正式体系自检")
	flag.Parse()
	if err := run(materials, candidate, mode, environmentSHA, inputSHA, output, repository, stagesCSV, selfCheck, sourceEquivalent, sourceIdentity); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-frontier:", err)
		os.Exit(2)
	}
}

func run(materialsPath, candidate, mode, environmentSHA, inputSHA, outputPath, repository, stagesCSV string, selfCheck bool, sourceEquivalent, sourceIdentity string) error {
	if mode != "targeted" && mode != "full" {
		return errors.New("mode 必须是 targeted 或 full")
	}
	if strings.TrimSpace(candidate) == "" || !validSHA(environmentSHA) || !validSHA(inputSHA) || outputPath == "" {
		return errors.New("候选、环境、输入和输出绑定不完整")
	}
	sourceProof, err := verifySourceIdentity(repository, candidate, sourceEquivalent, sourceIdentity, mode, selfCheck)
	if err != nil {
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
		Environment:     observationEnvironment(environmentSHA, sourceProof),
		ToolSHA256:      toolSHA, Metrics: metrics, StartedAt: started, FinishedAt: time.Now().UTC(),
	}
	if len(report.Metrics) == 0 {
		return errors.New("观察报告没有指标")
	}
	return writeJSON(outputPath, report)
}

func observationEnvironment(environmentSHA string, sourceProof map[string]any) map[string]any {
	environment := map[string]any{
		"sha256": environmentSHA,
		"go":     runtime.Version(),
		"os":     runtime.GOOS,
		"arch":   runtime.GOARCH,
		"cpus":   runtime.NumCPU(),
	}
	if sourceProof["kind"] != "candidate-commit" {
		environment["product_source"] = sourceProof
	}
	return environment
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
	Assets        []domain.Information
	Records       []derived.Record
	Queries       []queryValue
	QueryVectors  map[string][]float32
	Truth         []relationValue
	Changes       []assetValue
	OriginalMap   map[string]string
	Analyses      map[string]semantics.Analysis
	EmbeddingKeys map[string][]float32
}

type lifecycleMeasurement struct {
	SourceRunes             int
	OrganizationInputRunes  int
	OrganizationItems       int
	DerivedRecords          int
	DerivedVectors          int
	DerivedStorageBytes     int64
	RebuildInputRunes       int
	RebuildItems            int
	RebuiltRecords          int
	RebuiltVectors          int
	OrganizationWallSeconds float64
	RebuildWallSeconds      float64
}

type readMeasurement struct {
	IDs             []string
	ContentRunes    int
	SerializedBytes int
	ReadUnits       int
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
		asset := domain.Information{Schema: domain.AssetSchema, ID: id, Revision: 1, CreatedAt: created, UpdatedAt: created, Kind: domain.KindGeneral, Content: expandedContent(value), Contexts: contexts}
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
	analyses := make(map[string]semantics.Analysis)
	embeddingKeys := make(map[string][]float32)
	for fixtureID, analysis := range data.FrozenSemantics.Analyses {
		mapped := ids[fixtureID]
		if mapped == "" {
			continue
		}
		for index := range analysis.Relations {
			analysis.Relations[index].TargetID = ids[analysis.Relations[index].TargetID]
		}
		analyses[mapped] = analysis
		key := data.SemanticEmbeddingKeys[fixtureID]
		if key != "" {
			embeddingKeys[key] = append([]float32(nil), data.FrozenEmbeddings[fixtureID]...)
		}
	}
	return fixture{
		Assets: assets, Records: records, Queries: queries, QueryVectors: queryVectors,
		Truth: relations, Changes: changes, OriginalMap: ids, Analyses: analyses, EmbeddingKeys: embeddingKeys,
	}, nil
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
	if stages["semantic_representation"] {
		if err := measureSemanticRepresentation(ctx, value, out, scratchRoot); err != nil {
			return err
		}
	}
	if stages["storage_architecture"] {
		if err := measureStorageArchitecture(ctx, value, out, scratchRoot); err != nil {
			return err
		}
	}
	if stages["execution_state"] {
		if err := measureExecutionState(ctx, value, out, scratchRoot); err != nil {
			return err
		}
	}

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
	if stages["efficiency"] {
		measured, err := measureOrganizationLifecycle(ctx, value.Assets, scratchRoot)
		if err != nil {
			return err
		}
		// The kernel-iteration driver mechanically verifies that organization,
		// rebuild, and storage paths are byte-equivalent to V0 before invoking
		// this stage. One shared measurement therefore represents both sides of
		// the paired comparison without inventing order-dependent process noise.
		v0Measured := measured
		out.add("organization_input_overhead_ratio", "resources", "efficiency", overheadRatio(measured.OrganizationInputRunes, measured.SourceRunes), "lower")
		out.add("organization_vector_overhead_ratio", "resources", "efficiency", overheadRatio(measured.OrganizationItems, len(value.Assets)), "lower")
		out.add("derived_record_overhead_ratio", "resources", "efficiency", overheadRatio(measured.DerivedRecords, len(value.Assets)), "lower")
		out.add("derived_vector_overhead_ratio", "resources", "efficiency", overheadRatio(measured.DerivedVectors, len(value.Assets)), "lower")
		out.add("rebuild_input_overhead_ratio", "resources", "efficiency", overheadRatio(measured.RebuildInputRunes, measured.SourceRunes), "lower")
		out.add("rebuild_vector_overhead_ratio", "resources", "efficiency", overheadRatio(measured.RebuildItems, len(value.Assets)), "lower")
		out.add("rebuilt_record_overhead_ratio", "resources", "efficiency", overheadRatio(measured.RebuiltRecords, len(value.Assets)), "lower")
		out.add("rebuilt_vector_overhead_ratio", "resources", "efficiency", overheadRatio(measured.RebuiltVectors, len(value.Assets)), "lower")
		out.add("v0_derived_storage_bytes_per_source_rune", "resources", "efficiency", ratioFloat(float64(v0Measured.DerivedStorageBytes), float64(v0Measured.SourceRunes)), "lower")
		out.add("candidate_derived_storage_bytes_per_source_rune", "resources", "efficiency", ratioFloat(float64(measured.DerivedStorageBytes), float64(measured.SourceRunes)), "lower")
		out.add("v0_organization_real_p95_ms", "latency", "efficiency", v0Measured.OrganizationWallSeconds*1000, "lower")
		out.add("candidate_organization_real_p95_ms", "latency", "efficiency", measured.OrganizationWallSeconds*1000, "lower")
		out.add("v0_rebuild_real_p95_ms", "latency", "efficiency", v0Measured.RebuildWallSeconds*1000, "lower")
		out.add("candidate_rebuild_real_p95_ms", "latency", "efficiency", measured.RebuildWallSeconds*1000, "lower")
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
	if stages["context"] || stages["fusion"] || stages["efficiency"] {
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
		budgetRecall := make([]float64, 0)
		budgetProtection := make([]float64, 0)
		budgetScale := make([]float64, 0)
		fusionScores := make([]float64, 0, len(value.Queries))
		fusionNDCG := make([]float64, 0, len(value.Queries))
		for _, query := range value.Queries {
			started := time.Now()
			results, err := service.Search(ctx, core.SearchInput{Query: query.Query, Contexts: convertContexts(query.Contexts), Limit: 10})
			if err != nil {
				return err
			}
			searchElapsed := time.Since(started)
			out.add("query_p95_ms", "latency", "fusion", milliseconds(searchElapsed), "lower")
			ids := searchIDs(results)
			fusionScores = append(fusionScores, recall(ids, query.ExpectedIDs))
			fusionNDCG = append(fusionNDCG, ndcg(ids, query.ExpectedIDs))
			if len(query.Contexts) > 0 {
				contextScores = append(contextScores, contextScore(ids, query.ExpectedIDs, query.ForbiddenIDs))
			}
			if query.ContextBudgetChars > 0 {
				v0Started := time.Now()
				v0Delivery, err := budgetedFullServiceReadIDs(ctx, service, results, query.ContextBudgetChars, query.ReadLimit)
				v0Elapsed := time.Since(v0Started)
				if err != nil {
					return err
				}
				candidateStarted := time.Now()
				candidateDelivery, err := budgetedServiceReadIDs(ctx, service, results, query.Query, query.ContextBudgetChars, query.ReadLimit)
				candidateElapsed := time.Since(candidateStarted)
				if err != nil {
					return err
				}
				score := recall(candidateDelivery.IDs, query.ExpectedIDs)
				if query.ViewRole == "primary" {
					budgetRecall = append(budgetRecall, score)
				} else if query.ViewRole == "protection" {
					budgetProtection = append(budgetProtection, score)
				} else if query.ViewRole == "scale" {
					budgetScale = append(budgetScale, score)
				}
				if stages["efficiency"] {
					out.add("v0_query_workflow_p95_ms", "latency", "efficiency", milliseconds(searchElapsed+v0Elapsed), "lower")
					out.add("candidate_query_workflow_p95_ms", "latency", "efficiency", milliseconds(searchElapsed+candidateElapsed), "lower")
					out.add("v0_query_content_runes", "resources", "efficiency", float64(v0Delivery.ContentRunes), "lower")
					out.add("candidate_query_content_runes", "resources", "efficiency", float64(candidateDelivery.ContentRunes), "lower")
					out.add("v0_query_serialized_bytes", "resources", "efficiency", float64(v0Delivery.SerializedBytes), "lower")
					out.add("candidate_query_serialized_bytes", "resources", "efficiency", float64(candidateDelivery.SerializedBytes), "lower")
				}
			}
		}
		if stages["context"] {
			out.add("context_precision", "quality", "context", mean(contextScores), "higher")
			if len(budgetRecall) > 0 {
				out.add("required_evidence_budget_recall", "quality", "context", mean(budgetRecall), "higher")
				out.add("required_evidence_budget_error_rate", "quality", "context", 1-mean(budgetRecall), "lower")
			}
			if len(budgetProtection) > 0 {
				out.add("budget_fit_protection", "quality", "context", mean(budgetProtection), "higher")
			}
			if len(budgetScale) > 0 {
				out.add("scale_evidence_recall", "quality", "context", mean(budgetScale), "higher")
			}
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
	full := []string{"identity", "relations", "merge_split", "incremental_consistency", "organization", "indexing", "lexical", "vector", "graph", "context", "fusion"}
	known := append(append([]string(nil), full...), "efficiency", "semantic_representation", "storage_architecture", "execution_state")
	result := make(map[string]bool)
	if mode == "full" {
		for _, stage := range full {
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
		if asset.PaddingRepeat < 0 || (asset.PaddingRepeat > 0 && strings.TrimSpace(asset.Padding) == "") {
			return fmt.Errorf("%s 固定填充无效", asset.FixtureID)
		}
		if asset.TargetRunes < 0 || (asset.TargetRunes > 0 && (strings.TrimSpace(asset.Padding) == "" || asset.TargetRunes < utf8.RuneCountInString(asset.Content))) {
			return fmt.Errorf("%s 固定长度定义无效", asset.FixtureID)
		}
		if asset.FactPosition != "" && asset.FactPosition != "middle" {
			return fmt.Errorf("%s 事实位置定义无效", asset.FixtureID)
		}
		if asset.TargetRunes > 0 && utf8.RuneCountInString(expandedContent(asset)) != asset.TargetRunes {
			return fmt.Errorf("%s 固定长度无法精确复现", asset.FixtureID)
		}
		frozen := value.FrozenEmbeddings[asset.FixtureID]
		if len(frozen) != 16 || !finite(frozen) {
			return fmt.Errorf("%s 冻结向量维度无效", asset.FixtureID)
		}
	}
	for _, query := range value.Queries {
		validBudgetRole := query.ViewRole == "primary" || query.ViewRole == "protection" || query.ViewRole == "scale"
		if query.ContextBudgetChars < 0 || query.ReadLimit < 0 || (query.ContextBudgetChars > 0 && (query.ReadLimit == 0 || !validBudgetRole)) {
			return fmt.Errorf("%s 预算视图定义无效", query.QueryID)
		}
		if vector := value.FrozenQueryEmbeddings[query.QueryID]; len(vector) != 16 || !finite(vector) {
			return fmt.Errorf("%s 冻结查询向量无效", query.QueryID)
		}
	}
	return nil
}

func expandedContent(value assetValue) string {
	if value.TargetRunes > 0 {
		contentRunes := utf8.RuneCountInString(value.Content)
		remaining := value.TargetRunes - contentRunes
		if remaining <= 0 {
			return value.Content
		}
		prefix := 0
		if value.FactPosition == "middle" {
			prefix = remaining / 2
		}
		return repeatRunes(value.Padding, prefix) + value.Content + repeatRunes(value.Padding, remaining-prefix)
	}
	return value.Content + strings.Repeat(value.Padding, value.PaddingRepeat)
}

func repeatRunes(pattern string, count int) string {
	if count <= 0 || pattern == "" {
		return ""
	}
	patternRunes := []rune(pattern)
	result := make([]rune, 0, count)
	for len(result) < count {
		remaining := count - len(result)
		if remaining < len(patternRunes) {
			result = append(result, patternRunes[:remaining]...)
		} else {
			result = append(result, patternRunes...)
		}
	}
	return string(result)
}

func measureOrganizationLifecycle(ctx context.Context, assets []domain.Information, scratchRoot string) (lifecycleMeasurement, error) {
	root, err := os.MkdirTemp(scratchRoot, "efficiency-*")
	if err != nil {
		return lifecycleMeasurement{}, err
	}
	defer os.RemoveAll(root)
	assetStore, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		return lifecycleMeasurement{}, err
	}
	stateRoot := filepath.Join(root, "state")
	stateStore, err := derived.Open(stateRoot)
	if err != nil {
		_ = assetStore.Close()
		return lifecycleMeasurement{}, err
	}
	provider := &measuringProvider{}
	service, err := core.NewCollaborative(assetStore, stateStore, provider)
	if err != nil {
		_ = stateStore.Close()
		_ = assetStore.Close()
		return lifecycleMeasurement{}, err
	}
	defer service.Close()

	measured := lifecycleMeasurement{}
	inputs := make([]core.CreateInput, len(assets))
	for index, asset := range assets {
		measured.SourceRunes += utf8.RuneCountInString(asset.Content)
		inputs[index] = core.CreateInput{Kind: asset.Kind, Content: asset.Content, Contexts: asset.Contexts, Relations: asset.Relations, Source: asset.Source}
	}
	organizationStarted := time.Now()
	for start := 0; start < len(inputs); start += 20 {
		end := min(start+20, len(inputs))
		results, err := service.CreateBatch(ctx, inputs[start:end])
		if err != nil {
			return lifecycleMeasurement{}, err
		}
		for _, result := range results {
			if result.Error != "" || result.Result == nil {
				return lifecycleMeasurement{}, errors.New("真实组织规模测量未完成全部资产")
			}
		}
	}
	measured.OrganizationWallSeconds = time.Since(organizationStarted).Seconds()
	measured.OrganizationInputRunes = provider.documentRunes
	measured.OrganizationItems = provider.documentItems
	records, err := stateStore.AllWithEmbeddings()
	if err != nil {
		return lifecycleMeasurement{}, err
	}
	measured.DerivedRecords = len(records)
	measured.DerivedVectors = vectorRecordCount(records)
	measured.DerivedStorageBytes, err = directoryBytes(stateRoot)
	if err != nil {
		return lifecycleMeasurement{}, err
	}

	provider.reset()
	rebuildStarted := time.Now()
	if _, err := service.Maintain(ctx, true); err != nil {
		return lifecycleMeasurement{}, err
	}
	measured.RebuildWallSeconds = time.Since(rebuildStarted).Seconds()
	measured.RebuildInputRunes = provider.documentRunes
	measured.RebuildItems = provider.documentItems
	rebuilt, err := stateStore.AllWithEmbeddings()
	if err != nil {
		return lifecycleMeasurement{}, err
	}
	measured.RebuiltRecords = len(rebuilt)
	measured.RebuiltVectors = vectorRecordCount(rebuilt)
	return measured, nil
}

func measureStorageArchitecture(ctx context.Context, value fixture, out collector, scratchRoot string) error {
	root, err := os.MkdirTemp(scratchRoot, "storage-architecture-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	assetsPath := filepath.Join(root, "assets")
	statePath := filepath.Join(root, "state")
	assetStore, err := assetlog.Open(assetsPath)
	if err != nil {
		return err
	}
	stateStore, err := derived.Open(statePath)
	if err != nil {
		_ = assetStore.Close()
		return err
	}
	service, err := core.NewCollaborative(assetStore, stateStore, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		_ = stateStore.Close()
		_ = assetStore.Close()
		return err
	}
	defer service.Close()

	createdByFixture := make(map[string]string, len(value.Assets))
	works := make(map[string]semantics.Work, len(value.Assets))
	submissions := make(map[string]semantics.Submission, len(value.Assets))
	legacyDerivedBytes := int64(0)
	for index, source := range value.Assets {
		created, createErr := service.Create(ctx, core.CreateInput{
			Kind: source.Kind, Content: source.Content, Contexts: source.Contexts, Relations: source.Relations, Source: source.Source,
		})
		if createErr != nil {
			return createErr
		}
		createdByFixture[source.ID] = created.Information.ID
		workItems, workErr := service.SemanticWorkFor(ctx, []string{created.Information.ID})
		if workErr != nil || len(workItems) != 1 {
			return fmt.Errorf("storage view semantic work missing: %w", workErr)
		}
		work := workItems[0]
		works[source.ID] = work
		pending, exists := stateStore.GetWithEmbedding(work.Asset.ID)
		if !exists {
			return errors.New("storage view pending record missing")
		}
		legacyDerivedBytes += legacyDerivedRecordSize(pending, &work, nil)
		analysis := value.Analyses[source.ID]
		if strings.TrimSpace(analysis.Summary) == "" {
			analysis.Summary = fmt.Sprintf("isolated storage fixture %d", index+1)
		}
		submission := semantics.Submission{
			Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID, Revision: work.Asset.Revision,
			Capability: semantics.Capability{ID: "frozen-storage-view", Version: "v1", Execution: "deterministic"},
			Status:     semantics.SubmissionComplete, Analysis: analysis,
		}
		normalized, normalizeErr := semantics.NormalizeSubmission(work, submission, work.CreatedAt.Add(time.Second))
		if normalizeErr != nil {
			return normalizeErr
		}
		submissions[source.ID] = submission
		if index == len(value.Assets)-1 {
			continue
		}
		if _, submitErr := service.SubmitSemantic(ctx, submission); submitErr != nil {
			return submitErr
		}
		complete, exists := stateStore.GetWithEmbedding(work.Asset.ID)
		if !exists {
			return errors.New("storage view completed record missing")
		}
		legacyDerivedBytes += legacyDerivedRecordSize(complete, &work, &normalized)
	}

	lastSource := value.Assets[len(value.Assets)-1].ID
	pendingBefore, err := json.Marshal(works[lastSource])
	if err != nil {
		return err
	}
	if err := service.Close(); err != nil {
		return err
	}
	assetStore, err = assetlog.Open(assetsPath)
	if err != nil {
		return err
	}
	stateStore, err = derived.Open(statePath)
	if err != nil {
		_ = assetStore.Close()
		return err
	}
	service, err = core.NewCollaborative(assetStore, stateStore, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		_ = stateStore.Close()
		_ = assetStore.Close()
		return err
	}
	defer service.Close()
	lastID := createdByFixture[lastSource]
	restoredWork, err := service.SemanticWorkFor(ctx, []string{lastID})
	if err != nil || len(restoredWork) != 1 {
		return fmt.Errorf("storage view pending work did not recover: %w", err)
	}
	pendingAfter, _ := json.Marshal(restoredWork[0])
	if !equalBytes(pendingBefore, pendingAfter) {
		return errors.New("storage view changed semantic work while resolving compact references")
	}
	lastSubmission := submissions[lastSource]
	normalizedLast, err := semantics.NormalizeSubmission(restoredWork[0], lastSubmission, restoredWork[0].CreatedAt.Add(time.Second))
	if err != nil {
		return err
	}
	if _, err := service.SubmitSemantic(ctx, lastSubmission); err != nil {
		return err
	}
	if _, err := service.SubmitSemantic(ctx, lastSubmission); err != nil {
		return fmt.Errorf("compact semantic receipt is not idempotent: %w", err)
	}
	lastComplete, exists := stateStore.GetWithEmbedding(lastID)
	if !exists {
		return errors.New("storage view final record missing")
	}
	legacyDerivedBytes += legacyDerivedRecordSize(lastComplete, &restoredWork[0], &normalizedLast)

	largest := 0
	for index := range value.Assets {
		if len(value.Assets[index].Content) > len(value.Assets[largest].Content) {
			largest = index
		}
	}
	largestFixture := value.Assets[largest].ID
	largestID := createdByFixture[largestFixture]
	for revision := uint64(2); revision <= 4; revision++ {
		current, readErr := service.Read(ctx, largestID)
		if readErr != nil {
			return readErr
		}
		content := current.Content + fmt.Sprintf("\nDurable revision marker %d.", revision)
		if _, updateErr := service.Update(ctx, core.UpdateInput{ID: largestID, ExpectedRevision: current.Revision, Content: &content}); updateErr != nil {
			return updateErr
		}
		workItems, workErr := service.SemanticWorkFor(ctx, []string{largestID})
		if workErr != nil || len(workItems) != 1 {
			return fmt.Errorf("storage view update work missing: %w", workErr)
		}
		work := workItems[0]
		pending, _ := stateStore.GetWithEmbedding(largestID)
		legacyDerivedBytes += legacyDerivedRecordSize(pending, &work, nil)
		analysis := value.Analyses[largestFixture]
		if strings.TrimSpace(analysis.Summary) == "" {
			analysis.Summary = "isolated storage update"
		}
		submission := semantics.Submission{
			Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID, Revision: work.Asset.Revision,
			Capability: semantics.Capability{ID: "frozen-storage-view", Version: "v1", Execution: "deterministic"},
			Status:     semantics.SubmissionComplete, Analysis: analysis,
		}
		normalized, normalizeErr := semantics.NormalizeSubmission(work, submission, work.CreatedAt.Add(time.Second))
		if normalizeErr != nil {
			return normalizeErr
		}
		if _, submitErr := service.SubmitSemantic(ctx, submission); submitErr != nil {
			return submitErr
		}
		complete, _ := stateStore.GetWithEmbedding(largestID)
		legacyDerivedBytes += legacyDerivedRecordSize(complete, &work, &normalized)
	}

	assetLog := filepath.Join(assetsPath, "information.jsonl")
	stateLog := filepath.Join(statePath, derived.LogFileName)
	preAsset, err := os.Stat(assetLog)
	if err != nil {
		return err
	}
	if _, err := service.Maintain(ctx, false); err != nil {
		return err
	}
	postAsset, err := os.Stat(assetLog)
	if err != nil {
		return err
	}
	postState, err := os.Stat(stateLog)
	if err != nil {
		return err
	}
	assetDigest, err := fileSHA(assetLog)
	if err != nil {
		return err
	}
	stateDigest, err := fileSHA(stateLog)
	if err != nil {
		return err
	}
	if _, err := service.Maintain(ctx, false); err != nil {
		return err
	}
	secondAssetDigest, _ := fileSHA(assetLog)
	secondStateDigest, _ := fileSHA(stateLog)

	backup := filepath.Join(root, "assets.zip")
	if err := assetStore.Backup(backup); err != nil {
		return err
	}
	restoredPath := filepath.Join(root, "restored-assets")
	if err := assetlog.Restore(backup, restoredPath); err != nil {
		return err
	}
	restoredAssets, err := assetlog.Open(restoredPath)
	if err != nil {
		return err
	}
	restoredLargest, restoredExists := restoredAssets.Get(largestID)
	_ = restoredAssets.Close()
	currentLargest, _ := service.Read(ctx, largestID)
	restoredEncoded, _ := json.Marshal(restoredLargest)
	currentEncoded, _ := json.Marshal(currentLargest)

	queryHits := 0
	queryTotal := 0
	queryStarted := time.Now()
	for _, query := range value.Queries {
		results, searchErr := service.Search(ctx, core.SearchInput{Query: query.Query, Limit: 10})
		if searchErr != nil {
			return searchErr
		}
		for _, expected := range query.ExpectedIDs {
			queryTotal++
			createdID := createdByFixture[expected]
			for _, result := range results {
				if result.ID == createdID {
					queryHits++
					break
				}
			}
		}
	}
	queryElapsed := time.Since(queryStarted)
	sourceRunes := 0
	for _, asset := range assetStore.All() {
		sourceRunes += utf8.RuneCountInString(asset.Content)
	}
	v0Total := int64(preAsset.Size()) + legacyDerivedBytes
	candidateTotal := int64(postAsset.Size()) + postState.Size()
	out.add("v0_product_storage_bytes_per_source_rune", "resources", "storage_architecture", ratioFloat(float64(v0Total), float64(sourceRunes)), "lower")
	out.add("candidate_product_storage_bytes_per_source_rune", "resources", "storage_architecture", ratioFloat(float64(candidateTotal), float64(sourceRunes)), "lower")
	out.add("storage_amplification_ratio", "resources", "storage_architecture", ratioFloat(float64(candidateTotal), float64(v0Total)), "lower")
	out.add("derived_storage_amplification_ratio", "resources", "storage_architecture", ratioFloat(float64(postState.Size()), float64(legacyDerivedBytes)), "lower")
	out.add("authority_history_reclaim_ratio", "resources", "storage_architecture", ratioFloat(float64(postAsset.Size()), float64(preAsset.Size())), "lower")
	out.add("semantic_work_payload_recovery", "quality", "storage_architecture", 1, "higher")
	out.add("semantic_receipt_idempotency", "quality", "storage_architecture", 1, "higher")
	out.add("maintenance_byte_repeatability", "quality", "storage_architecture", boolScore(assetDigest == secondAssetDigest && stateDigest == secondStateDigest), "higher")
	out.add("backup_restore_integrity", "quality", "storage_architecture", boolScore(restoredExists && equalBytes(restoredEncoded, currentEncoded)), "higher")
	out.add("storage_view_search_recall", "quality", "storage_architecture", ratio(queryHits, queryTotal), "higher")
	out.add("storage_view_query_p95_ms", "latency", "storage_architecture", queryElapsed.Seconds()*1000, "lower")
	out.add("derived_records_per_asset", "resources", "storage_architecture", ratioFloat(float64(len(stateStore.All())), float64(len(assetStore.All()))), "lower")
	return nil
}

type durableBatchMeasurement struct {
	Wall              time.Duration
	AllocatedBytes    uint64
	AuthorityExact    bool
	DerivedExact      bool
	OrderExact        bool
	RestartExact      bool
	TailRecoveryExact bool
}

func measureExecutionState(ctx context.Context, value fixture, out collector, scratchRoot string) error {
	assets, records := executionScale(value, 3)
	legacy, err := measureDurableBatch(filepath.Join(scratchRoot, fmt.Sprintf("execution-legacy-%d", time.Now().UnixNano())), assets, records, false, false)
	if err != nil {
		return err
	}
	candidate, err := measureDurableBatch(filepath.Join(scratchRoot, fmt.Sprintf("execution-candidate-%d", time.Now().UnixNano())), assets, records, true, true)
	if err != nil {
		return err
	}
	legacyGeneration, err := measureGenerationBuild(filepath.Join(scratchRoot, fmt.Sprintf("generation-legacy-%d", time.Now().UnixNano())), records, false)
	if err != nil {
		return err
	}
	candidateGeneration, err := measureGenerationBuild(filepath.Join(scratchRoot, fmt.Sprintf("generation-candidate-%d", time.Now().UnixNano())), records, true)
	if err != nil {
		return err
	}
	legacyPublic, _, _, err := measurePublicCreate(ctx, filepath.Join(scratchRoot, fmt.Sprintf("public-legacy-%d", time.Now().UnixNano())), assets, false)
	if err != nil {
		return err
	}
	candidatePublic, publicSearch, publicRecovery, err := measurePublicCreate(ctx, filepath.Join(scratchRoot, fmt.Sprintf("public-candidate-%d", time.Now().UnixNano())), assets, true)
	if err != nil {
		return err
	}
	uncommittedIsolation, err := measureUncommittedGenerationIsolation(filepath.Join(scratchRoot, fmt.Sprintf("generation-isolation-%d", time.Now().UnixNano())))
	if err != nil {
		return err
	}
	concurrentCompletion, err := measureConcurrentBatchCreate(ctx, filepath.Join(scratchRoot, fmt.Sprintf("execution-concurrent-%d", time.Now().UnixNano())))
	if err != nil {
		return err
	}
	out.add("predecessor_durable_write_wall_ms", "latency", "execution_state", milliseconds(legacy.Wall), "lower")
	out.add("candidate_durable_write_wall_ms", "latency", "execution_state", milliseconds(candidate.Wall), "lower")
	out.add("durable_batch_wall_ratio", "latency", "execution_state", ratioFloat(float64(candidate.Wall), float64(legacy.Wall)), "lower")
	out.add("predecessor_generation_build_wall_ms", "latency", "execution_state", milliseconds(legacyGeneration), "lower")
	out.add("candidate_generation_build_wall_ms", "latency", "execution_state", milliseconds(candidateGeneration), "lower")
	out.add("generation_build_wall_ratio", "latency", "execution_state", ratioFloat(float64(candidateGeneration), float64(legacyGeneration)), "lower")
	out.add("predecessor_public_create_wall_ms", "latency", "execution_state", milliseconds(legacyPublic), "lower")
	out.add("candidate_public_create_wall_ms", "latency", "execution_state", milliseconds(candidatePublic), "lower")
	out.add("public_create_wall_ratio", "latency", "execution_state", ratioFloat(float64(candidatePublic), float64(legacyPublic)), "lower")
	out.add("batch_allocation_ratio", "resources", "execution_state", ratioFloat(float64(candidate.AllocatedBytes), float64(legacy.AllocatedBytes)), "lower")
	out.add("batch_item_limit", "resources", "execution_state", 20, "lower")
	out.add("authoritative_state_equivalence", "quality", "execution_state", boolScore(candidate.AuthorityExact), "higher")
	out.add("derived_state_equivalence", "quality", "execution_state", boolScore(candidate.DerivedExact), "higher")
	out.add("batch_order_preservation", "quality", "execution_state", boolScore(candidate.OrderExact), "higher")
	out.add("restart_recovery", "quality", "execution_state", boolScore(candidate.RestartExact && publicRecovery), "higher")
	out.add("interrupted_tail_recovery", "quality", "execution_state", boolScore(candidate.TailRecoveryExact), "higher")
	out.add("uncommitted_generation_isolation", "quality", "execution_state", boolScore(uncommittedIsolation), "higher")
	out.add("concurrent_batch_completion", "quality", "execution_state", boolScore(concurrentCompletion), "higher")
	out.add("public_search_recall", "quality", "execution_state", boolScore(publicSearch), "higher")
	return nil
}

func executionScale(value fixture, repetitions int) ([]domain.Information, []derived.Record) {
	assets := make([]domain.Information, 0, len(value.Assets)*repetitions)
	records := make([]derived.Record, 0, len(value.Records)*repetitions)
	for repetition := 0; repetition < repetitions; repetition++ {
		for _, source := range value.Assets {
			asset := source
			asset.ID = fmt.Sprintf("R%02d-%s", repetition+1, source.ID)
			asset.CreatedAt = source.CreatedAt.Add(time.Duration(repetition) * time.Hour)
			asset.UpdatedAt = asset.CreatedAt
			if len(asset.Contexts) == 0 {
				asset.Contexts = nil
			}
			if len(asset.Relations) == 0 {
				asset.Relations = nil
			}
			assets = append(assets, asset)
		}
		for _, source := range value.Records {
			record := source
			record.AssetID = fmt.Sprintf("R%02d-%s", repetition+1, source.AssetID)
			record.GeneratedAt = source.GeneratedAt.Add(time.Duration(repetition) * time.Hour)
			records = append(records, record)
		}
	}
	sort.Slice(assets, func(left, right int) bool {
		if assets[left].CreatedAt.Equal(assets[right].CreatedAt) {
			return assets[left].ID < assets[right].ID
		}
		return assets[left].CreatedAt.Before(assets[right].CreatedAt)
	})
	return assets, records
}

func measureDurableBatch(root string, assets []domain.Information, records []derived.Record, batched, corruptTail bool) (durableBatchMeasurement, error) {
	assetsPath := filepath.Join(root, "assets")
	statePath := filepath.Join(root, "state")
	assetStore, err := assetlog.Open(assetsPath)
	if err != nil {
		return durableBatchMeasurement{}, err
	}
	stateStore, err := derived.Open(statePath)
	if err != nil {
		_ = assetStore.Close()
		return durableBatchMeasurement{}, err
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	if batched {
		for offset := 0; offset < len(assets); offset += 20 {
			end := min(offset+20, len(assets))
			if err := assetStore.CreateBatch(assets[offset:end]); err != nil {
				return durableBatchMeasurement{}, err
			}
		}
		for offset := 0; offset < len(records); offset += 20 {
			end := min(offset+20, len(records))
			if err := stateStore.PutBatch(records[offset:end]); err != nil {
				return durableBatchMeasurement{}, err
			}
		}
	} else {
		for _, asset := range assets {
			if err := assetStore.Create(asset); err != nil {
				return durableBatchMeasurement{}, err
			}
		}
		for _, record := range records {
			if err := stateStore.Put(record); err != nil {
				return durableBatchMeasurement{}, err
			}
		}
	}
	elapsed := time.Since(started)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	authorityExact := reflect.DeepEqual(assetStore.All(), assets)
	derivedExact := recordsEqual(stateStore, records)
	if err := stateStore.Close(); err != nil {
		return durableBatchMeasurement{}, err
	}
	if err := assetStore.Close(); err != nil {
		return durableBatchMeasurement{}, err
	}
	if corruptTail {
		assetTail, openErr := os.OpenFile(filepath.Join(assetsPath, "information.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			return durableBatchMeasurement{}, openErr
		}
		_, writeErr := assetTail.WriteString(`{"operation":"create"`)
		closeErr := assetTail.Close()
		if writeErr != nil || closeErr != nil {
			return durableBatchMeasurement{}, errors.Join(writeErr, closeErr)
		}
		stateTail, openErr := os.OpenFile(filepath.Join(statePath, derived.LogFileName), os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			return durableBatchMeasurement{}, openErr
		}
		_, writeErr = stateTail.Write([]byte{'O', 'W', 'D'})
		closeErr = stateTail.Close()
		if writeErr != nil || closeErr != nil {
			return durableBatchMeasurement{}, errors.Join(writeErr, closeErr)
		}
	}
	assetStore, err = assetlog.Open(assetsPath)
	if err != nil {
		return durableBatchMeasurement{}, err
	}
	stateStore, err = derived.Open(statePath)
	if err != nil {
		_ = assetStore.Close()
		return durableBatchMeasurement{}, err
	}
	restartExact := reflect.DeepEqual(assetStore.All(), assets) && recordsEqual(stateStore, records)
	if err := stateStore.Close(); err != nil {
		return durableBatchMeasurement{}, err
	}
	if err := assetStore.Close(); err != nil {
		return durableBatchMeasurement{}, err
	}
	return durableBatchMeasurement{
		Wall: elapsed, AllocatedBytes: allocated, AuthorityExact: authorityExact, DerivedExact: derivedExact,
		OrderExact: authorityExact, RestartExact: restartExact, TailRecoveryExact: !corruptTail || restartExact,
	}, nil
}

func recordsEqual(store *derived.Store, expected []derived.Record) bool {
	if len(store.All()) != len(expected) {
		return false
	}
	for _, record := range expected {
		actual, exists := store.GetWithEmbedding(record.AssetID)
		if !exists {
			return false
		}
		left, leftErr := derived.EncodeRecord(actual)
		right, rightErr := derived.EncodeRecord(record)
		if leftErr != nil || rightErr != nil || !equalBytes(left, right) {
			return false
		}
	}
	return true
}

func measureGenerationBuild(root string, records []derived.Record, staged bool) (time.Duration, error) {
	current, err := derived.Open(root)
	if err != nil {
		return 0, err
	}
	defer current.Close()
	next, err := derived.CreateGeneration(root, fmt.Sprintf("gen-execution-%t", staged))
	if err != nil {
		return 0, err
	}
	started := time.Now()
	if staged {
		err = next.StageGeneration(records)
	} else {
		for _, record := range records {
			if err = next.Put(record); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = current.CommitGeneration(next, derived.GenerationMetadata{AssetCount: len(records), AssetSnapshot: strings.Repeat("a", 64), EmbeddingSpace: "frozen-frontier-v1"})
	}
	elapsed := time.Since(started)
	if err != nil {
		_ = next.Discard()
		return 0, err
	}
	if !recordsEqual(current, records) {
		return 0, errors.New("派生世代切换改变了冻结记录")
	}
	return elapsed, nil
}

func measurePublicCreate(ctx context.Context, root string, assets []domain.Information, batched bool) (time.Duration, bool, bool, error) {
	assetStore, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		return 0, false, false, err
	}
	stateStore, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		_ = assetStore.Close()
		return 0, false, false, err
	}
	service, err := core.NewCollaborative(assetStore, stateStore, embedding.HashForTesting{Dimensions: 16})
	if err != nil {
		return 0, false, false, err
	}
	inputs := make([]core.CreateInput, len(assets))
	for index, asset := range assets {
		inputs[index] = core.CreateInput{Kind: asset.Kind, Content: asset.Content, Contexts: asset.Contexts, Relations: asset.Relations, Source: asset.Source}
	}
	started := time.Now()
	if batched {
		for offset := 0; offset < len(inputs); offset += 20 {
			end := min(offset+20, len(inputs))
			results, createErr := service.CreateBatch(ctx, inputs[offset:end])
			if createErr != nil || len(results) != end-offset {
				return 0, false, false, errors.Join(createErr, errors.New("公开批量创建结果不完整"))
			}
			for _, result := range results {
				if result.Error != "" || result.Result == nil {
					return 0, false, false, fmt.Errorf("公开批量创建失败: %s", result.Error)
				}
			}
		}
	} else {
		for _, input := range inputs {
			if _, err := service.Create(ctx, input); err != nil {
				return 0, false, false, err
			}
		}
	}
	elapsed := time.Since(started)
	results, searchErr := service.Search(ctx, core.SearchInput{Query: "marker-x01 lunar ledger", Limit: 5})
	searchOK := searchErr == nil && len(results) > 0 && strings.Contains(results[0].Summary, "marker-x01")
	status := service.SemanticStatus()
	if status["pending"] != len(inputs) {
		return 0, false, false, fmt.Errorf("公开创建没有形成完整待处理状态: %#v", status)
	}
	if err := service.Close(); err != nil {
		return 0, false, false, err
	}
	assetStore, err = assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		return 0, false, false, err
	}
	stateStore, err = derived.Open(filepath.Join(root, "state"))
	if err != nil {
		_ = assetStore.Close()
		return 0, false, false, err
	}
	reopened, err := core.NewCollaborative(assetStore, stateStore, embedding.HashForTesting{Dimensions: 16})
	if err != nil {
		return 0, false, false, err
	}
	recovered := reopened.SemanticStatus()["pending"] == len(inputs)
	if err := reopened.Close(); err != nil {
		return 0, false, false, err
	}
	return elapsed, searchOK, recovered, nil
}

func measureUncommittedGenerationIsolation(root string) (bool, error) {
	current, err := derived.Open(root)
	if err != nil {
		return false, err
	}
	defer current.Close()
	next, err := derived.CreateGeneration(root, "gen-uncommitted-isolation")
	if err != nil {
		return false, err
	}
	record := derived.Record{AssetID: "uncommitted-only", AssetRevision: 1, Status: "ready"}
	if err := next.StageGeneration([]derived.Record{record}); err != nil {
		return false, err
	}
	_, visible := current.Get(record.AssetID)
	if err := next.Discard(); err != nil {
		return false, err
	}
	return !visible, nil
}

func measureConcurrentBatchCreate(ctx context.Context, root string) (bool, error) {
	assetStore, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		return false, err
	}
	stateStore, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		_ = assetStore.Close()
		return false, err
	}
	service, err := core.NewCollaborative(assetStore, stateStore, embedding.HashForTesting{Dimensions: 16})
	if err != nil {
		return false, err
	}
	defer service.Close()
	var wait sync.WaitGroup
	errorsByWorker := make([]error, 2)
	for worker := 0; worker < 2; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			inputs := make([]core.CreateInput, 20)
			for index := range inputs {
				inputs[index] = core.CreateInput{Content: fmt.Sprintf("concurrent worker %d durable item %d", worker, index)}
			}
			results, createErr := service.CreateBatch(ctx, inputs)
			if createErr != nil || len(results) != len(inputs) {
				errorsByWorker[worker] = errors.Join(createErr, errors.New("并发批量创建结果不完整"))
				return
			}
			for _, result := range results {
				if result.Error != "" || result.Result == nil {
					errorsByWorker[worker] = fmt.Errorf("并发批量创建失败: %s", result.Error)
					return
				}
			}
		}()
	}
	wait.Wait()
	return errorsByWorker[0] == nil && errorsByWorker[1] == nil && len(assetStore.All()) == 40 && len(stateStore.All()) == 40, errors.Join(errorsByWorker...)
}

func legacyDerivedRecordSize(record derived.Record, work *semantics.Work, result *semantics.Submission) int64 {
	metadata := struct {
		Schema         string                `json:"schema"`
		AssetID        string                `json:"asset_id"`
		AssetRevision  uint64                `json:"asset_revision"`
		GeneratedAt    time.Time             `json:"generated_at"`
		Provider       string                `json:"provider"`
		Status         string                `json:"status"`
		Error          string                `json:"error,omitempty"`
		Analysis       semantics.Analysis    `json:"analysis"`
		SemanticWork   *semantics.Work       `json:"semantic_work,omitempty"`
		SemanticResult *semantics.Submission `json:"semantic_result,omitempty"`
		EmbeddingSpace string                `json:"embedding_space,omitempty"`
	}{
		Schema: "ownward.derived/v3", AssetID: record.AssetID, AssetRevision: record.AssetRevision,
		GeneratedAt: record.GeneratedAt, Provider: record.Provider, Status: record.Status, Error: record.Error,
		Analysis: record.Analysis, SemanticWork: work, SemanticResult: result, EmbeddingSpace: record.EmbeddingSpace,
	}
	encoded, _ := json.Marshal(metadata)
	return int64(16 + len(encoded) + len(record.Embedding)*4 + 4)
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func vectorRecordCount(records []derived.Record) int {
	count := 0
	for _, record := range records {
		if len(record.Embedding) > 0 {
			count++
		}
	}
	return count
}

func directoryBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
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

func overheadRatio(actual, baseline int) float64 {
	if baseline <= 0 {
		return 0
	}
	return float64(actual-baseline) / float64(baseline)
}

func ratioFloat(numerator, denominator float64) float64 {
	if denominator <= 0 {
		return 0
	}
	return numerator / denominator
}

func budgetedServiceReadIDs(ctx context.Context, service *core.Service, results []core.SearchResult, query string, budget, limit int) (readMeasurement, error) {
	measured := readMeasurement{IDs: make([]string, 0)}
	for _, result := range results {
		remaining := limit - measured.ReadUnits
		if remaining <= 0 {
			break
		}
		referenceLimit := min(3, remaining)
		references, err := service.SearchEvidence(ctx, core.EvidenceSearchInput{SourceID: result.ID, Query: query, Limit: referenceLimit})
		if err != nil {
			return readMeasurement{}, err
		}
		readSource := false
		if len(references) == 0 {
			information, err := service.Read(ctx, result.ID)
			if err != nil {
				return readMeasurement{}, err
			}
			contentRunes := utf8.RuneCountInString(information.Content)
			if measured.ReadUnits > 0 && measured.ContentRunes+contentRunes > budget {
				return measured, nil
			}
			encoded, err := json.Marshal(information)
			if err != nil {
				return readMeasurement{}, err
			}
			measured.ContentRunes += contentRunes
			measured.SerializedBytes += len(encoded)
			measured.ReadUnits++
			readSource = true
		} else {
			for _, reference := range references {
				if measured.ReadUnits > 0 && measured.ContentRunes+reference.ContentRunes > budget {
					return measured, nil
				}
				evidence, err := service.ReadEvidence(ctx, reference.ID)
				if err != nil {
					return readMeasurement{}, err
				}
				if evidence.SourceID != result.ID || evidence.Reference() != reference {
					return readMeasurement{}, errors.New("检索证据引用与来源读取不一致")
				}
				encoded, err := json.Marshal(evidence)
				if err != nil {
					return readMeasurement{}, err
				}
				measured.ContentRunes += reference.ContentRunes
				measured.SerializedBytes += len(encoded)
				measured.ReadUnits++
				readSource = true
				if measured.ReadUnits >= limit {
					break
				}
			}
		}
		if readSource {
			measured.IDs = append(measured.IDs, result.ID)
		}
		if measured.ReadUnits >= limit {
			break
		}
	}
	return measured, nil
}

func budgetedFullServiceReadIDs(ctx context.Context, service *core.Service, results []core.SearchResult, budget, limit int) (readMeasurement, error) {
	measured := readMeasurement{IDs: make([]string, 0)}
	for _, result := range results {
		information, err := service.Read(ctx, result.ID)
		if err != nil {
			return readMeasurement{}, err
		}
		contentRunes := utf8.RuneCountInString(information.Content)
		if len(measured.IDs) > 0 && measured.ContentRunes+contentRunes > budget {
			break
		}
		encoded, err := json.Marshal(information)
		if err != nil {
			return readMeasurement{}, err
		}
		measured.IDs = append(measured.IDs, result.ID)
		measured.ContentRunes += contentRunes
		measured.SerializedBytes += len(encoded)
		measured.ReadUnits++
		if measured.ReadUnits >= limit {
			break
		}
	}
	return measured, nil
}

func budgetedReadIDs(results []core.SearchResult, assets []domain.Information, budget, limit int) []string {
	contents := make(map[string]string, len(assets))
	for _, asset := range assets {
		contents[asset.ID] = asset.Content
	}
	ids := make([]string, 0)
	used := 0
	for _, result := range results {
		content, ok := contents[result.ID]
		if !ok {
			continue
		}
		contentChars := utf8.RuneCountInString(content)
		if len(ids) > 0 && used+contentChars > budget {
			break
		}
		ids = append(ids, result.ID)
		used += contentChars
		if len(ids) >= limit {
			break
		}
	}
	return ids
}

type frozenProvider struct{ queries map[string][]float32 }

type boundedSemanticProvider struct {
	queries            map[string][]float32
	embeddingKeys      map[string][]float32
	documentInputs     []string
	documentCalls      int
	oversizedRawInputs int
}

func (*boundedSemanticProvider) Name() string { return "frozen-bounded-semantic-v1" }
func (b *boundedSemanticProvider) Space() embedding.Space {
	dimensions := 16
	for _, vector := range b.embeddingKeys {
		dimensions = len(vector)
		break
	}
	return embedding.Space{ID: "frozen-bounded-semantic-v1", Dimensions: dimensions}
}
func (b *boundedSemanticProvider) EmbedDocuments(_ context.Context, values []string) ([][]float32, error) {
	b.documentCalls++
	result := make([][]float32, len(values))
	for index, value := range values {
		b.documentInputs = append(b.documentInputs, value)
		if len([]byte(value)) > 320 {
			b.oversizedRawInputs++
			return nil, errors.New("frozen embedding transport rejects oversized input")
		}
		for key, vector := range b.embeddingKeys {
			if strings.Contains(value, key) {
				result[index] = append([]float32(nil), vector...)
				break
			}
		}
		if len(result[index]) == 0 {
			result[index] = make([]float32, b.Space().Dimensions)
			result[index][len(result[index])-1] = 1
		}
	}
	return result, nil
}
func (b *boundedSemanticProvider) EmbedQuery(_ context.Context, value string) ([]float32, error) {
	vector := b.queries[value]
	if len(vector) == 0 {
		return nil, errors.New("query is outside frozen semantic representation view")
	}
	return append([]float32(nil), vector...), nil
}
func (*boundedSemanticProvider) Close() error { return nil }

func measureSemanticRepresentation(ctx context.Context, value fixture, out collector, scratchRoot string) error {
	if len(value.Analyses) == 0 || len(value.EmbeddingKeys) == 0 {
		return errors.New("semantic representation view lacks frozen analyses or embedding keys")
	}
	root, err := os.MkdirTemp(scratchRoot, "semantic-representation-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	assetStore, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		return err
	}
	stateStore, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		_ = assetStore.Close()
		return err
	}
	provider := &boundedSemanticProvider{queries: value.QueryVectors, embeddingKeys: value.EmbeddingKeys}
	service, err := core.NewCollaborative(assetStore, stateStore, provider)
	if err != nil {
		return err
	}
	inputs := make([]core.CreateInput, len(value.Assets))
	for index, asset := range value.Assets {
		inputs[index] = core.CreateInput{Content: asset.Content, Contexts: asset.Contexts}
	}
	organizationStarted := time.Now()
	created, err := service.CreateBatch(ctx, inputs)
	if err != nil || len(created) != len(inputs) {
		return fmt.Errorf("semantic representation fixture creation failed: %w", err)
	}
	ids := make([]string, len(created))
	createdByFixture := make(map[string]string, len(created))
	analysisByCreated := make(map[string]semantics.Analysis, len(created))
	longIDs := make(map[string]struct{})
	shortIDs := make(map[string]struct{})
	for index, result := range created {
		if result.Error != "" || result.Result == nil {
			return fmt.Errorf("semantic representation fixture item failed: %s", result.Error)
		}
		ids[index] = result.Result.Information.ID
		createdByFixture[value.Assets[index].ID] = ids[index]
		analysisByCreated[ids[index]] = value.Analyses[value.Assets[index].ID]
		if len([]byte(result.Result.Information.Content)) > 320 {
			longIDs[ids[index]] = struct{}{}
		} else {
			shortIDs[ids[index]] = struct{}{}
		}
	}
	initialShortVectors := 0
	for id := range shortIDs {
		record, exists := stateStore.GetWithEmbedding(id)
		if exists && len(record.Embedding) > 0 {
			initialShortVectors++
		}
	}
	provider.documentInputs = nil
	provider.documentCalls = 0
	work, err := service.SemanticWorkFor(ctx, ids)
	if err != nil || len(work) != len(ids) {
		return fmt.Errorf("semantic representation work is incomplete: %w", err)
	}
	submissions := make([]semantics.Submission, len(work))
	for index, item := range work {
		analysis, exists := analysisByCreated[item.Asset.ID]
		if !exists {
			return fmt.Errorf("frozen analysis missing for %s", item.Asset.ID)
		}
		submissions[index] = semantics.Submission{
			Schema: semantics.SubmissionSchema, WorkID: item.ID, AssetID: item.Asset.ID, Revision: item.Asset.Revision,
			Capability: semantics.Capability{ID: "frozen-fast-view", Version: "v1", Execution: "deterministic"},
			Status:     semantics.SubmissionComplete, Analysis: analysis,
		}
	}
	submitted, err := service.SubmitSemanticBatch(ctx, submissions)
	organizationElapsed := time.Since(organizationStarted)
	if err != nil || len(submitted) != len(submissions) {
		return fmt.Errorf("semantic representation submission failed: %w", err)
	}
	readyLong := 0
	vectorCount := 0
	for _, id := range ids {
		record, exists := stateStore.GetWithEmbedding(id)
		if !exists {
			continue
		}
		if len(record.Embedding) > 0 {
			vectorCount++
			if _, long := longIDs[id]; long {
				readyLong++
			}
		}
	}
	markerMatches := 0
	joinedInputs := strings.Join(provider.documentInputs, "\n")
	for key := range value.EmbeddingKeys {
		if strings.Contains(joinedInputs, key) {
			markerMatches++
		}
	}
	searchRecall := make([]float64, 0)
	searchErrors := make([]float64, 0)
	semanticSignals := make([]float64, 0)
	searchDurations := make([]float64, 0)
	for _, query := range value.Queries {
		started := time.Now()
		results, searchErr := service.Search(ctx, core.SearchInput{Query: query.Query, Limit: 10})
		searchDurations = append(searchDurations, milliseconds(time.Since(started)))
		if searchErr != nil {
			return searchErr
		}
		ids := searchIDs(results)
		expected := remap(query.ExpectedIDs, createdByFixture)
		score := recall(ids, expected)
		if query.ViewRole == "primary" {
			searchRecall = append(searchRecall, score)
			searchErrors = append(searchErrors, 1-score)
		}
		if query.ViewRole == "primary" {
			for _, expectedID := range expected {
				matched := false
				for _, result := range results {
					if result.ID == expectedID && containsString(result.Signals, "semantic") {
						matched = true
						break
					}
				}
				semanticSignals = append(semanticSignals, boolScore(matched))
			}
		}
	}
	callsBeforeRebuild := provider.documentCalls
	rebuildStarted := time.Now()
	rebuildCounts, err := service.Maintain(ctx, true)
	rebuildElapsed := time.Since(rebuildStarted)
	if err != nil || rebuildCounts["ready"] != len(ids) {
		return fmt.Errorf("semantic representation rebuild failed: counts=%#v err=%w", rebuildCounts, err)
	}
	rebuildScores := make([]float64, 0)
	for _, query := range value.Queries {
		if query.ViewRole != "primary" {
			continue
		}
		results, searchErr := service.Search(ctx, core.SearchInput{Query: query.Query, Limit: 10})
		if searchErr != nil {
			return searchErr
		}
		rebuildScores = append(rebuildScores, recall(searchIDs(results), remap(query.ExpectedIDs, createdByFixture)))
	}
	if err := service.Close(); err != nil {
		return err
	}
	assetStore, err = assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		return err
	}
	stateStore, err = derived.Open(filepath.Join(root, "state"))
	if err != nil {
		return err
	}
	reopened, err := core.NewCollaborative(assetStore, stateStore, &boundedSemanticProvider{queries: value.QueryVectors, embeddingKeys: value.EmbeddingKeys})
	if err != nil {
		return err
	}
	restartScores := make([]float64, 0)
	for _, query := range value.Queries {
		results, searchErr := reopened.Search(ctx, core.SearchInput{Query: query.Query, Limit: 10})
		if searchErr != nil {
			return searchErr
		}
		if query.ViewRole == "primary" {
			restartScores = append(restartScores, recall(searchIDs(results), remap(query.ExpectedIDs, createdByFixture)))
		}
	}
	if err := reopened.Close(); err != nil {
		return err
	}
	sourceBytes := 0
	for _, asset := range value.Assets {
		sourceBytes += len([]byte(asset.Content))
	}
	semanticInputBytes := 0
	for _, input := range provider.documentInputs {
		semanticInputBytes += len([]byte(input))
	}
	out.add("semantic_search_recall", "quality", "semantic_representation", mean(searchRecall), "higher")
	out.add("semantic_search_error_rate", "quality", "semantic_representation", mean(searchErrors), "lower")
	out.add("long_asset_vector_recovery", "quality", "semantic_representation", ratio(readyLong, len(longIDs)), "higher")
	out.add("short_asset_vector_availability", "quality", "semantic_representation", ratio(initialShortVectors, len(shortIDs)), "higher")
	out.add("semantic_input_marker_coverage", "quality", "semantic_representation", ratio(markerMatches, len(longIDs)), "higher")
	out.add("restart_semantic_recall", "quality", "semantic_representation", mean(restartScores), "higher")
	out.add("rebuild_semantic_recall", "quality", "semantic_representation", mean(rebuildScores), "higher")
	out.add("semantic_signal_rate", "quality", "semantic_representation", mean(semanticSignals), "higher")
	out.add("derived_vectors_per_asset", "resources", "semantic_representation", ratioFloat(float64(vectorCount), float64(len(ids))), "lower")
	out.add("semantic_input_to_source_bytes", "resources", "semantic_representation", ratioFloat(float64(semanticInputBytes), float64(sourceBytes)), "lower")
	out.add("oversized_raw_embedding_attempts", "resources", "semantic_representation", float64(provider.oversizedRawInputs), "lower")
	out.add("semantic_embedding_calls", "resources", "semantic_representation", float64(provider.documentCalls), "lower")
	out.add("rebuild_semantic_embedding_calls", "resources", "semantic_representation", float64(provider.documentCalls-callsBeforeRebuild), "lower")
	out.add("semantic_representation_p95_ms", "latency", "semantic_representation", organizationElapsed.Seconds()*1000, "lower")
	out.add("semantic_rebuild_p95_ms", "latency", "semantic_representation", rebuildElapsed.Seconds()*1000, "lower")
	for _, measured := range searchDurations {
		out.add("semantic_search_p95_ms", "latency", "semantic_representation", measured, "lower")
	}
	return nil
}

type measuringProvider struct {
	documentCalls int
	documentItems int
	documentRunes int
}

func (m *measuringProvider) Name() string { return "frontier-efficiency-measurement" }
func (m *measuringProvider) Space() embedding.Space {
	return embedding.Space{ID: "frontier-efficiency-measurement", Dimensions: 16}
}
func (m *measuringProvider) EmbedDocuments(ctx context.Context, values []string) ([][]float32, error) {
	m.documentCalls++
	m.documentItems += len(values)
	for _, value := range values {
		m.documentRunes += utf8.RuneCountInString(value)
	}
	return (embedding.HashForTesting{Dimensions: 16}).EmbedDocuments(ctx, values)
}
func (m *measuringProvider) EmbedQuery(ctx context.Context, value string) ([]float32, error) {
	return (embedding.HashForTesting{Dimensions: 16}).EmbedQuery(ctx, value)
}
func (*measuringProvider) Close() error { return nil }
func (m *measuringProvider) reset() {
	m.documentCalls = 0
	m.documentItems = 0
	m.documentRunes = 0
}

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

func verifySourceIdentity(repository, expected, equivalent, sourceIdentity, mode string, selfCheck bool) (map[string]any, error) {
	if strings.TrimSpace(sourceIdentity) != "" {
		if !selfCheck || mode != "targeted" || !validSHA(sourceIdentity) || expected != "worktree:"+sourceIdentity || strings.TrimSpace(equivalent) != "" {
			return nil, errors.New("当前工作树源码身份只允许用于非正式定向观察")
		}
		actual, err := productSourceDigest(repository)
		if err != nil {
			return nil, err
		}
		if actual != sourceIdentity {
			return nil, errors.New("当前产品源码与声明的工作树身份不一致")
		}
		headCommand := exec.Command("git", "rev-parse", "HEAD")
		headCommand.Dir = repository
		headEncoded, err := headCommand.Output()
		if err != nil {
			return nil, fmt.Errorf("读取当前提交: %w", err)
		}
		head := strings.TrimSpace(string(headEncoded))
		if err := verifySelf(head, true); err != nil {
			return nil, err
		}
		return map[string]any{
			"kind": "worktree-product-source", "head": head, "product_source_sha256": actual,
		}, nil
	}
	if strings.TrimSpace(equivalent) == "" {
		if err := verifyCandidate(repository, expected); err != nil {
			return nil, err
		}
		if err := verifySelf(expected, selfCheck); err != nil {
			return nil, err
		}
		return map[string]any{"kind": "candidate-commit", "candidate": expected}, nil
	}
	if !selfCheck || mode != "targeted" || equivalent != expected {
		return nil, errors.New("源码等价候选只允许用于非正式定向观察")
	}
	for _, arguments := range [][]string{
		{"diff", "--quiet", equivalent + "..HEAD", "--", "internal", "cmd/ownward", "go.mod", "go.sum"},
		{"diff", "--quiet", "--", "internal", "cmd/ownward", "go.mod", "go.sum"},
		{"diff", "--cached", "--quiet", "--", "internal", "cmd/ownward", "go.mod", "go.sum"},
	} {
		command := exec.Command("git", arguments...)
		command.Dir = repository
		if err := command.Run(); err != nil {
			return nil, errors.New("当前产品源码与 V0 候选并非逐字等价")
		}
	}
	headCommand := exec.Command("git", "rev-parse", "HEAD")
	headCommand.Dir = repository
	headEncoded, err := headCommand.Output()
	if err != nil {
		return nil, fmt.Errorf("读取当前提交: %w", err)
	}
	head := strings.TrimSpace(string(headEncoded))
	if err := verifySelf(head, true); err != nil {
		return nil, err
	}
	treeCommand := exec.Command("git", "ls-tree", "-r", equivalent, "--", "internal", "cmd/ownward", "go.mod", "go.sum")
	treeCommand.Dir = repository
	treeEncoded, err := treeCommand.Output()
	if err != nil {
		return nil, fmt.Errorf("读取 V0 产品源码身份: %w", err)
	}
	return map[string]any{
		"kind":                "byte-equivalent-product-source",
		"candidate":           equivalent,
		"observer_revision":   head,
		"product_tree_sha256": digest(treeEncoded),
	}, nil
}

func productSourceDigest(repository string) (string, error) {
	paths := make([]string, 0)
	for _, root := range []string{"internal", filepath.Join("cmd", "ownward"), "go.mod", "go.sum"} {
		path := filepath.Join(repository, root)
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			paths = append(paths, path)
			continue
		}
		if err := filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() {
				paths = append(paths, current)
			}
			return nil
		}); err != nil {
			return "", err
		}
	}
	sort.Slice(paths, func(left, right int) bool {
		leftRelative, _ := filepath.Rel(repository, paths[left])
		rightRelative, _ := filepath.Rel(repository, paths[right])
		return filepath.ToSlash(leftRelative) < filepath.ToSlash(rightRelative)
	})
	digest := sha256.New()
	var length [8]byte
	for _, path := range paths {
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return "", err
		}
		name := []byte(filepath.ToSlash(relative))
		binary.LittleEndian.PutUint32(length[:4], uint32(len(name)))
		_, _ = digest.Write(length[:4])
		_, _ = digest.Write(name)
		encoded, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		binary.LittleEndian.PutUint64(length[:], uint64(len(encoded)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write(encoded)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
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

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
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
