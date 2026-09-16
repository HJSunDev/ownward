package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
	"github.com/google/jsonschema-go/jsonschema"
)

func TestStreamingSchemaMatchesExistingValidator(t *testing.T) {
	create, _ := jsonschema.For[CreateInput](nil)
	update, _ := jsonschema.For[UpdateInput](nil)
	batch, _ := jsonschema.For[CreateBatchInput](nil)
	read, _ := jsonschema.For[ReadInput](nil)
	inputs := []string{`{}`, `null`, `{"content":"中文"}`, `{"content":2}`, `{"content":null}`, `{"content":"a","unknown":1}`, `{"content":"a","source":{"ref":"x"}}`, `{"content":"a","contexts":[{"key":"a"}]}`, `{"content":"a","explicit_relations":[{"type":"qualifies","target_id":"x","selector":{"exact":"a","prefix":""}}]}`, `{"id":"x","expected_revision":1}`, `{"id":"x","expected_revision":1.5}`, `{"id":"x","expected_revision":-1}`, `{"id":"x","expected_revision":1,"contexts":[]}`, `{"items":[{"content":"a"}]}`, `{"items":null}`, `{"id":null}`}
	b, _ := resourcebudget.New(resourcebudget.MiB, 0)
	for _, schema := range []*jsonschema.Schema{create, update, batch, read} {
		r, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range inputs {
			d, err := streamjson.Parse(context.Background(), t.TempDir(), strings.NewReader(raw), b, resourcebudget.MiB)
			if err != nil {
				t.Fatal(err)
			}
			var value any
			json.Unmarshal([]byte(raw), &value)
			want, got := r.Validate(value), d.Root().Validate(context.Background(), schema)
			d.Close()
			if (want == nil) != (got == nil) {
				t.Fatalf("%s: 原校验=%v 流式校验=%v", raw, want, got)
			}
		}
	}
}
