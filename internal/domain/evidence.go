package domain

import (
	"errors"
	"strings"
)

const EvidenceSchema = "ownward.evidence/v1"

// EvidenceReference identifies a rebuildable slice of one authoritative asset.
// The source asset remains the information authority; this value only narrows
// the amount of source text that must be delivered for a retrieval purpose.
type EvidenceReference struct {
	Schema         string `json:"schema"`
	ID             string `json:"id"`
	SourceID       string `json:"source_id"`
	SourceRevision uint64 `json:"source_revision"`
	StartRune      int    `json:"start_rune"`
	EndRune        int    `json:"end_rune"`
	ContentRunes   int    `json:"content_runes"`
}

func (r EvidenceReference) Validate() error {
	if r.Schema != EvidenceSchema || strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.SourceID) == "" {
		return errors.New("证据引用缺少有效格式或身份")
	}
	if r.SourceRevision == 0 || r.StartRune < 0 || r.EndRune <= r.StartRune || r.ContentRunes != r.EndRune-r.StartRune {
		return errors.New("证据引用的来源版本或区间无效")
	}
	return nil
}

// Evidence is source text resolved from an authoritative asset at read time.
// It is not an independently mutable information asset.
type Evidence struct {
	Schema         string `json:"schema"`
	ID             string `json:"id"`
	SourceID       string `json:"source_id"`
	SourceRevision uint64 `json:"source_revision"`
	StartRune      int    `json:"start_rune"`
	EndRune        int    `json:"end_rune"`
	Content        string `json:"content"`
}

func (e Evidence) Reference() EvidenceReference {
	return EvidenceReference{
		Schema: EvidenceSchema, ID: e.ID, SourceID: e.SourceID, SourceRevision: e.SourceRevision,
		StartRune: e.StartRune, EndRune: e.EndRune, ContentRunes: e.EndRune - e.StartRune,
	}
}

func (e Evidence) Validate() error {
	if err := e.Reference().Validate(); err != nil {
		return err
	}
	if len([]rune(e.Content)) != e.EndRune-e.StartRune {
		return errors.New("证据内容与来源区间不一致")
	}
	return nil
}
