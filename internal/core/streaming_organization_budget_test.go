package core

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/semanticstream"
	"github.com/HJSunDev/ownward/internal/streamjson"
	"github.com/google/jsonschema-go/jsonschema"
	"io"
	"runtime"
	"testing"
)

func TestStreamingSchemaValidOrganizationWorkingSet(t *testing.T) {
	budget, _ := resourcebudget.New(4*resourcebudget.MiB, 0)
	ctx := resourcebudget.WithContext(context.Background(), budget)
	dir := t.TempDir()
	doc, e := streamjson.Build(ctx, dir, budget, 32*resourcebudget.MiB, func(w io.Writer) error {
		io.WriteString(w, `{"schema":"ownward.organization/v2","units":[`)
		selector := `{"exact":"Alice","prefix":"","suffix":""}`
		for u := 0; u < 128; u++ {
			if u > 0 {
				io.WriteString(w, ",")
			}
			fmt.Fprintf(w, `{"id":"u%d","statement":"Alice","selector":%s,"context":[`, u, selector)
			for c := 0; c < 16; c++ {
				if c > 0 {
					io.WriteString(w, ",")
				}
				io.WriteString(w, selector)
			}
			io.WriteString(w, `],"mentions":[`)
			for m := 0; m < 32; m++ {
				if m > 0 {
					io.WriteString(w, ",")
				}
				fmt.Fprintf(w, `{"id":"m%d","name":"Alice","role":"person","selector":%s}`, m, selector)
			}
			io.WriteString(w, `]}`)
		}
		io.WriteString(w, `],"links":[`)
		endpoint := `{"asset_id":"a","unit_id":"u0","mention_id":"m0","selector":` + selector + `}`
		for l := 0; l < 128; l++ {
			if l > 0 {
				io.WriteString(w, ",")
			}
			fmt.Fprintf(w, `{"type":"supports","meaning":"Alice","source":%s,"target":%s,"conditions":[`, endpoint, endpoint)
			for c := 0; c < 16; c++ {
				if c > 0 {
					io.WriteString(w, ",")
				}
				io.WriteString(w, endpoint)
			}
			io.WriteString(w, `]}`)
		}
		_, e := io.WriteString(w, `]}`)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	defer doc.Close()
	var schema jsonschema.Schema
	if e = json.Unmarshal(semantics.OrganizationOutputSchema(), &schema); e != nil {
		t.Fatal(e)
	}
	if e = doc.Root().Validate(ctx, &schema); e != nil {
		t.Fatalf("public organization schema rejected: %v", e)
	}
	t.Log("public organization schema passed; semantic normalization intentionally not claimed")
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	org, e := semanticstream.ReadOrganization(doc.Root())
	if e != nil {
		t.Fatal(e)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("units=%d links=%d heap_delta=%d child_budget_used=%d", len(org.Units), len(org.Links), delta, budget.Used())
	runtime.KeepAlive(org)
	if delta > 4*resourcebudget.MiB {
		t.Errorf("organization metadata alone exceeds 4 MiB whole-operation reservation before semantic validation")
	}
}
