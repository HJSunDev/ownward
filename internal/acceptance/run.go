package acceptance

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/candidate"
	"github.com/HJSunDev/ownward/internal/config"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

const reportSchema = "ownward.acceptance-report/v2"

type Options struct {
	BaselinePath string
	OutputPath   string
	DataDir      string
	Candidate    string
	BinaryPath   string
}

type Check struct {
	Name      string             `json:"name"`
	Passed    bool               `json:"passed"`
	Metrics   map[string]float64 `json:"metrics,omitempty"`
	Threshold map[string]float64 `json:"threshold,omitempty"`
	Detail    string             `json:"detail,omitempty"`
}

type QueryResult struct {
	ID              string   `json:"id"`
	Type            string   `json:"type"`
	ReturnedIDs     []string `json:"returned_ids"`
	WithoutGraphIDs []string `json:"without_graph_ids,omitempty"`
	LatencyMillis   float64  `json:"latency_ms"`
}

type Report struct {
	Schema         string            `json:"schema"`
	Candidate      string            `json:"candidate"`
	BinaryVersion  string            `json:"release_binary_version"`
	BinarySHA256   string            `json:"release_binary_sha256"`
	Baseline       string            `json:"baseline"`
	BaselineSHA256 string            `json:"baseline_sha256"`
	DataSHA256     map[string]string `json:"data_sha256"`
	Provider       string            `json:"provider"`
	StartedAt      time.Time         `json:"started_at"`
	FinishedAt     time.Time         `json:"finished_at"`
	Environment    map[string]string `json:"environment"`
	Passed         bool              `json:"passed"`
	Checks         []Check           `json:"checks"`
	Queries        []QueryResult     `json:"queries"`
}

type queryMetrics struct {
	recalls         []float64
	ranks           []float64
	ndcgs           []float64
	graphGains      []float64
	passed          int
	total           int
	forbidden       int
	forbiddenTotal  int
	evidenceCorrect int
	evidenceTotal   int
}

func Run(ctx context.Context, options Options) (Report, error) {
	started := time.Now().UTC()
	binary, err := candidate.Inspect(ctx, options.BinaryPath, options.Candidate)
	if err != nil {
		return Report{}, err
	}
	fixtures, err := loadFixtures(options.BaselinePath)
	if err != nil {
		return Report{}, err
	}
	if fixtures.Thresholds.Organization.KindAccuracy <= 0 {
		return Report{}, fmt.Errorf("验收基线缺少信息类型准确率阈值")
	}
	loaded, err := config.Load(options.DataDir)
	if err != nil {
		return Report{}, err
	}
	provider, err := loaded.SemanticProvider(true)
	if err != nil {
		return Report{}, err
	}
	dataDir := loaded.DataDir
	removeData := false
	if options.DataDir == "" {
		dataDir, err = os.MkdirTemp("", "ownward-acceptance-*")
		if err != nil {
			return Report{}, err
		}
		removeData = true
	}
	if removeData {
		defer os.RemoveAll(dataDir)
	}
	assets, err := assetlog.Open(filepath.Join(dataDir, "assets"))
	if err != nil {
		return Report{}, err
	}
	state, err := derived.Open(filepath.Join(dataDir, "state"))
	if err != nil {
		_ = assets.Close()
		return Report{}, err
	}
	service, err := core.NewOrganized(assets, state, provider)
	if err != nil {
		_ = state.Close()
		_ = assets.Close()
		return Report{}, err
	}
	defer service.Close()
	report := Report{
		Schema:        reportSchema,
		Candidate:     strings.TrimSpace(options.Candidate),
		BinaryVersion: binary.Version,
		BinarySHA256:  binary.SHA256,
		Baseline:      fixtures.Descriptor.Schema,
		Provider:      provider.Name(),
		StartedAt:     started,
		Environment:   map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version()},
	}
	report.BaselineSHA256, report.DataSHA256, err = fixtureDigests(options.BaselinePath, fixtures.Descriptor)
	if err != nil {
		return Report{}, err
	}
	fixtureToAsset := make(map[string]string, len(fixtures.Information))
	assetToFixture := make(map[string]string, len(fixtures.Information))
	expectedContent := make(map[string]string, len(fixtures.Information))
	expectedContexts := make(map[string][]domain.Context, len(fixtures.Information))
	ingestionTimes := make([]time.Duration, 0, len(fixtures.Information))
	for _, item := range fixtures.Information {
		begin := time.Now()
		created, createErr := service.Create(ctx, core.CreateInput{
			Content:  item.Content,
			Contexts: item.Contexts,
			Source:   domain.Source{Actor: "acceptance", Ref: item.FixtureID},
		})
		ingestionTimes = append(ingestionTimes, time.Since(begin))
		if createErr != nil {
			return Report{}, fmt.Errorf("导入验收信息 %s: %w", item.FixtureID, createErr)
		}
		fixtureToAsset[item.FixtureID] = created.Information.ID
		assetToFixture[created.Information.ID] = item.FixtureID
		expectedContent[item.FixtureID] = item.Content
		expectedContexts[item.FixtureID] = item.Contexts
	}
	stableUpdates := 0
	for _, update := range fixtures.Updates {
		assetID := fixtureToAsset[update.FixtureID]
		current, readErr := service.Read(ctx, assetID)
		if readErr != nil {
			return Report{}, fmt.Errorf("读取待更新验收信息 %s: %w", update.FixtureID, readErr)
		}
		content := update.Content
		contexts := append([]domain.Context(nil), update.Contexts...)
		begin := time.Now()
		updated, updateErr := service.Update(ctx, core.UpdateInput{
			ID: assetID, ExpectedRevision: current.Revision, Content: &content, Contexts: &contexts,
		})
		ingestionTimes = append(ingestionTimes, time.Since(begin))
		if updateErr != nil {
			return Report{}, fmt.Errorf("更新验收信息 %s: %w", update.FixtureID, updateErr)
		}
		if updated.Information.ID == assetID && updated.Information.Revision == current.Revision+1 {
			stableUpdates++
		}
		expectedContent[update.FixtureID] = update.Content
		expectedContexts[update.FixtureID] = update.Contexts
	}
	retained := 0
	ready := 0
	for fixtureID, assetID := range fixtureToAsset {
		value, readErr := service.Read(ctx, assetID)
		if readErr == nil && value.Content == expectedContent[fixtureID] && equalContexts(value.Contexts, expectedContexts[fixtureID]) {
			retained++
		}
		if record, exists := state.Get(assetID); exists && record.AssetRevision == value.Revision && record.Status == "ready" {
			ready++
		}
	}
	organizationP95 := p95(ingestionTimes).Seconds()
	report.Checks = append(report.Checks, Check{
		Name:      "信息沉淀与明确语义保持",
		Passed:    retained == len(fixtures.Information) && ready == len(fixtures.Information) && stableUpdates == len(fixtures.Updates) && organizationP95 <= fixtures.Thresholds.Ingestion.OrganizationSeconds,
		Metrics:   map[string]float64{"semantic_retention": ratio(retained, len(fixtures.Information)), "organized_ready": ratio(ready, len(fixtures.Information)), "stable_update_identity": ratio(stableUpdates, len(fixtures.Updates)), "organization_p95_seconds": organizationP95},
		Threshold: map[string]float64{"semantic_retention_min": fixtures.Thresholds.Organization.SemanticRetention, "organized_ready_min": 1, "stable_update_identity_min": 1, "organization_p95_seconds_max": fixtures.Thresholds.Ingestion.OrganizationSeconds},
	})

	report.Checks = append(report.Checks, organizationChecks(state, fixtures, fixtureToAsset, assetToFixture)...)
	retrieval, queries, retrievalErr := evaluateRetrieval(ctx, service, fixtures, assetToFixture)
	if retrievalErr != nil {
		return Report{}, retrievalErr
	}
	report.Checks = append(report.Checks, retrieval...)
	report.Queries = append(report.Queries, queries...)
	recovery, recoveryErr := verifyRecovery(ctx, assets, provider, fixtures, fixtureToAsset, assetToFixture)
	if recoveryErr != nil {
		return Report{}, recoveryErr
	}
	report.Checks = append(report.Checks, recovery)
	report.FinishedAt = time.Now().UTC()
	report.Passed = true
	for _, check := range report.Checks {
		if !check.Passed {
			report.Passed = false
			break
		}
	}
	if options.OutputPath != "" {
		if err := writeReport(options.OutputPath, report); err != nil {
			return Report{}, err
		}
	}
	return report, nil
}

func verifyRecovery(ctx context.Context, source *assetlog.Store, provider semantics.Provider, fixtures fixtureSet, fixtureToAsset, assetToFixture map[string]string) (Check, error) {
	root, err := os.MkdirTemp("", "ownward-acceptance-recovery-*")
	if err != nil {
		return Check{}, err
	}
	defer os.RemoveAll(root)
	backup := filepath.Join(root, "assets.ownward")
	if err := source.Backup(backup); err != nil {
		return Check{}, fmt.Errorf("创建验收备份: %w", err)
	}
	excludesDerived, err := backupContainsOnlyAssets(backup)
	if err != nil {
		return Check{}, err
	}
	restoredAssetDir := filepath.Join(root, "restored", "assets")
	if err := assetlog.Restore(backup, restoredAssetDir); err != nil {
		return Check{}, fmt.Errorf("向空白环境恢复验收备份: %w", err)
	}
	byteEquivalent := true
	for _, name := range []string{"manifest.json", "information.jsonl"} {
		original, readErr := os.ReadFile(filepath.Join(source.Dir(), name))
		if readErr != nil {
			return Check{}, readErr
		}
		restored, readErr := os.ReadFile(filepath.Join(restoredAssetDir, name))
		if readErr != nil {
			return Check{}, readErr
		}
		byteEquivalent = byteEquivalent && bytes.Equal(original, restored)
	}
	restoredAssets, err := assetlog.Open(restoredAssetDir)
	if err != nil {
		return Check{}, err
	}
	restoredState, err := derived.Open(filepath.Join(root, "restored", "state"))
	if err != nil {
		_ = restoredAssets.Close()
		return Check{}, err
	}
	restored, err := core.NewOrganized(restoredAssets, restoredState, provider)
	if err != nil {
		_ = restoredState.Close()
		_ = restoredAssets.Close()
		return Check{}, err
	}
	counts, err := restored.Maintain(ctx, true)
	if err != nil {
		_ = restored.Close()
		return Check{}, fmt.Errorf("从资产重建派生状态: %w", err)
	}
	organization := organizationChecks(restoredState, fixtures, fixtureToAsset, assetToFixture)
	retrieval, _, err := evaluateRetrieval(ctx, restored, fixtures, assetToFixture)
	if err != nil {
		_ = restored.Close()
		return Check{}, err
	}
	postRebuildPassed := allChecksPassed(organization) && allChecksPassed(retrieval)
	rulesAvailable := strings.TrimSpace(restored.Rules(ctx)) != ""
	created, err := restored.Create(ctx, core.CreateInput{
		Kind: domain.KindLesson, Content: "合成闭环：星港-7F4A 项目的恢复口令是蓝色雨燕。",
		Contexts: []domain.Context{{Key: "project", Value: "星港-7F4A"}}, Source: domain.Source{Actor: "acceptance"},
	})
	if err != nil {
		_ = restored.Close()
		return Check{}, fmt.Errorf("恢复后创建信息: %w", err)
	}
	createdReady := created.Organization.Status == "ready"
	createdID := created.Information.ID
	if err := restored.Close(); err != nil {
		return Check{}, err
	}

	reopenedAssets, err := assetlog.Open(restoredAssetDir)
	if err != nil {
		return Check{}, err
	}
	reopenedState, err := derived.Open(filepath.Join(root, "restored", "state"))
	if err != nil {
		_ = reopenedAssets.Close()
		return Check{}, err
	}
	reopened, err := core.NewOrganized(reopenedAssets, reopenedState, provider)
	if err != nil {
		_ = reopenedState.Close()
		_ = reopenedAssets.Close()
		return Check{}, err
	}
	defer reopened.Close()
	read, err := reopened.Read(ctx, createdID)
	if err != nil {
		return Check{}, err
	}
	results, err := reopened.Search(ctx, core.SearchInput{Query: "星港-7F4A 的恢复口令", Contexts: []domain.Context{{Key: "project", Value: "星港-7F4A"}}})
	if err != nil {
		return Check{}, err
	}
	independentSession := containsResult(results, createdID)
	updatedContent := "合成闭环：星港-7F4A 项目的恢复口令是银色雨燕。"
	updated, err := reopened.Update(ctx, core.UpdateInput{ID: createdID, ExpectedRevision: read.Revision, Content: &updatedContent})
	if err != nil {
		return Check{}, err
	}
	updatedRead, err := reopened.Read(ctx, createdID)
	if err != nil {
		return Check{}, err
	}
	updatedResults, err := reopened.Search(ctx, core.SearchInput{Query: "星港-7F4A 银色雨燕"})
	if err != nil {
		return Check{}, err
	}
	mutationClosure := updated.Organization.Status == "ready" && updatedRead.ID == createdID && updatedRead.Revision == 2 && updatedRead.Content == updatedContent && containsResult(updatedResults, createdID)
	metrics := map[string]float64{
		"asset_byte_equivalence": boolNumber(byteEquivalent), "backup_excludes_derived": boolNumber(excludesDerived),
		"rebuilt_ready": ratio(counts["ready"], len(fixtures.Information)), "post_rebuild_acceptance": boolNumber(postRebuildPassed),
		"rules_available": boolNumber(rulesAvailable), "restored_create_ready": boolNumber(createdReady),
		"independent_session_retrieval": boolNumber(independentSession), "restored_mutation_closure": boolNumber(mutationClosure),
	}
	passed := byteEquivalent && excludesDerived && counts["ready"] == len(fixtures.Information) && postRebuildPassed && rulesAvailable && createdReady && independentSession && mutationClosure
	return Check{Name: "资产备份、空白恢复与派生重建", Passed: passed, Metrics: metrics, Threshold: map[string]float64{
		"asset_byte_equivalence_min": 1, "backup_excludes_derived_min": 1, "rebuilt_ready_min": 1, "post_rebuild_acceptance_min": 1,
		"rules_available_min": 1, "restored_create_ready_min": 1, "independent_session_retrieval_min": 1, "restored_mutation_closure_min": 1,
	}}, nil
}

func backupContainsOnlyAssets(path string) (bool, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return false, err
	}
	defer reader.Close()
	expected := map[string]struct{}{"manifest.json": {}, "information.jsonl": {}, "backup.json": {}}
	for _, file := range reader.File {
		if _, ok := expected[file.Name]; !ok {
			return false, nil
		}
		delete(expected, file.Name)
	}
	return len(expected) == 0, nil
}

func allChecksPassed(checks []Check) bool {
	for _, check := range checks {
		if !check.Passed {
			return false
		}
	}
	return true
}

func containsResult(results []core.SearchResult, id string) bool {
	for _, result := range results {
		if result.ID == id {
			return true
		}
	}
	return false
}

func boolNumber(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func fixtureDigests(baselinePath string, descriptor baselineDescriptor) (string, map[string]string, error) {
	absolute, err := filepath.Abs(baselinePath)
	if err != nil {
		return "", nil, err
	}
	baselineDigest, err := fileDigest(absolute)
	if err != nil {
		return "", nil, err
	}
	base := filepath.Dir(absolute)
	paths := map[string]string{
		"thresholds": descriptor.Thresholds, "information": descriptor.Information, "kind_gold": descriptor.KindGold,
		"relation_gold": descriptor.RelationGold, "queries": descriptor.Queries,
	}
	if descriptor.Updates != "" {
		paths["updates"] = descriptor.Updates
	}
	digests := make(map[string]string, len(paths))
	for name, path := range paths {
		digest, digestErr := fileDigest(resolve(base, path))
		if digestErr != nil {
			return "", nil, digestErr
		}
		digests[name] = digest
	}
	return baselineDigest, digests, nil
}

func fileDigest(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	return fmt.Sprintf("%x", digest[:]), nil
}

func organizationChecks(state *derived.Store, fixtures fixtureSet, fixtureToAsset, assetToFixture map[string]string) []Check {
	kindCorrect := 0
	for _, expected := range fixtures.Kinds {
		assetID := fixtureToAsset[expected.FixtureID]
		record, ok := state.Get(assetID)
		if ok && record.Analysis.Kind == expected.Kind {
			kindCorrect++
		}
	}
	kindAccuracy := ratio(kindCorrect, len(fixtures.Kinds))
	expectedRelations := make(map[string]struct{}, len(fixtures.Relations))
	for _, relation := range fixtures.Relations {
		expectedRelations[relationKey(relation.SourceID, relation.Type, relation.TargetID)] = struct{}{}
	}
	actualRelations := make(map[string]struct{})
	for _, record := range state.All() {
		source, sourceOK := assetToFixture[record.AssetID]
		if !sourceOK || record.Status != "ready" {
			continue
		}
		for _, relation := range record.Analysis.Relations {
			target, targetOK := assetToFixture[relation.TargetID]
			if targetOK {
				actualRelations[relationKey(source, relation.Type, target)] = struct{}{}
			}
		}
	}
	trueRelations := intersectionCount(expectedRelations, actualRelations)
	relationPrecision := ratio(trueRelations, len(actualRelations))
	relationRecall := ratio(trueRelations, len(expectedRelations))
	return []Check{
		{Name: "自主信息类型判断", Passed: kindAccuracy >= fixtures.Thresholds.Organization.KindAccuracy,
			Metrics: map[string]float64{"accuracy": kindAccuracy}, Threshold: map[string]float64{"accuracy_min": fixtures.Thresholds.Organization.KindAccuracy}},
		{Name: "自主语义关系组织", Passed: relationPrecision >= fixtures.Thresholds.Organization.RelationPrecision && relationRecall >= fixtures.Thresholds.Organization.RelationRecall,
			Metrics: map[string]float64{"precision": relationPrecision, "recall": relationRecall}, Threshold: map[string]float64{"precision_min": fixtures.Thresholds.Organization.RelationPrecision, "recall_min": fixtures.Thresholds.Organization.RelationRecall}},
	}
}

func evaluateRetrieval(ctx context.Context, service *core.Service, fixtures fixtureSet, assetToFixture map[string]string) ([]Check, []QueryResult, error) {
	byType := make(map[string]*queryMetrics)
	queries := make([]QueryResult, 0, len(fixtures.Queries))
	for _, query := range fixtures.Queries {
		metric := byType[query.Type]
		if metric == nil {
			metric = &queryMetrics{}
			byType[query.Type] = metric
		}
		begin := time.Now()
		results, err := service.Search(ctx, core.SearchInput{Query: query.Query, Contexts: query.Contexts, Limit: 10})
		latency := time.Since(begin)
		if err != nil {
			return nil, nil, fmt.Errorf("执行验收查询 %s: %w", query.QueryID, err)
		}
		withoutGraph, err := service.Search(ctx, core.SearchInput{Query: query.Query, Contexts: query.Contexts, Limit: 10, DisableRelationExpansion: true})
		if err != nil {
			return nil, nil, fmt.Errorf("执行检索消融 %s: %w", query.QueryID, err)
		}
		ids := fixtureIDs(results, assetToFixture)
		withoutIDs := fixtureIDs(withoutGraph, assetToFixture)
		metricLimit := 10
		if query.Type == "explicit_object" {
			metricLimit = 5
		}
		recall := recallAt(ids, query.ExpectedIDs, metricLimit)
		metric.recalls = append(metric.recalls, recall)
		metric.ranks = append(metric.ranks, reciprocalRank(ids, query.ExpectedIDs, 10))
		metric.ndcgs = append(metric.ndcgs, ndcgAt(ids, query.ExpectedIDs, 10))
		metric.total++
		if recall == 1 {
			metric.passed++
		}
		for _, forbidden := range query.ForbiddenIDs {
			metric.forbiddenTotal++
			if containsID(ids, forbidden) {
				metric.forbidden++
			}
		}
		if query.Type == "relation_constraint" {
			queryEvidenceCorrect := 0
			for _, result := range results {
				if !contains(result.Signals, "relation") {
					continue
				}
				metric.evidenceTotal++
				if containsID(query.ExpectedIDs, assetToFixture[result.ID]) {
					metric.evidenceCorrect++
					queryEvidenceCorrect++
				}
			}
			metric.graphGains = append(metric.graphGains, ratio(queryEvidenceCorrect, len(query.ExpectedIDs)))
		}
		queries = append(queries, QueryResult{ID: query.QueryID, Type: query.Type, ReturnedIDs: ids, WithoutGraphIDs: withoutIDs, LatencyMillis: float64(latency.Microseconds()) / 1000})
	}
	return retrievalChecks(byType, fixtures.Thresholds), queries, nil
}

func retrievalChecks(byType map[string]*queryMetrics, limits thresholds) []Check {
	metric := func(name string) *queryMetrics {
		if value := byType[name]; value != nil {
			return value
		}
		return &queryMetrics{}
	}
	explicit := metric("explicit_object")
	semantic := metric("semantic_intent")
	relation := metric("relation_constraint")
	contextual := metric("context_applicability")
	relationEvidenceGain := limits.Organization.RetrievalEvidenceGain
	if relationEvidenceGain == 0 {
		relationEvidenceGain = limits.Organization.RetrievalRecallGain
	}
	checks := []Check{
		{Name: "明确对象检索", Passed: min(explicit.recalls) >= limits.Retrieval.ExplicitObject.RecallAt5 && min(explicit.ranks) >= limits.Retrieval.ExplicitObject.MRRAt10,
			Metrics: map[string]float64{"recall_at_5_min": min(explicit.recalls), "mrr_at_10_min": min(explicit.ranks)}, Threshold: map[string]float64{"recall_at_5_min": limits.Retrieval.ExplicitObject.RecallAt5, "mrr_at_10_min": limits.Retrieval.ExplicitObject.MRRAt10}},
		{Name: "语义意图检索", Passed: min(semantic.recalls) >= limits.Retrieval.SemanticIntent.RecallAt10 && min(semantic.ndcgs) >= limits.Retrieval.SemanticIntent.NDCGAt10,
			Metrics: map[string]float64{"recall_at_10_min": min(semantic.recalls), "ndcg_at_10_min": min(semantic.ndcgs)}, Threshold: map[string]float64{"recall_at_10_min": limits.Retrieval.SemanticIntent.RecallAt10, "ndcg_at_10_min": limits.Retrieval.SemanticIntent.NDCGAt10}},
		{Name: "关系约束检索", Passed: average(relation.recalls) >= limits.Retrieval.RelationConstraint.Recall && ratio(relation.evidenceCorrect, relation.evidenceTotal) >= limits.Retrieval.RelationConstraint.Precision,
			Metrics: map[string]float64{"evidence_recall": average(relation.recalls), "evidence_precision": ratio(relation.evidenceCorrect, relation.evidenceTotal), "ndcg_at_10": average(relation.ndcgs), "graph_evidence_gain": average(relation.graphGains)}, Threshold: map[string]float64{"evidence_recall_min": limits.Retrieval.RelationConstraint.Recall, "evidence_precision_min": limits.Retrieval.RelationConstraint.Precision, "graph_evidence_gain_min": relationEvidenceGain}},
		{Name: "场景适用性检索", Passed: ratio(contextual.passed, contextual.total) >= limits.Retrieval.ContextApplicability.Accuracy && ratio(contextual.forbidden, contextual.forbiddenTotal) <= limits.Retrieval.ContextApplicability.Leakage,
			Metrics: map[string]float64{"accuracy": ratio(contextual.passed, contextual.total), "incompatible_leakage": ratio(contextual.forbidden, contextual.forbiddenTotal)}, Threshold: map[string]float64{"accuracy_min": limits.Retrieval.ContextApplicability.Accuracy, "incompatible_leakage_max": limits.Retrieval.ContextApplicability.Leakage}},
	}
	checks[2].Passed = checks[2].Passed && average(relation.graphGains) >= relationEvidenceGain
	return checks
}

func writeReport(path string, report Report) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary := absolute + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, absolute)
}

func fixtureIDs(results []core.SearchResult, reverse map[string]string) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		if id := reverse[result.ID]; id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func equalContexts(left, right []domain.Context) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]domain.Context(nil), left...)
	rightCopy := append([]domain.Context(nil), right...)
	sort.Slice(leftCopy, func(i, j int) bool {
		return leftCopy[i].Key+"\x00"+leftCopy[i].Value < leftCopy[j].Key+"\x00"+leftCopy[j].Value
	})
	sort.Slice(rightCopy, func(i, j int) bool {
		return rightCopy[i].Key+"\x00"+rightCopy[i].Value < rightCopy[j].Key+"\x00"+rightCopy[j].Value
	})
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func relationKey(source, relationType, target string) string {
	if relationType == "related_to" && source > target {
		source, target = target, source
	}
	return source + "\x00" + relationType + "\x00" + target
}

func intersectionCount(left, right map[string]struct{}) int {
	count := 0
	for value := range left {
		if _, ok := right[value]; ok {
			count++
		}
	}
	return count
}

func recallAt(returned, expected []string, limit int) float64 {
	if len(expected) == 0 {
		return 1
	}
	if len(returned) > limit {
		returned = returned[:limit]
	}
	found := 0
	for _, id := range expected {
		if containsID(returned, id) {
			found++
		}
	}
	return ratio(found, len(expected))
}

func reciprocalRank(returned, expected []string, limit int) float64 {
	if len(returned) > limit {
		returned = returned[:limit]
	}
	for index, id := range returned {
		if containsID(expected, id) {
			return 1 / float64(index+1)
		}
	}
	return 0
}

func ndcgAt(returned, expected []string, limit int) float64 {
	if len(returned) > limit {
		returned = returned[:limit]
	}
	dcg := 0.0
	for index, id := range returned {
		if containsID(expected, id) {
			dcg += 1 / math.Log2(float64(index)+2)
		}
	}
	idcg := 0.0
	for index := 0; index < len(expected) && index < limit; index++ {
		idcg += 1 / math.Log2(float64(index)+2)
	}
	if idcg == 0 {
		return 1
	}
	return dcg / idcg
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func min(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func p95(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]time.Duration(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	index := int(math.Ceil(float64(len(copyValues))*0.95)) - 1
	return copyValues[index]
}

func containsID(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
