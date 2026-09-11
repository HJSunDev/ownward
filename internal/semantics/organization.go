package semantics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/HJSunDev/ownward/internal/domain"
)

const OrganizationSchema = "ownward.organization/v1"

// Organization 是可重建的原文组织；字段仅承接外部智能的判断。
type Organization struct {
	Schema   string         `json:"schema"`
	Snapshot string         `json:"snapshot,omitempty"`
	Units    []SemanticUnit `json:"units"`
	Links    []GroundedLink `json:"links"`
}

type SemanticUnit struct {
	ID        string                `json:"id"`
	Statement string                `json:"statement,omitempty"`
	Selector  domain.TextSelector   `json:"selector,omitempty"`
	Context   []domain.TextSelector `json:"context,omitempty"`
	Mentions  []Mention             `json:"mentions,omitempty"`
}

type Mention struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Role     string              `json:"role,omitempty"`
	Selector domain.TextSelector `json:"selector,omitempty"`
}

// GraphEndpoint 可引用原资产或一个已发布的单元/提及，引用始终携带原文入口。
type GraphEndpoint struct {
	AssetID            string              `json:"asset_id"`
	Revision           uint64              `json:"revision,omitempty"`
	Snapshot           string              `json:"snapshot,omitempty"`
	UnitID             string              `json:"unit_id,omitempty"`
	MentionID          string              `json:"mention_id,omitempty"`
	Fingerprint        string              `json:"fingerprint,omitempty"`
	MentionFingerprint string              `json:"mention_fingerprint,omitempty"`
	Selector           domain.TextSelector `json:"selector,omitempty"`
}

type GroundedLink struct {
	ID         string          `json:"id,omitempty"`
	Type       string          `json:"type"`
	Meaning    string          `json:"meaning"`
	Source     GraphEndpoint   `json:"source"`
	Target     GraphEndpoint   `json:"target"`
	Conditions []GraphEndpoint `json:"conditions,omitempty"`
}

func CloneOrganization(value *Organization) *Organization {
	if value == nil {
		return nil
	}
	encoded, _ := json.Marshal(value)
	var result Organization
	_ = json.Unmarshal(encoded, &result)
	return &result
}

// OrganizationInventory 不交付其他工作的关系推断，避免循环依赖派生判断。
func OrganizationInventory(value *Organization) *Organization {
	result := CloneOrganization(value)
	if result != nil {
		result.Links = nil
	}
	return result
}

func organizationDigest(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:16])
}

func UnitFingerprint(unit SemanticUnit) string {
	unit.ID = ""
	unit.Mentions = append([]Mention(nil), unit.Mentions...)
	for n := range unit.Mentions {
		unit.Mentions[n].ID = ""
	}
	return organizationDigest(unit)
}

func MentionFingerprint(mention Mention) string { mention.ID = ""; return organizationDigest(mention) }

func NormalizeOrganization(asset domain.Information, value *Organization, candidates []Candidate) (*Organization, error) {
	if value == nil {
		return nil, nil
	}
	result := CloneOrganization(value)
	if result.Schema != OrganizationSchema || len(result.Units) > 128 || len(result.Links) > 128 {
		return nil, errors.New("关系组织格式或工作量无效")
	}
	units := map[string]bool{}
	var unitErrors []error
	for n := range result.Units {
		unit := &result.Units[n]
		if strings.TrimSpace(unit.ID) == "" || units[unit.ID] || len(unit.Context) > 16 || len(unit.Mentions) > 32 {
			return nil, errors.New("证据单元身份、陈述或范围无效")
		}
		units[unit.ID] = true
		if err := normalizeUnit(asset, unit, len(result.Units) == 1); err != nil {
			unitErrors = append(unitErrors, fmt.Errorf("units[%d] (%q): %w", n, unit.ID, err))
		}
	}
	if len(unitErrors) > 0 {
		return nil, errors.Join(unitErrors...)
	}
	sort.Slice(result.Units, func(i, j int) bool { return result.Units[i].ID < result.Units[j].ID })
	result.Snapshot = "org_" + organizationDigest(struct {
		ID       string
		Revision uint64
		Units    []SemanticUnit
	}{asset.ID, asset.Revision, result.Units})
	available := map[string]Candidate{asset.ID: {ID: asset.ID, Revision: asset.Revision, Content: asset.Content, Organization: result}}
	for _, candidate := range candidates {
		available[candidate.ID] = candidate
	}
	seen := map[string]bool{}
	links := make([]GroundedLink, 0, len(result.Links))
	var linkErrors []error
	for index, link := range result.Links {
		var endpointErrors []error
		if (!IsAllowedRelationType(link.Type) && link.Type != "same_object") || strings.TrimSpace(link.Meaning) == "" || len(link.Conditions) > 16 {
			return nil, errors.New("关系含义或条件无效")
		}
		if link.Source.AssetID != asset.ID && link.Target.AssetID != asset.ID {
			return nil, errors.New("关系没有绑定当前资产")
		}
		for _, item := range []struct {
			side     string
			endpoint *GraphEndpoint
		}{{"source", &link.Source}, {"target", &link.Target}} {
			if err := normalizeEndpoint(item.endpoint, available); err != nil {
				endpointErrors = append(endpointErrors, fmt.Errorf("links[%d].%s: %w", index, item.side, err))
			}
		}
		if len(endpointErrors) > 0 {
			linkErrors = append(linkErrors, endpointErrors...)
			continue
		}
		if link.Type == "same_object" && (link.Source.MentionID == "" || link.Target.MentionID == "") {
			return nil, errors.New("同一对象必须连接对象提及")
		}
		if link.Source == link.Target {
			linkErrors = append(linkErrors, fmt.Errorf("links[%d] (%s): source 与 target 指向同一端点；请指定原文中两个不同的依据位置，否则不构成关联", index, link.Type))
			continue
		}
		for n := range link.Conditions {
			if err := normalizeEndpoint(&link.Conditions[n], available); err != nil {
				return nil, err
			}
		}
		link.ID = ""
		link.ID = "rel_" + organizationDigest(link)
		if !seen[link.ID] {
			links = append(links, link)
			seen[link.ID] = true
		}
	}
	if len(linkErrors) > 0 {
		return nil, errors.Join(linkErrors...)
	}
	result.Links = links
	return result, nil
}

func normalizeUnit(asset domain.Information, unit *SemanticUnit, wholeSource bool) error {
	if unit.Selector.Exact == "" {
		if !wholeSource {
			return errors.New("多个单元需要分别定位原文")
		}
		unit.Selector.Exact = asset.Content
	}
	start, end, err := unit.Selector.Resolve(asset.Content)
	if err != nil {
		return fmt.Errorf("单元 %s: %w", unit.ID, err)
	}
	contexts := make([]domain.TextSelector, 0, len(unit.Context))
	seenContext := map[[2]int]bool{}
	for _, selector := range unit.Context {
		cs, ce, err := selector.Resolve(asset.Content)
		if err != nil {
			return err
		}
		if (cs < start || ce > end) && !seenContext[[2]int{cs, ce}] {
			contexts = append(contexts, selector)
			seenContext[[2]int{cs, ce}] = true
		}
	}
	unit.Context = contexts
	mentions := map[string]bool{}
	for mn := range unit.Mentions {
		mention := &unit.Mentions[mn]
		if mention.Selector.Exact == "" {
			selector, err := implicitMentionSelector(asset.Content, *unit, mention.Name)
			if err != nil {
				return fmt.Errorf("对象 %s: %w", mention.ID, err)
			}
			mention.Selector = selector
		}
		if mention.ID == "" || mention.Name == "" || mentions[mention.ID] {
			return errors.New("对象提及身份无效")
		}
		mentions[mention.ID] = true
		ms, me, err := mention.Selector.Resolve(asset.Content)
		if err != nil && mention.Selector.Prefix == "" && mention.Selector.Suffix == "" {
			// A mention is located within its declared unit. Add mechanical
			// disambiguation context when its exact quote is unique there.
			located, locateErr := implicitMentionSelector(asset.Content, *unit, mention.Selector.Exact)
			if locateErr == nil && located.Exact == mention.Selector.Exact {
				mention.Selector = located
				ms, me, err = mention.Selector.Resolve(asset.Content)
			}
		}
		if err != nil {
			return err
		}
		contained := ms >= start && me <= end
		for _, selector := range unit.Context {
			cs, ce, _ := selector.Resolve(asset.Content)
			contained = contained || (ms >= cs && me <= ce)
		}
		if !contained {
			return fmt.Errorf("单元 %q 的对象提及 %q (%q) 不在该单元或必要上下文内；请定位此单元中的实际提及，或明确它所需的原文上下文", unit.ID, mention.ID, mention.Name)
		}
	}
	return nil
}

// A missing selector denotes the literal name inside this unit, not an entity
// resolution request. Case folding never rewrites the original passage, and
// multiple matches still require the caller to disambiguate explicitly.
func implicitMentionSelector(content string, unit SemanticUnit, name string) (domain.TextSelector, error) {
	text, query := []rune(content), []rune(name)
	if len(query) == 0 {
		return domain.TextSelector{}, errors.New("对象名称为空")
	}
	matches := map[int]bool{}
	for _, scope := range append([]domain.TextSelector{unit.Selector}, unit.Context...) {
		start, end, err := scope.Resolve(content)
		if err != nil {
			return domain.TextSelector{}, err
		}
		for at := start; at+len(query) <= end; at++ {
			if strings.EqualFold(string(text[at:at+len(query)]), name) {
				matches[at] = true
			}
		}
	}
	if len(matches) != 1 {
		return domain.TextSelector{}, errors.New("名称没有唯一原文位置，请提供 exact 与必要邻文")
	}
	for at := range matches {
		for width := 16; ; width *= 2 {
			selector := domain.TextSelector{Exact: string(text[at : at+len(query)]),
				Prefix: string(text[max(0, at-width):at]), Suffix: string(text[at+len(query) : min(len(text), at+len(query)+width)])}
			if start, _, err := selector.Resolve(content); err == nil && start == at {
				return selector, nil
			}
			if width >= len(text) {
				return domain.TextSelector{}, errors.New("对象定位不能唯一恢复")
			}
		}
	}
	panic("unreachable")
}

func normalizeEndpoint(endpoint *GraphEndpoint, candidates map[string]Candidate) error {
	candidate, ok := candidates[endpoint.AssetID]
	if !ok || (endpoint.Revision != 0 && endpoint.Revision != candidate.Revision) {
		return errors.New("关联端点不在当前工作或版本已变化")
	}
	endpoint.Revision = candidate.Revision
	if endpoint.UnitID == "" {
		if endpoint.MentionID != "" || endpoint.Snapshot != "" {
			return errors.New("资产端点不能冒充组织单元")
		}
		endpoint.Fingerprint = ""
		endpoint.MentionFingerprint = ""
		if endpoint.Selector.Exact != "" {
			start, end, err := endpoint.Selector.Resolve(candidate.Content)
			if err != nil {
				return err
			}
			if start == 0 && end == len([]rune(candidate.Content)) {
				// Asset identity and revision already locate the complete source.
				// Keep one source body, not a copy on every incident relation.
				endpoint.Selector = domain.TextSelector{}
			}
		}
		return nil
	}
	organization := candidate.Organization
	if organization == nil || (endpoint.Snapshot != "" && endpoint.Snapshot != organization.Snapshot) {
		return fmt.Errorf("资产 %q 未提供匹配的组织快照，不能引用单元 %q；请引用已提供的单元清单，或用原文 selector 定位并省略 unit_id/mention_id", endpoint.AssetID, endpoint.UnitID)
	}
	endpoint.Snapshot = organization.Snapshot
	for _, unit := range organization.Units {
		if unit.ID != endpoint.UnitID {
			continue
		}
		selector := unit.Selector
		endpoint.MentionFingerprint = ""
		if endpoint.MentionID != "" {
			found := false
			for _, mention := range unit.Mentions {
				if mention.ID == endpoint.MentionID {
					endpoint.MentionFingerprint = MentionFingerprint(mention)
					selector = mention.Selector
					found = true
					break
				}
			}
			if !found {
				ids := make([]string, 0, len(unit.Mentions))
				for _, mention := range unit.Mentions {
					ids = append(ids, mention.ID)
				}
				return fmt.Errorf("资产 %q 单元 %q 未声明对象提及 %q；可用 mention_id: %v。引用单元时省略 mention_id；对象身份关系则必须先声明实际提及", endpoint.AssetID, endpoint.UnitID, endpoint.MentionID, ids)
			}
		}
		if endpoint.Selector.Exact != "" {
			start, end, err := endpoint.Selector.Resolve(candidate.Content)
			expectedStart, expectedEnd, expectedErr := selector.Resolve(candidate.Content)
			if err != nil || expectedErr != nil || start != expectedStart || end != expectedEnd {
				return errors.New("关联定位与单元或提及不一致")
			}
		}
		// The local unit/mention already owns its locator. Do not persist or ask
		// the model to repeat that text on every incident edge.
		endpoint.Selector = domain.TextSelector{}
		endpoint.Fingerprint = UnitFingerprint(unit)
		return nil
	}
	ids := make([]string, 0, len(organization.Units))
	for _, unit := range organization.Units {
		ids = append(ids, unit.ID)
	}
	return fmt.Errorf("资产 %q 未声明单元 %q；可用 unit_id: %v。原文引用应使用 selector 并省略 unit_id/mention_id", endpoint.AssetID, endpoint.UnitID, ids)
}
