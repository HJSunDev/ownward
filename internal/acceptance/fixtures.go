package acceptance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/domain"
)

type baselineDescriptor struct {
	Schema       string `json:"schema"`
	Thresholds   string `json:"thresholds"`
	Information  string `json:"information"`
	KindGold     string `json:"kind_gold"`
	RelationGold string `json:"relation_gold"`
	Queries      string `json:"queries"`
	Updates      string `json:"updates,omitempty"`
}

type thresholds struct {
	Retrieval struct {
		ExplicitObject struct {
			RecallAt5 float64 `json:"recall_at_5_min"`
			MRRAt10   float64 `json:"mrr_at_10_min"`
		} `json:"explicit_object"`
		SemanticIntent struct {
			RecallAt10 float64 `json:"recall_at_10_min"`
			NDCGAt10   float64 `json:"ndcg_at_10_min"`
		} `json:"semantic_intent"`
		RelationConstraint struct {
			Precision float64 `json:"evidence_precision_min"`
			Recall    float64 `json:"evidence_recall_min"`
		} `json:"relation_constraint"`
		ContextApplicability struct {
			Accuracy float64 `json:"accuracy_min"`
			Leakage  float64 `json:"incompatible_context_leakage_max"`
		} `json:"context_applicability"`
	} `json:"retrieval"`
	Organization struct {
		RelationPrecision float64 `json:"relation_precision_min"`
		RelationRecall    float64 `json:"relation_recall_min"`
		SemanticRetention float64 `json:"explicit_semantic_retention_min"`
		RetrievalGain     float64 `json:"retrieval_recall_gain_over_no_graph_min"`
		KindAccuracy      float64 `json:"kind_accuracy_min"`
	} `json:"organization"`
	Ingestion struct {
		OrganizationSeconds float64 `json:"organization_complete_p95_seconds_max"`
	} `json:"ingestion"`
}

type fixtureInformation struct {
	FixtureID string           `json:"fixture_id"`
	Content   string           `json:"content"`
	Contexts  []domain.Context `json:"contexts"`
}

type kindGold struct {
	FixtureID string                 `json:"fixture_id"`
	Kind      domain.InformationKind `json:"kind"`
}

type relationGold struct {
	SourceID string `json:"source_id"`
	Type     string `json:"type"`
	TargetID string `json:"target_id"`
}

type queryFixture struct {
	QueryID              string           `json:"query_id"`
	Type                 string           `json:"type"`
	Query                string           `json:"query"`
	Contexts             []domain.Context `json:"contexts"`
	ExpectedIDs          []string         `json:"expected_ids"`
	ForbiddenIDs         []string         `json:"forbidden_ids"`
	RequiredRelationPath []string         `json:"required_relation_path"`
}

type updateFixture struct {
	FixtureID string           `json:"fixture_id"`
	Content   string           `json:"content"`
	Contexts  []domain.Context `json:"contexts"`
}

type fixtureSet struct {
	Descriptor  baselineDescriptor
	Thresholds  thresholds
	Information []fixtureInformation
	Kinds       []kindGold
	Relations   []relationGold
	Queries     []queryFixture
	Updates     []updateFixture
}

func loadFixtures(path string) (fixtureSet, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fixtureSet{}, err
	}
	var descriptor baselineDescriptor
	if err := readJSON(absolute, &descriptor); err != nil {
		return fixtureSet{}, fmt.Errorf("读取验收基线: %w", err)
	}
	if descriptor.Schema == "" || descriptor.Thresholds == "" {
		return fixtureSet{}, fmt.Errorf("验收基线描述不完整")
	}
	base := filepath.Dir(absolute)
	result := fixtureSet{Descriptor: descriptor}
	if err := readJSON(resolve(base, descriptor.Thresholds), &result.Thresholds); err != nil {
		return fixtureSet{}, fmt.Errorf("读取验收阈值: %w", err)
	}
	if err := readJSONLines(resolve(base, descriptor.Information), &result.Information); err != nil {
		return fixtureSet{}, fmt.Errorf("读取验收信息: %w", err)
	}
	if err := readJSONLines(resolve(base, descriptor.KindGold), &result.Kinds); err != nil {
		return fixtureSet{}, fmt.Errorf("读取类型金标: %w", err)
	}
	if err := readJSONLines(resolve(base, descriptor.RelationGold), &result.Relations); err != nil {
		return fixtureSet{}, fmt.Errorf("读取关系金标: %w", err)
	}
	if err := readJSONLines(resolve(base, descriptor.Queries), &result.Queries); err != nil {
		return fixtureSet{}, fmt.Errorf("读取检索问题: %w", err)
	}
	if descriptor.Updates != "" {
		if err := readJSONLines(resolve(base, descriptor.Updates), &result.Updates); err != nil {
			return fixtureSet{}, fmt.Errorf("读取验收更新: %w", err)
		}
	}
	if err := validateFixtures(result); err != nil {
		return fixtureSet{}, fmt.Errorf("验收基线无效: %w", err)
	}
	return result, nil
}

func validateFixtures(fixtures fixtureSet) error {
	if len(fixtures.Information) == 0 || len(fixtures.Kinds) == 0 || len(fixtures.Relations) == 0 || len(fixtures.Queries) == 0 {
		return fmt.Errorf("信息、类型金标、关系金标和检索问题均不能为空")
	}
	information := make(map[string]struct{}, len(fixtures.Information))
	for _, item := range fixtures.Information {
		if item.FixtureID == "" || item.Content == "" {
			return fmt.Errorf("信息缺少 fixture_id 或 content")
		}
		if _, duplicate := information[item.FixtureID]; duplicate {
			return fmt.Errorf("信息标识 %q 重复", item.FixtureID)
		}
		information[item.FixtureID] = struct{}{}
		for _, context := range item.Contexts {
			if context.Key == "" || context.Value == "" {
				return fmt.Errorf("信息 %q 包含空场景", item.FixtureID)
			}
		}
	}
	kinds := make(map[string]struct{}, len(fixtures.Kinds))
	for _, item := range fixtures.Kinds {
		if _, exists := information[item.FixtureID]; !exists {
			return fmt.Errorf("类型金标引用未知信息 %q", item.FixtureID)
		}
		if _, duplicate := kinds[item.FixtureID]; duplicate {
			return fmt.Errorf("信息 %q 的类型金标重复", item.FixtureID)
		}
		if _, err := domain.ParseKind(string(item.Kind)); err != nil || item.Kind == domain.KindGeneral {
			return fmt.Errorf("信息 %q 的类型金标无效", item.FixtureID)
		}
		kinds[item.FixtureID] = struct{}{}
	}
	if len(kinds) != len(information) {
		return fmt.Errorf("每项信息必须且只能有一个类型金标")
	}
	for _, relation := range fixtures.Relations {
		_, sourceExists := information[relation.SourceID]
		_, targetExists := information[relation.TargetID]
		if !sourceExists || !targetExists || relation.SourceID == relation.TargetID || relation.Type == "" {
			return fmt.Errorf("关系 %q -[%s]-> %q 无效", relation.SourceID, relation.Type, relation.TargetID)
		}
	}
	updates := make(map[string]struct{}, len(fixtures.Updates))
	for _, update := range fixtures.Updates {
		if _, exists := information[update.FixtureID]; !exists || strings.TrimSpace(update.Content) == "" {
			return fmt.Errorf("更新引用未知信息 %q 或缺少内容", update.FixtureID)
		}
		if _, duplicate := updates[update.FixtureID]; duplicate {
			return fmt.Errorf("信息 %q 的更新重复", update.FixtureID)
		}
		updates[update.FixtureID] = struct{}{}
		for _, context := range update.Contexts {
			if context.Key == "" || context.Value == "" {
				return fmt.Errorf("更新 %q 包含空场景", update.FixtureID)
			}
		}
	}
	queryIDs := make(map[string]struct{}, len(fixtures.Queries))
	queryTypes := make(map[string]int)
	allowedTypes := map[string]struct{}{"explicit_object": {}, "semantic_intent": {}, "relation_constraint": {}, "context_applicability": {}}
	for _, query := range fixtures.Queries {
		if query.QueryID == "" || query.Query == "" || len(query.ExpectedIDs) == 0 {
			return fmt.Errorf("检索问题缺少标识、查询或期望结果")
		}
		if _, duplicate := queryIDs[query.QueryID]; duplicate {
			return fmt.Errorf("检索问题标识 %q 重复", query.QueryID)
		}
		if _, allowed := allowedTypes[query.Type]; !allowed {
			return fmt.Errorf("检索问题 %q 类型无效", query.QueryID)
		}
		queryIDs[query.QueryID] = struct{}{}
		queryTypes[query.Type]++
		expected := make(map[string]struct{}, len(query.ExpectedIDs))
		for _, id := range query.ExpectedIDs {
			if _, exists := information[id]; !exists {
				return fmt.Errorf("检索问题 %q 引用未知期望信息 %q", query.QueryID, id)
			}
			expected[id] = struct{}{}
		}
		for _, id := range query.ForbiddenIDs {
			if _, exists := information[id]; !exists {
				return fmt.Errorf("检索问题 %q 引用未知禁止信息 %q", query.QueryID, id)
			}
			if _, conflict := expected[id]; conflict {
				return fmt.Errorf("检索问题 %q 同时期望并禁止信息 %q", query.QueryID, id)
			}
		}
	}
	for queryType := range allowedTypes {
		if queryTypes[queryType] == 0 {
			return fmt.Errorf("缺少 %s 检索问题", queryType)
		}
	}
	return nil
}

func resolve(base, value string) string {
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Clean(filepath.Join(base, filepath.FromSlash(value)))
}

func readJSON(path string, output any) error {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, output)
}

func readJSONLines[T any](path string, output *[]T) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var item T
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return fmt.Errorf("第 %d 行: %w", line, err)
		}
		*output = append(*output, item)
	}
	return scanner.Err()
}
