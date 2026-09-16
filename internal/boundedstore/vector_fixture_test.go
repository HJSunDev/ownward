package boundedstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/derived"
)

// The optional fixture is the frozen preflight material, not generated AI data.
// Setup imports identical raw vectors; measurements exercise the production pack/search path.
func TestFrozenVectorFixture(t *testing.T) {
	dir := os.Getenv("OWNWARD_VECTOR_FIXTURE")
	if dir == "" {
		t.Skip("frozen local vectors not configured")
	}
	s := retrievalStore(t)
	ctx := context.Background()
	check := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	check(s.CreateGeneration(ctx, "g", "space"))
	f, e := os.Open(filepath.Join(dir, "vectors.bin"))
	check(e)
	defer f.Close()
	read := func(n int) []byte { b := make([]byte, n); _, e := io.ReadFull(f, b); check(e); return b }
	count := int(binary.LittleEndian.Uint32(read(4)))
	records := make([]derived.Record, count)
	check(s.write(ctx, func(tx *sql.Tx) error {
		for i := range records {
			id := string(read(int(binary.LittleEndian.Uint32(read(4)))))
			data := read(2048)
			vector := make([]float32, 512)
			for j := range vector {
				vector[j] = math.Float32frombits(binary.LittleEndian.Uint32(data[4*j:]))
			}
			records[i] = derived.Record{AssetID: id, AssetRevision: 1, Status: "ready", InputsKnown: true, Embedding: vector, EmbeddingSpace: "space"}
			org := fmt.Sprintf("o%06d", i)
			digest := sha256.Sum256(data)
			for _, v := range []struct {
				q    string
				args []any
			}{
				{"INSERT INTO payloads(id,operation,state) VALUES(?,'fixture','published')", []any{id}},
				{"INSERT INTO assets VALUES(?,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','information',?,0)", []any{id, id}},
				{"INSERT INTO organizations VALUES(?,'g',?,1,'','published','ready','','')", []any{org, id}},
				{"INSERT INTO organization_current VALUES('g',?,?)", []any{id, org}},
				{"INSERT INTO vectors VALUES(?,'space',512,?,?)", []any{org, data, digest[:]}},
				{"INSERT INTO vector_delta VALUES(?,'space')", []any{org}},
			} {
				if _, e := tx.ExecContext(ctx, v.q, v.args...); e != nil {
					return e
				}
			}
		}
		return nil
	}))
	var cases struct{ Queries [][]float32 }
	data, e := os.ReadFile(filepath.Join(dir, "query-cases.json"))
	check(e)
	check(json.Unmarshal(data, &cases))
	old := derived.NewIndex(records)
	type run struct {
		Phase        string      `json:"phase"`
		Query        int         `json:"query"`
		Milliseconds float64     `json:"milliseconds"`
		Stats        VectorStats `json:"stats"`
	}
	runs := []run{}
	verify := func(phase string) {
		for i, q := range cases.Queries {
			begin := time.Now()
			got, stats, e := s.VectorSearch(ctx, "g", "space", q, nil, 10)
			ms := float64(time.Since(begin)) / float64(time.Millisecond)
			check(e)
			want := old.Search(q, nil, 10)
			if len(got) != len(want) {
				t.Fatalf("count %d/%d", len(got), len(want))
			}
			for j := range got {
				if got[j].ID != want[j].AssetID || got[j].Score != want[j].Score {
					t.Fatalf("%s q%d rank%d mismatch", phase, i, j)
				}
			}
			runs = append(runs, run{phase, i, ms, stats})
		}
	}
	verify("delta-exact")
	for {
		var n int
		check(s.view(ctx, func(q queryer) error { return q.QueryRowContext(ctx, "SELECT count(*) FROM vector_delta").Scan(&n) }))
		if n == 0 {
			break
		}
		_, e := s.PackVectors(ctx, "space", true)
		check(e)
	}
	verify("packed-filtered")
	report := map[string]any{"vectors": count, "queries": len(cases.Queries), "bit_exact": true, "runs": runs, "cache": "OS cache not cleared; production SQLite path"}
	data, e = json.MarshalIndent(report, "", "  ")
	check(e)
	if output := os.Getenv("OWNWARD_VECTOR_REPORT"); output != "" {
		check(os.WriteFile(output, data, 0600))
	}
	t.Logf("%d vectors, %d queries: exact before and after packing", count, len(cases.Queries))
}
