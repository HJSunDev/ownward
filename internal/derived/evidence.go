package derived

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/domain"
)

const (
	EvidenceUnitSchema       = "ownward.derived-evidence-unit/v1"
	DefaultEvidenceUnitRunes = 384
	minimumEvidenceUnitRunes = DefaultEvidenceUnitRunes / 2
	evidenceIDPrefix         = "e1-"
)

// EvidenceUnit is an ephemeral, rebuildable source-range identity. It is
// generated only for already-retrieved assets and is never persisted.
type EvidenceUnit struct {
	Schema         string `json:"schema"`
	ID             string `json:"id"`
	SourceID       string `json:"source_id"`
	SourceRevision uint64 `json:"source_revision"`
	StartRune      int    `json:"start_rune"`
	EndRune        int    `json:"end_rune"`
	StartByte      int    `json:"-"`
	EndByte        int    `json:"-"`
	Content        string `json:"-"`
}

type evidenceIdentity struct {
	SourceID       string `json:"s"`
	SourceRevision uint64 `json:"r"`
	StartRune      int    `json:"b"`
	EndRune        int    `json:"e"`
	StartByte      int    `json:"i"`
	EndByte        int    `json:"j"`
	ContentSHA256  string `json:"h"`
}

func (u EvidenceUnit) Reference() domain.EvidenceReference {
	return domain.EvidenceReference{
		Schema: domain.EvidenceSchema, ID: u.ID, SourceID: u.SourceID, SourceRevision: u.SourceRevision,
		StartRune: u.StartRune, EndRune: u.EndRune, ContentRunes: u.EndRune - u.StartRune,
	}
}

// EvidenceRanges deterministically partitions a long asset at natural text
// boundaries without hashing every range. Call MaterializeEvidenceUnit only
// for query-selected ranges; neither representation is persisted.
func EvidenceRanges(value domain.Information) []EvidenceUnit {
	runeCount := utf8.RuneCountInString(value.Content)
	if runeCount <= DefaultEvidenceUnitRunes {
		return nil
	}
	units := make([]EvidenceUnit, 0, runeCount/DefaultEvidenceUnitRunes+1)
	for startRune, startByte := 0, 0; startByte < len(value.Content); {
		endRune, endByte := nextEvidenceBoundary(value.Content, startRune, startByte)
		unit := EvidenceUnit{
			Schema: EvidenceUnitSchema, SourceID: value.ID, SourceRevision: value.Revision,
			StartRune: startRune, EndRune: endRune, StartByte: startByte, EndByte: endByte,
			Content: value.Content[startByte:endByte],
		}
		units = append(units, unit)
		startRune, startByte = endRune, endByte
	}
	return units
}

func BuildEvidenceUnits(value domain.Information) []EvidenceUnit {
	units := EvidenceRanges(value)
	for index := range units {
		materialized, err := MaterializeEvidenceUnit(value, units[index])
		if err != nil {
			return nil
		}
		units[index] = materialized
	}
	return units
}

func EvidenceUnitText(value domain.Information, unit EvidenceUnit) (string, error) {
	if unit.Schema != EvidenceUnitSchema || unit.SourceID != value.ID || unit.SourceRevision != value.Revision {
		return "", errors.New("证据单元与当前来源资产不一致")
	}
	if unit.StartRune < 0 || unit.EndRune <= unit.StartRune || unit.StartByte < 0 || unit.EndByte <= unit.StartByte || unit.EndByte > len(value.Content) {
		return "", errors.New("证据单元来源区间无效")
	}
	content := value.Content[unit.StartByte:unit.EndByte]
	if !utf8.ValidString(content) || utf8.RuneCountInString(content) != unit.EndRune-unit.StartRune {
		return "", errors.New("证据单元来源区间无效")
	}
	if unit.Content != "" {
		if unit.Content != content {
			return "", errors.New("证据单元来源内容无效")
		}
	} else if utf8.RuneCountInString(value.Content[:unit.StartByte]) != unit.StartRune {
		return "", errors.New("证据单元来源区间无效")
	}
	return content, nil
}

func MaterializeEvidenceUnit(value domain.Information, unit EvidenceUnit) (EvidenceUnit, error) {
	content, err := EvidenceUnitText(value, unit)
	if err != nil {
		return EvidenceUnit{}, err
	}
	unit.ID = evidenceUnitID(unit, []rune(content))
	return unit, nil
}

func nextEvidenceBoundary(content string, startRune, startByte int) (int, int) {
	minimum := startRune + minimumEvidenceUnitRunes
	maximum := startRune + DefaultEvidenceUnitRunes
	lastNaturalRune, lastNaturalByte := 0, 0
	currentRune := startRune
	for relativeByte, current := range content[startByte:] {
		currentRune++
		nextByte := startByte + relativeByte + utf8.RuneLen(current)
		if currentRune > minimum && naturalBoundary(current) {
			lastNaturalRune, lastNaturalByte = currentRune, nextByte
		}
		if currentRune == maximum {
			if lastNaturalRune > 0 {
				return lastNaturalRune, lastNaturalByte
			}
			return currentRune, nextByte
		}
	}
	return currentRune, len(content)
}

func naturalBoundary(value rune) bool {
	switch value {
	case '\n', '\r', '。', '！', '？', '；', '.', '!', '?', ';':
		return true
	default:
		return unicode.IsSpace(value)
	}
}

func evidenceUnitID(unit EvidenceUnit, content []rune) string {
	digest := sha256.Sum256([]byte(string(content)))
	payload, _ := json.Marshal(evidenceIdentity{
		SourceID: unit.SourceID, SourceRevision: unit.SourceRevision,
		StartRune: unit.StartRune, EndRune: unit.EndRune, StartByte: unit.StartByte, EndByte: unit.EndByte,
		ContentSHA256: hex.EncodeToString(digest[:]),
	})
	return evidenceIDPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

// ParseEvidenceUnitID restores a source range without a persisted lookup
// table. ResolveEvidence still validates it against the current source bytes.
func ParseEvidenceUnitID(id string) (EvidenceUnit, error) {
	if !strings.HasPrefix(id, evidenceIDPrefix) || len(id) > 4096 {
		return EvidenceUnit{}, errors.New("证据单元身份无效")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, evidenceIDPrefix))
	if err != nil {
		return EvidenceUnit{}, errors.New("证据单元身份无效")
	}
	var identity evidenceIdentity
	if err := json.Unmarshal(payload, &identity); err != nil || strings.TrimSpace(identity.SourceID) == "" || identity.SourceRevision == 0 ||
		identity.StartRune < 0 || identity.EndRune <= identity.StartRune || identity.StartByte < 0 || identity.EndByte <= identity.StartByte || len(identity.ContentSHA256) != sha256.Size*2 {
		return EvidenceUnit{}, errors.New("证据单元身份无效")
	}
	canonical, _ := json.Marshal(identity)
	if evidenceIDPrefix+base64.RawURLEncoding.EncodeToString(canonical) != id {
		return EvidenceUnit{}, errors.New("证据单元身份非规范")
	}
	return EvidenceUnit{
		Schema: EvidenceUnitSchema, ID: id, SourceID: identity.SourceID, SourceRevision: identity.SourceRevision,
		StartRune: identity.StartRune, EndRune: identity.EndRune, StartByte: identity.StartByte, EndByte: identity.EndByte,
	}, nil
}

func ResolveEvidence(value domain.Information, unit EvidenceUnit) (domain.Evidence, error) {
	content, err := EvidenceUnitText(value, unit)
	if err != nil {
		return domain.Evidence{}, err
	}
	if evidenceUnitID(unit, []rune(content)) != unit.ID {
		return domain.Evidence{}, errors.New("证据单元身份与来源内容不一致")
	}
	evidence := domain.Evidence{
		Schema: domain.EvidenceSchema, ID: unit.ID, SourceID: value.ID, SourceRevision: value.Revision,
		StartRune: unit.StartRune, EndRune: unit.EndRune, Content: content,
	}
	return evidence, evidence.Validate()
}
