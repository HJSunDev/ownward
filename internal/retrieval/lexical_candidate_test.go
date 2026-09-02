//go:build ownward_candidate

package retrieval

import (
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/kernelv2candidate/coverage"
)

func TestCandidateSourceMetadataIsFixedAndNeverReopensAuthority(t *testing.T) {
	values := []domain.Information{
		{ID: "short", Revision: 1, Content: "current cobalt relay binding result"},
		{ID: "long", Revision: 1, Content: strings.Repeat("neutral archive material. ", 40) + " current cobalt relay binding result."},
	}
	index := NewLexical(values)
	got := index.SourceMetadata([]string{"short", "long"})
	if got["short"].PassageScore != 0 || got["long"].PassageScore != 1 {
		t.Fatalf("deep-source metadata drifted: %+v", got)
	}
	frozen := got["long"]
	index.documents[index.docs["long"]].information.Content = ""
	after := index.SourceMetadata([]string{"long"})["long"]
	if after != frozen {
		t.Fatalf("request-time metadata reopened authority text: got=%+v want=%+v", after, frozen)
	}
}

func TestCandidateSourceMetadataUpsertReplacesSketch(t *testing.T) {
	index := NewLexical([]domain.Information{{ID: "one", Revision: 1, Content: strings.Repeat("old cobalt archive. ", 40)}})
	before := index.SourceMetadata([]string{"one"})["one"]
	index.Upsert(domain.Information{ID: "one", Revision: 2, Content: strings.Repeat("new amber ledger. ", 40)})
	after := index.SourceMetadata([]string{"one"})["one"]
	if before.Diversity == after.Diversity || after.Diversity == (coverage.Sketch{}) {
		t.Fatalf("upsert did not replace fixed source sketch: before=%+v after=%+v", before, after)
	}
}
