package derived

import (
	"time"

	"github.com/HJSunDev/ownward/internal/semantics"
)

type Record struct {
	InputsKnown           bool                           `json:"inputs_known,omitempty"`
	InputAssets           []semantics.CandidateReference `json:"input_assets,omitempty"`
	Schema                string                         `json:"schema"`
	AssetID               string                         `json:"asset_id"`
	AssetRevision         uint64                         `json:"asset_revision"`
	GeneratedAt           time.Time                      `json:"generated_at"`
	Provider              string                         `json:"provider"`
	Status                string                         `json:"status"`
	Error                 string                         `json:"error,omitempty"`
	Analysis              semantics.Analysis             `json:"analysis"`
	SemanticWorkReference *semantics.WorkReference       `json:"semantic_work_reference,omitempty"`
	SemanticReceipt       *semantics.SubmissionReceipt   `json:"semantic_receipt,omitempty"`
	EmbeddingSpace        string                         `json:"embedding_space,omitempty"`
	Embedding             []float32                      `json:"embedding,omitempty"`
}

func (r Record) HasPendingSemanticWork() bool {
	return r.SemanticWorkReference != nil && r.SemanticReceipt == nil
}

func (r Record) HasSemanticResult() bool {
	return r.SemanticReceipt != nil
}

type Edge struct {
	Grounded   *semantics.GroundedLink `json:"grounded,omitempty"`
	OwnerID    string                  `json:"-"`
	SourceID   string                  `json:"source_id"`
	TargetID   string                  `json:"target_id"`
	Type       string                  `json:"type"`
	Confidence float64                 `json:"confidence"`
	Evidence   string                  `json:"evidence,omitempty"`
	Depth      int                     `json:"depth"`
}

// Inputs 包含参与判断的原文和此前分析的来源，关系边不能替代实际输入依赖。
func Inputs(record Record) []semantics.CandidateReference {
	values := append([]semantics.CandidateReference(nil), record.InputAssets...)
	if record.SemanticWorkReference != nil {
		values = append(values, record.SemanticWorkReference.Candidates...)
	}
	for _, relation := range record.Analysis.Relations {
		values = append(values, semantics.CandidateReference{ID: relation.TargetID, Revision: relation.TargetRevision})
		if relation.InferredBy != "" {
			values = append(values, semantics.CandidateReference{ID: relation.InferredBy, Revision: relation.TargetRevision})
		}
	}
	seen := map[semantics.CandidateReference]bool{}
	result := make([]semantics.CandidateReference, 0, len(values))
	for _, v := range values {
		v.Similarity = 0
		if v.ID != "" && v.ID != record.AssetID && !seen[v] {
			result = append(result, v)
			seen[v] = true
		}
	}
	return result
}
