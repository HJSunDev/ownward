package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

const (
	AssetAuthorityContract     = "ownward.asset-authority"
	ControlStateContract       = "ownward.control-state"
	ProductCapabilityContract  = "ownward.product-capability"
	KernelLifecycleContract    = "ownward.kernel-lifecycle"
	SemanticCapabilityContract = "ownward.semantic-capability"
	VectorCapabilityContract   = "ownward.vector-capability"
	AccessAdapterContract      = "ownward.access-adapter"
	ProductRulesContract       = "ownward.product-rules"
	AssemblyContract           = "ownward.assembly"
)

type Reference struct {
	ID               string `json:"id"`
	Version          int    `json:"version"`
	DefinitionSHA256 string `json:"definition_sha256"`
}

type Definition struct {
	ID             string   `json:"id"`
	Version        int      `json:"version"`
	Responsibility string   `json:"responsibility"`
	Operations     []string `json:"operations"`
	Schemas        []string `json:"schemas"`
	Source         string   `json:"source"`
}

var definitions = []Definition{
	{AssetAuthorityContract, 1, "权威资产的耐久提交、版本化读取、变化范围、维护、备份与完整恢复", []string{"create", "create_batch", "update_if_revision", "get_current", "list_current", "sync", "compact", "backup", "restore"}, []string{"ownward.information/v1", "ownward.asset-change-scope/v1"}, "internal/contract/asset.go"},
	{ControlStateContract, 1, "保存活动组合、活动内核世代及其修订；已经成立的产品授权决定也只能归此权威边界（当前无，不虚构）", []string{"read", "compare_and_swap"}, []string{ControlStateSchema}, "internal/contract/control.go"},
	{ProductCapabilityContract, 1, "统一表达规则、创建、更新、读取、检索、导航和语义协作的产品语义", []string{"rules", "create", "create_batch", "update", "read", "evidence_search", "evidence_read", "search", "navigate", "semantic_work", "semantic_submit", "semantic_status"}, []string{"ownward.information/v1", "ownward.evidence/v1", "ownward.semantic-work/v1", "ownward.semantic-submission/v1"}, "internal/contract/product.go"},
	{KernelLifecycleContract, 1, "打开、维护、重建和关闭一个确定内核组合，不拥有活动选择权", []string{"maintain", "rebuild", "close"}, []string{"ownward.derived/v4"}, "internal/contract/kernel.go"},
	{SemanticCapabilityContract, 1, "对来源绑定的开放内容产生可校验候选判断，不直接写资产或派生状态", []string{"identity", "analyze_work"}, []string{"ownward.semantic-work/v1", "ownward.semantic-submission/v1"}, "internal/contract/semantic.go"},
	{VectorCapabilityContract, 1, "在声明的同一向量空间中生成文档与查询表示", []string{"space", "embed_documents", "embed_query", "close"}, []string{"ownward.embedding-bundle/v3"}, "internal/contract/vector.go"},
	{AccessAdapterContract, 1, "把外部协议映射到统一产品能力，不拥有产品语义或写入权", []string{"rules", "create", "create_batch", "update", "read", "evidence_search", "evidence_read", "search", "navigate", "semantic_work", "semantic_submit", "semantic_status"}, []string{"mcp/2025-06-18"}, "internal/contract/access.go"},
	{ProductRulesContract, 1, "向所有接入发布同一套信息范围、检索和沉淀规则", []string{"rules"}, []string{"ownward.product-rules/v1"}, "internal/contract/rules.go"},
	{AssemblyContract, 1, "在资源打开前校验当前同进程组合，并以唯一入口显式装配既有产品语义及退化行为", []string{"verify", "open", "close"}, []string{"ownward.composition/v1"}, "internal/composition/manifest.go"},
}

func Definitions() []Definition {
	result := make([]Definition, len(definitions))
	copy(result, definitions)
	for index := range result {
		result[index].Operations = append([]string(nil), result[index].Operations...)
		result[index].Schemas = append([]string(nil), result[index].Schemas...)
	}
	return result
}

func Resolve(id string, version int) (Definition, bool) {
	for _, definition := range definitions {
		if definition.ID == id && definition.Version == version {
			return definition, true
		}
	}
	return Definition{}, false
}

func DefinitionSHA256(definition Definition) (string, error) {
	normalized := definition
	normalized.Source = ""
	normalized.Operations = append([]string(nil), definition.Operations...)
	normalized.Schemas = append([]string(nil), definition.Schemas...)
	sort.Strings(normalized.Operations)
	sort.Strings(normalized.Schemas)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("编码契约定义: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
