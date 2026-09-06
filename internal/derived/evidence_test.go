package derived

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

func TestCompactEvidenceKeepsFullIntegrityAndLegacyReads(t *testing.T) {
	asset := domain.Information{ID: "01a07212-3b1a-71b2-91d5-d403c03cd965", Revision: 9,
		Content: strings.Repeat("多语言 evidence source. ", 60)}
	unit := BuildEvidenceUnits(asset)[0]
	if !strings.HasPrefix(unit.ID, "e2-") || len(unit.ID) > 120 {
		t.Fatalf("reference remains too long: %d", len(unit.ID))
	}
	hash := sha256.Sum256([]byte(unit.Content))
	payload, _ := json.Marshal(evidenceIdentity{SourceID: unit.SourceID, SourceRevision: unit.SourceRevision,
		StartRune: unit.StartRune, EndRune: unit.EndRune, StartByte: unit.StartByte, EndByte: unit.EndByte, ContentSHA256: hex.EncodeToString(hash[:])})
	legacy := "e1-" + base64.RawURLEncoding.EncodeToString(payload)
	for _, id := range []string{unit.ID, legacy} {
		parsed, err := ParseEvidenceUnitID(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveEvidence(asset, parsed); err != nil {
			t.Fatal(err)
		}
		changed := asset
		changed.Content = strings.Replace(asset.Content, "evidence", "altered!", 1)
		if _, err := ResolveEvidence(changed, parsed); err == nil {
			t.Fatal("content tampering was accepted")
		}
		changed = asset
		changed.Revision++
		if _, err := ResolveEvidence(changed, parsed); err == nil {
			t.Fatal("stale source revision was accepted")
		}
	}
}

func TestEvidenceUnitsPartitionAndRebuildWithoutCopyingSource(t *testing.T) {
	content := "first fact. " + strings.Repeat("long source paragraph; ", 80)
	asset := domain.Information{Schema: domain.AssetSchema, ID: "asset-1", Revision: 3, CreatedAt: time.Now(), UpdatedAt: time.Now(), Kind: domain.KindGeneral, Content: content}
	units := BuildEvidenceUnits(asset)
	if len(units) < 2 {
		t.Fatalf("expected multiple evidence units, got %d", len(units))
	}
	joined := strings.Builder{}
	previousEnd := 0
	for _, unit := range units {
		if unit.StartRune != previousEnd || unit.SourceID != asset.ID || unit.SourceRevision != asset.Revision {
			t.Fatalf("non-contiguous or unbound evidence unit: %#v", unit)
		}
		evidence, err := ResolveEvidence(asset, unit)
		if err != nil {
			t.Fatal(err)
		}
		joined.WriteString(evidence.Content)
		parsed, err := ParseEvidenceUnitID(unit.ID)
		if err != nil || parsed.SourceID != unit.SourceID || parsed.SourceRevision != unit.SourceRevision ||
			parsed.StartRune != unit.StartRune || parsed.EndRune != unit.EndRune ||
			parsed.StartByte != unit.StartByte || parsed.EndByte != unit.EndByte {
			t.Fatalf("self-contained evidence identity did not round-trip: parsed=%#v err=%v", parsed, err)
		}
		previousEnd = unit.EndRune
	}
	if joined.String() != content {
		t.Fatal("evidence partition did not reconstruct the authoritative content exactly")
	}
	changed := asset
	changed.Revision++
	if _, err := ResolveEvidence(changed, units[0]); err == nil {
		t.Fatal("evidence unit must be invalidated by source revision")
	}
	tampered := units[0]
	tampered.ID += "x"
	if _, err := ResolveEvidence(asset, tampered); err == nil {
		t.Fatal("tampered evidence identity was accepted")
	}
}
