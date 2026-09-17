package boundedstore

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func TestCompactPostingsUpgradeAndResume(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupted), func(t *testing.T) {
			ctx := context.Background()
			b, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
			path := filepath.Join(t.TempDir(), "store.sqlite")
			s, e := Open(ctx, path, Options{Budget: b, paused: true})
			if e != nil {
				t.Fatal(e)
			}
			var body strings.Builder
			for i := 0; i < 200; i++ {
				fmt.Fprintf(&body, "token%d ", i)
			}
			putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 1, Content: body.String()})
			putRetrievalAsset(t, s, domain.Information{ID: "b", Revision: 1, Content: "token32 token32 token41"})
			before, e := s.LexicalSearch(ctx, "token32 token41", nil, 10)
			if e != nil {
				t.Fatal(e)
			}
			// Recreate the previous on-disk format without retokenizing content.
			_, e = s.writer.ExecContext(ctx, `CREATE TABLE old_postings(term BLOB,payload TEXT,frequency INTEGER,PRIMARY KEY(term,payload)) WITHOUT ROWID;
INSERT INTO old_postings SELECT p.term,i.payload,p.frequency FROM postings p JOIN lexical_payload_ids i ON i.id=p.payload;
DROP TABLE postings; DROP TABLE lexical_payload_ids; ALTER TABLE old_postings RENAME TO postings;
CREATE INDEX postings_payload ON postings(payload,term); UPDATE store_meta SET value=2 WHERE key='format';`)
			if e != nil {
				t.Fatal(e)
			}
			if interrupted {
				_, e = s.writer.ExecContext(ctx, `ALTER TABLE postings RENAME TO lexical_upgrade_source;`+retrievalSchema+`UPDATE store_meta SET value=3 WHERE key='format';`)
				if e != nil {
					t.Fatal(e)
				}
				if more, e := s.upgradeLexicalBatch(ctx); e != nil || !more {
					t.Fatal(more, e)
				}
				if n := countTest(t, s, "SELECT count(*) FROM postings"); n != 64 {
					t.Fatal(n)
				}
			}
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			s, e = Open(ctx, path, Options{Budget: b, paused: true})
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			after, e := s.LexicalSearch(ctx, "token32 token41", nil, 10)
			if e != nil || !reflect.DeepEqual(before, after) {
				t.Fatal(before, after, e)
			}
			if n := countTest(t, s, "SELECT count(*) FROM sqlite_master WHERE name='lexical_upgrade_source'"); n != 0 {
				t.Fatal(n)
			}
			if n := countTest(t, s, "SELECT count(*) FROM postings WHERE typeof(payload)!='integer'"); n != 0 {
				t.Fatal(n)
			}
			putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 2, Content: "replacement"})
			if e = s.DrainMaintenance(ctx); e != nil {
				t.Fatal(e)
			}
			if n := countTest(t, s, "SELECT count(*) FROM lexical_payload_ids"); n != 2 {
				t.Fatal("retired identity retained", n)
			}
			var bad sql.NullString
			if e = s.writer.QueryRowContext(ctx, "SELECT \"table\" FROM pragma_foreign_key_check LIMIT 1").Scan(&bad); e != sql.ErrNoRows {
				t.Fatal("foreign key failure", bad, e)
			}
		})
	}
}
