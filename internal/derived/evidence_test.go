package derived

import (
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

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
