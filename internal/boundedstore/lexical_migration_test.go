package boundedstore

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func TestLexicalBuildDoesNotHoldItsOwnWALReader(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
	s, e := Open(ctx, filepath.Join(t.TempDir(), "index.sqlite"), Options{Budget: b, paused: true})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var content strings.Builder
	for i := 0; i < 8000; i++ {
		fmt.Fprintf(&content, "term%06d ", i)
	}
	p, e := s.Stage(ctx, "fixture", StringSource(content.String()), StringSource(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.prepareLexical(ctx, p, "asset"); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = s.writer.QueryRowContext(ctx, "SELECT count(*) FROM postings WHERE payload=(SELECT id FROM lexical_payload_ids WHERE payload=?)", p.ID).Scan(&count); e != nil || count < 8000 {
		t.Fatal("lost lexical terms", count, e)
	}
}
