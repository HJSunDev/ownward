package boundedstore

import (
	"context"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/retrieval"
)

func TestLargeQueryScoringPreservesPassages(t *testing.T) {
	s := retrievalStore(t)
	query := strings.Repeat("unmatched ", 12000) + "项目预算 alpha 李明"
	passages := []string{"李明负责项目。预算三万。", "alpha beta 项目", "不同的内容"}
	e := s.WithSnapshot(context.Background(), func(ctx context.Context) error {
		walk := func(f func(string) error) error {
			for _, p := range passages {
				if e := f(p); e != nil {
					return e
				}
			}
			return nil
		}
		scorer, e := s.SourceScorer(ctx, StringSource(query), walk)
		if e != nil {
			return e
		}
		old := retrieval.NewPassageTextScorer(query, passages)
		for _, p := range passages {
			got := scorer.Score(p)
			if got != old.Score(p) {
				t.Fatalf("score %v vs %v", got, old.Score(p))
			}
		}
		return scorer.Err()
	})
	if e != nil {
		t.Fatal(e)
	}
}
