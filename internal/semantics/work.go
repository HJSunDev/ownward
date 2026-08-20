package semantics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

const (
	WorkSchema       = "ownward.semantic-work/v1"
	SubmissionSchema = "ownward.semantic-submission/v1"

	SubmissionComplete  = "complete"
	SubmissionUncertain = "uncertain"
)

// Work 是内核交给外部语义能力的有界工作单元。它只包含当前资产和候选上下文，
// 不包含用户正在执行的任务、检索问题或验收答案。
type Work struct {
	Schema     string             `json:"schema"`
	ID         string             `json:"id"`
	Generation string             `json:"generation"`
	Asset      domain.Information `json:"asset"`
	Candidates []Candidate        `json:"candidates,omitempty"`
	Previous   *Analysis          `json:"previous_analysis,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
}

// Capability 标识产生候选判断的外部语义能力。字段描述能力来源，不规定它必须是
// 模型、智能体、服务还是未来其他形态。
type Capability struct {
	ID        string `json:"id" jsonschema:"产生判断的外部语义能力标识，例如 codex"`
	Version   string `json:"version" jsonschema:"产生判断的能力或模型版本"`
	Execution string `json:"execution,omitempty" jsonschema:"可选的执行路径或运行条件"`
}

// Submission 是外部语义能力返回的候选判断。它不能直接成为关系图或长期资产；
// 只有通过内核校验后，才会进入当前派生组织状态。
type Submission struct {
	Schema      string     `json:"schema" jsonschema:"固定填写 ownward.semantic-submission/v1"`
	WorkID      string     `json:"work_id" jsonschema:"原样复制语义工作的 id"`
	AssetID     string     `json:"asset_id" jsonschema:"原样复制语义工作的 asset.id"`
	Revision    uint64     `json:"asset_revision" jsonschema:"原样复制语义工作的 asset.revision"`
	Capability  Capability `json:"capability" jsonschema:"产生本次判断的外部语义能力来源"`
	Status      string     `json:"status" jsonschema:"只能填写 complete 或 uncertain；能够可靠判断时填写 complete，无法可靠判断时填写 uncertain"`
	Uncertainty string     `json:"uncertainty,omitempty" jsonschema:"status 为 uncertain 时必须说明无法可靠判断的原因"`
	Analysis    Analysis   `json:"analysis" jsonschema:"只依据当前语义工作中的资产和候选上下文形成的候选判断"`
	AcceptedAt  time.Time  `json:"accepted_at,omitempty"`
}

func NewWork(generation string, asset domain.Information, candidates []Candidate, previous *Analysis, now time.Time) (Work, error) {
	work := Work{
		Schema:     WorkSchema,
		Generation: strings.TrimSpace(generation),
		Asset:      asset,
		Candidates: append([]Candidate(nil), candidates...),
		CreatedAt:  now.UTC(),
	}
	if previous != nil {
		value := *previous
		work.Previous = &value
	}
	binding := struct {
		Schema     string      `json:"schema"`
		Generation string      `json:"generation"`
		AssetID    string      `json:"asset_id"`
		Revision   uint64      `json:"revision"`
		Candidates []Candidate `json:"candidates"`
	}{WorkSchema, work.Generation, asset.ID, asset.Revision, work.Candidates}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return Work{}, err
	}
	digest := sha256.Sum256(encoded)
	work.ID = "sw_" + hex.EncodeToString(digest[:16])
	if err := work.Validate(); err != nil {
		return Work{}, err
	}
	return work, nil
}

func (w Work) Validate() error {
	if w.Schema != WorkSchema || strings.TrimSpace(w.ID) == "" || strings.TrimSpace(w.Generation) == "" || w.CreatedAt.IsZero() {
		return errors.New("语义工作元数据无效")
	}
	if err := w.Asset.Validate(); err != nil {
		return fmt.Errorf("语义工作资产无效: %w", err)
	}
	if len(w.Candidates) > 32 {
		return errors.New("语义工作候选超过限制")
	}
	seen := make(map[string]struct{}, len(w.Candidates))
	for _, candidate := range w.Candidates {
		if strings.TrimSpace(candidate.ID) == "" || candidate.Revision == 0 || candidate.ID == w.Asset.ID || strings.TrimSpace(candidate.Content) == "" {
			return errors.New("语义工作候选无效")
		}
		if _, exists := seen[candidate.ID]; exists {
			return errors.New("语义工作包含重复候选")
		}
		seen[candidate.ID] = struct{}{}
	}
	return nil
}

func NormalizeSubmission(work Work, value Submission, acceptedAt time.Time) (Submission, error) {
	if err := work.Validate(); err != nil {
		return Submission{}, err
	}
	if value.Schema != SubmissionSchema || value.WorkID != work.ID || value.AssetID != work.Asset.ID || value.Revision != work.Asset.Revision {
		return Submission{}, errors.New("语义结果与当前工作不一致")
	}
	value.Capability.ID = strings.TrimSpace(value.Capability.ID)
	value.Capability.Version = strings.TrimSpace(value.Capability.Version)
	value.Capability.Execution = strings.TrimSpace(value.Capability.Execution)
	if value.Capability.ID == "" || value.Capability.Version == "" {
		return Submission{}, errors.New("语义结果缺少能力来源")
	}
	value.Status = strings.TrimSpace(value.Status)
	value.Uncertainty = truncate(strings.TrimSpace(value.Uncertainty), 512)
	if value.Status != SubmissionComplete && value.Status != SubmissionUncertain {
		return Submission{}, errors.New("语义结果状态无效：必须是 complete 或 uncertain")
	}
	if value.Status == SubmissionUncertain && value.Uncertainty == "" {
		return Submission{}, errors.New("不确定的语义结果必须说明原因")
	}
	value.Analysis = normalizeAnalysis(work.Asset, value.Analysis)
	candidates := make(map[string]Candidate, len(work.Candidates))
	for _, candidate := range work.Candidates {
		candidates[candidate.ID] = candidate
	}
	for index := range value.Analysis.Relations {
		relation := &value.Analysis.Relations[index]
		candidate, exists := candidates[relation.TargetID]
		if !exists || !validRelationType(relation.Type) || !validRelationDirection(relation.Direction) ||
			relation.Confidence < 0.75 || relation.Confidence > 1 || math.IsNaN(relation.Confidence) || math.IsInf(relation.Confidence, 0) || strings.TrimSpace(relation.Evidence) == "" {
			return Submission{}, errors.New("语义关系未被当前工作和证据支持")
		}
		relation.TargetRevision = candidate.Revision
		relation.InferredBy = work.Asset.ID
	}
	value.AcceptedAt = acceptedAt.UTC()
	return value, nil
}

func SameSubmission(left, right Submission) bool {
	left.AcceptedAt = time.Time{}
	right.AcceptedAt = time.Time{}
	left.Capability.Execution = ""
	right.Capability.Execution = ""
	leftEncoded, leftErr := json.Marshal(left)
	rightEncoded, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftEncoded) == string(rightEncoded)
}
