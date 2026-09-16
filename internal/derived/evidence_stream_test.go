package derived

import (
	"github.com/HJSunDev/ownward/internal/domain"
	"reflect"
	"strings"
	"testing"
)

func TestStreamingEvidenceBoundariesMatch(t *testing.T) {
	for _, text := range []string{strings.Repeat("a", 384), strings.Repeat("字", 385), strings.Repeat("中文🙂 some text。\r\n", 300), strings.Repeat("a", 800) + "\n\n" + strings.Repeat("tail ", 300)} {
		a := domain.Information{ID: "a", Revision: 1, Content: text}
		var got []EvidenceUnit
		if e := WalkEvidenceRanges(a.ID, a.Revision, strings.NewReader(text), func(u EvidenceUnit) error { got = append(got, u); return nil }); e != nil {
			t.Fatal(e)
		}
		want := EvidenceRanges(a)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("boundaries mismatch: got %v want %v", got, want)
		}
	}
}
