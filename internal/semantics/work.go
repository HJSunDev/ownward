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
	WorkSchema              = "ownward.semantic-work/v1"
	SubmissionSchema        = "ownward.semantic-submission/v1"
	WorkReferenceSchema     = "ownward.semantic-work-reference/v1"
	SubmissionReceiptSchema = "ownward.semantic-submission-receipt/v1"

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

// CandidateReference is the durable identity of candidate context. Candidate
// content remains authoritative in the asset store and is resolved only when
// a pending work item is exposed again.
type CandidateReference struct {
	ID         string  `json:"id"`
	Revision   uint64  `json:"revision"`
	Similarity float64 `json:"semantic_similarity,omitempty"`
}

// WorkReference is the compact, rebuildable durable form of Work. It keeps the
// exact work and candidate identities without duplicating authoritative asset
// content in derived state.
type WorkReference struct {
	Schema     string               `json:"schema"`
	ID         string               `json:"id"`
	Generation string               `json:"generation"`
	AssetID    string               `json:"asset_id"`
	Revision   uint64               `json:"asset_revision"`
	Candidates []CandidateReference `json:"candidates,omitempty"`
	Previous   *Analysis            `json:"previous_analysis,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
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
	Status      string     `json:"status" jsonschema:"只能填写 complete 或 uncertain。只要能够可靠概括资产本身就填写 complete；没有可靠的关系、场景或主题时在相应字段使用空数组，不能因此填写 uncertain。只有连资产基本含义都无法可靠理解时才填写 uncertain"`
	Uncertainty string     `json:"uncertainty,omitempty" jsonschema:"仅在 status 为 uncertain 时说明为什么无法可靠理解资产基本含义"`
	Analysis    Analysis   `json:"analysis" jsonschema:"只依据当前语义工作中的资产和候选上下文形成的候选判断"`
	AcceptedAt  time.Time  `json:"accepted_at,omitempty"`
}

// SubmissionReceipt proves which normalized result was accepted without
// retaining a second copy of that result's analysis. The authoritative current
// analysis remains in the derived record.
type SubmissionReceipt struct {
	Schema      string     `json:"schema"`
	WorkID      string     `json:"work_id"`
	AssetID     string     `json:"asset_id"`
	Revision    uint64     `json:"asset_revision"`
	Capability  Capability `json:"capability"`
	Status      string     `json:"status"`
	Uncertainty string     `json:"uncertainty,omitempty"`
	AcceptedAt  time.Time  `json:"accepted_at"`
	SHA256      string     `json:"submission_sha256"`
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

func ReferenceWork(work Work) (WorkReference, error) {
	if err := work.Validate(); err != nil {
		return WorkReference{}, err
	}
	reference := WorkReference{
		Schema: WorkReferenceSchema, ID: work.ID, Generation: work.Generation,
		AssetID: work.Asset.ID, Revision: work.Asset.Revision, CreatedAt: work.CreatedAt,
		Candidates: make([]CandidateReference, 0, len(work.Candidates)),
	}
	if work.Previous != nil {
		previous := *work.Previous
		reference.Previous = &previous
	}
	for _, candidate := range work.Candidates {
		reference.Candidates = append(reference.Candidates, CandidateReference{
			ID: candidate.ID, Revision: candidate.Revision, Similarity: candidate.Similarity,
		})
	}
	return reference, reference.Validate()
}

func (w WorkReference) Validate() error {
	if w.Schema != WorkReferenceSchema || strings.TrimSpace(w.ID) == "" || strings.TrimSpace(w.Generation) == "" ||
		strings.TrimSpace(w.AssetID) == "" || w.Revision == 0 || w.CreatedAt.IsZero() {
		return errors.New("语义工作引用元数据无效")
	}
	if len(w.Candidates) > 32 {
		return errors.New("语义工作引用候选超过限制")
	}
	seen := make(map[string]struct{}, len(w.Candidates))
	for _, candidate := range w.Candidates {
		if strings.TrimSpace(candidate.ID) == "" || candidate.Revision == 0 || candidate.ID == w.AssetID ||
			math.IsNaN(candidate.Similarity) || math.IsInf(candidate.Similarity, 0) {
			return errors.New("语义工作引用候选无效")
		}
		if _, exists := seen[candidate.ID]; exists {
			return errors.New("语义工作引用包含重复候选")
		}
		seen[candidate.ID] = struct{}{}
	}
	return nil
}

// ResolveWork restores the public work payload from authoritative assets. The
// caller must provide candidates in reference order and at the exact revision.
func ResolveWork(reference WorkReference, asset domain.Information, candidates []domain.Information) (Work, error) {
	if err := reference.Validate(); err != nil {
		return Work{}, err
	}
	if asset.ID != reference.AssetID || asset.Revision != reference.Revision || len(candidates) != len(reference.Candidates) {
		return Work{}, errors.New("语义工作引用与权威资产不一致")
	}
	work := Work{
		Schema: WorkSchema, ID: reference.ID, Generation: reference.Generation,
		Asset: asset, CreatedAt: reference.CreatedAt,
		Candidates: make([]Candidate, 0, len(candidates)),
	}
	if reference.Previous != nil {
		previous := *reference.Previous
		work.Previous = &previous
	}
	for index, candidate := range candidates {
		expected := reference.Candidates[index]
		if candidate.ID != expected.ID || candidate.Revision != expected.Revision {
			return Work{}, errors.New("语义工作候选已被新版本取代")
		}
		work.Candidates = append(work.Candidates, Candidate{
			ID: candidate.ID, Revision: candidate.Revision, Content: candidate.Content,
			Contexts: append([]domain.Context(nil), candidate.Contexts...), Similarity: expected.Similarity,
		})
	}
	return work, work.Validate()
}

func NormalizeSubmission(work Work, value Submission, acceptedAt time.Time) (Submission, error) {
	if err := work.Validate(); err != nil {
		return Submission{}, err
	}
	references := make([]CandidateReference, 0, len(work.Candidates))
	for _, candidate := range work.Candidates {
		references = append(references, CandidateReference{ID: candidate.ID, Revision: candidate.Revision, Similarity: candidate.Similarity})
	}
	return normalizeSubmission(work.ID, work.Asset, references, value, acceptedAt)
}

func NormalizeSubmissionReference(reference WorkReference, asset domain.Information, value Submission, acceptedAt time.Time) (Submission, error) {
	if err := reference.Validate(); err != nil {
		return Submission{}, err
	}
	if asset.ID != reference.AssetID || asset.Revision != reference.Revision {
		return Submission{}, errors.New("语义工作引用与权威资产不一致")
	}
	return normalizeSubmission(reference.ID, asset, reference.Candidates, value, acceptedAt)
}

func normalizeSubmission(workID string, asset domain.Information, candidatesList []CandidateReference, value Submission, acceptedAt time.Time) (Submission, error) {
	if value.Schema != SubmissionSchema || value.WorkID != workID || value.AssetID != asset.ID || value.Revision != asset.Revision {
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
	value.Analysis = normalizeAnalysis(asset, value.Analysis)
	candidates := make(map[string]CandidateReference, len(candidatesList))
	for _, candidate := range candidatesList {
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
		relation.InferredBy = asset.ID
	}
	value.AcceptedAt = acceptedAt.UTC()
	return value, nil
}

func SameSubmission(left, right Submission) bool {
	leftDigest, leftErr := submissionDigest(left)
	rightDigest, rightErr := submissionDigest(right)
	return leftErr == nil && rightErr == nil && leftDigest == rightDigest
}

func NewSubmissionReceipt(value Submission) (SubmissionReceipt, error) {
	if value.Schema != SubmissionSchema || strings.TrimSpace(value.WorkID) == "" || strings.TrimSpace(value.AssetID) == "" ||
		value.Revision == 0 || value.AcceptedAt.IsZero() {
		return SubmissionReceipt{}, errors.New("语义结果不能形成有效回执")
	}
	digest, err := submissionDigest(value)
	if err != nil {
		return SubmissionReceipt{}, err
	}
	receipt := SubmissionReceipt{
		Schema: SubmissionReceiptSchema, WorkID: value.WorkID, AssetID: value.AssetID,
		Revision: value.Revision, Capability: value.Capability, Status: value.Status,
		Uncertainty: value.Uncertainty, AcceptedAt: value.AcceptedAt.UTC(), SHA256: digest,
	}
	return receipt, receipt.Validate()
}

func (r SubmissionReceipt) Validate() error {
	decoded, err := hex.DecodeString(strings.TrimSpace(r.SHA256))
	if r.Schema != SubmissionReceiptSchema || strings.TrimSpace(r.WorkID) == "" || strings.TrimSpace(r.AssetID) == "" ||
		r.Revision == 0 || r.AcceptedAt.IsZero() || strings.TrimSpace(r.Capability.ID) == "" || strings.TrimSpace(r.Capability.Version) == "" ||
		(r.Status != SubmissionComplete && r.Status != SubmissionUncertain) || err != nil || len(decoded) != sha256.Size {
		return errors.New("语义提交回执无效")
	}
	if r.Status == SubmissionUncertain && strings.TrimSpace(r.Uncertainty) == "" {
		return errors.New("不确定语义提交回执缺少原因")
	}
	return nil
}

func (r SubmissionReceipt) Matches(value Submission) bool {
	if err := r.Validate(); err != nil {
		return false
	}
	digest, err := submissionDigest(value)
	return err == nil && digest == r.SHA256
}

func submissionDigest(value Submission) (string, error) {
	value.AcceptedAt = time.Time{}
	value.Capability.Execution = ""
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
