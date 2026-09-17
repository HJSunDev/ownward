package boundedstore

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestDeploymentLegacySemanticFormatsAndBackupIsolation(t *testing.T) {
	for _, format := range []string{"ownward.derived/v2", "ownward.derived/v3"} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			body := legacyFixture(t, root)
			when := time.Unix(100, 0).UTC()
			asset := domain.Information{Schema: domain.AssetSchema, ID: "asset", Revision: 4, CreatedAt: when, UpdatedAt: when, Kind: domain.KindKnowledge, Content: body}
			work, e := semantics.NewWork("old-generation", asset, nil, nil, when)
			if e != nil {
				t.Fatal(e)
			}
			analysis := semantics.Analysis{Summary: "Preserved accepted knowledge"}
			sub := semantics.Submission{Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: asset.ID, Revision: 4, Capability: semantics.Capability{ID: "fixture", Version: "1"}, Status: semantics.SubmissionComplete, Analysis: analysis, AcceptedAt: when}
			receipt, e := semantics.NewSubmissionReceipt(sub)
			if e != nil {
				t.Fatal(e)
			}
			ref, e := semantics.ReferenceWork(work)
			if e != nil {
				t.Fatal(e)
			}
			v := map[string]any{"schema": format, "asset_id": "asset", "asset_revision": 4, "generated_at": when, "provider": "fixture", "status": "ready", "analysis": analysis, "semantic_work": work, "semantic_result": sub, "embedding_space": "space"}
			vectors := make([]byte, 2048)
			binary.LittleEndian.PutUint32(vectors[0:4], 0x3f800000)
			if e = os.MkdirAll(filepath.Join(root, "state"), 0700); e != nil {
				t.Fatal(e)
			}
			if format == "ownward.derived/v2" {
				v["embedding_f32le"] = vectors
				b, _ := json.Marshal(v)
				e = os.WriteFile(filepath.Join(root, "state", "organization.jsonl"), append(b, '\n'), 0600)
			} else {
				b, _ := json.Marshal(v)
				h := make([]byte, 16)
				copy(h, "OWD3")
				binary.LittleEndian.PutUint32(h[4:8], uint32(len(b)))
				binary.LittleEndian.PutUint32(h[8:12], uint32(len(vectors)))
				crc := crc32.NewIEEE()
				crc.Write(b)
				crc.Write(vectors)
				binary.LittleEndian.PutUint32(h[12:], crc.Sum32())
				e = os.WriteFile(filepath.Join(root, "state", "organization.binlog"), append(append(append(h, b...), vectors...), []byte("DONE")...), 0600)
			}
			if e != nil {
				t.Fatal(e)
			}
			o := deploymentOptions()
			initial := contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "composition", ActiveKernelGeneration: "kernel"}
			o.Initialize = func(ctx context.Context, s *Store, _ string) error {
				_, e := s.OpenControlAuthority(ctx, initial)
				return e
			}
			s, e := OpenDeployment(ctx, root, o)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			version, e := s.CurrentOrganization(ctx, "legacy-migration", "asset")
			if e != nil {
				t.Fatal(e)
			}
			h, e := s.RecordHeader(ctx, version)
			if e != nil {
				t.Fatal(e)
			}
			gotRef, _ := json.Marshal(h.SemanticWorkReference)
			wantRef, _ := json.Marshal(ref)
			if string(gotRef) != string(wantRef) || !reflect.DeepEqual(h.SemanticReceipt, &receipt) || h.Analysis.Summary != analysis.Summary {
				t.Fatalf("legacy semantic identity changed: %s / %s; receipt %#v / %#v", gotRef, wantRef, h.SemanticReceipt, receipt)
			}
			a, e := s.OpenControlAuthority(ctx, initial)
			if e != nil {
				t.Fatal(e)
			}
			c := informationcontrol.New(a)
			token, e := c.InitializeOwner("Owner")
			if e != nil {
				t.Fatal(e)
			}
			archive := filepath.Join(t.TempDir(), "assets.zip")
			if e = s.ExportArchive(ctx, archive); e != nil {
				t.Fatal(e)
			}
			if e = s.ExportArchive(ctx, archive); e == nil {
				t.Fatal("existing backup overwritten")
			}
			out := filepath.Join(t.TempDir(), "restored")
			if _, e = RestoreArchive(ctx, archive, out, o.Options); e != nil {
				t.Fatal(e)
			}
			r, e := OpenDeployment(ctx, out, o)
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			var n int
			if e = r.writer.QueryRowContext(ctx, "SELECT count(*) FROM organizations").Scan(&n); e != nil || n != 0 {
				t.Fatal("asset backup retained derived data", n, e)
			}
			if readTest(t, r, "asset") != body {
				t.Fatal("asset backup changed raw")
			}
			if _, e = r.BeginAccess(ctx, contract.AuthenticationDigest(informationcontrol.Authenticate(ctx, token)), contract.ReadPermission); e == nil {
				t.Fatal("restored old credential accepted")
			}
		})
	}
}
