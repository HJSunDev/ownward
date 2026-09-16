package streamjson

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func parseTest(t *testing.T, input string) *Document {
	t.Helper()
	b, _ := resourcebudget.New(2*resourcebudget.MiB, 0)
	d, err := Parse(context.Background(), t.TempDir(), strings.NewReader(input), b, 32*resourcebudget.MiB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}
func TestLargeStringsArraysAndCanonicalDigestInput(t *testing.T) {
	value := strings.Repeat("原文\n\"&<>🙂\\\u2028\u2029", 20000)
	input := `{"z":[1.00,1e3,null,false],"content":` + strconvQuote(value) + `,"a":{"n":"\ud800x\udc00","pair":"\ud83d\ude42"}}`
	d := parseTest(t, input)
	field, ok, err := d.Root().Field("content")
	if err != nil || !ok {
		t.Fatal(err)
	}
	r, err := field.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(got) != value {
		t.Fatal("decoded text differs", err)
	}
	var decoded any
	if err = json.Unmarshal([]byte(input), &decoded); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(decoded)
	var out bytes.Buffer
	if err = d.Root().Canonical(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, out.Bytes()) {
		t.Fatalf("canonical differs: got length %d, want %d", out.Len(), len(want))
	}
}
func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
func TestRejectInvalidAndDuplicateFields(t *testing.T) {
	for _, input := range []string{`{"a":1,"a":2}`, `{"a":1,"\u0061":2}`, `{"x":"bad\z"}`, `[1,]`, `{"a":}`, `true false`, `{"a":"\u00q1"}`} {
		b, _ := resourcebudget.New(resourcebudget.MiB, 0)
		d, err := Parse(context.Background(), t.TempDir(), strings.NewReader(input), b, resourcebudget.MiB)
		if err == nil {
			d.Close()
			t.Errorf("accepted %s", input)
		}
		if b.Used() != 0 {
			t.Fatal("leaked workspace")
		}
	}
}
func TestStreamingArrayAndCleanup(t *testing.T) {
	d := parseTest(t, "["+strings.Repeat(`{"n":1},`, 10000)+`{"n":2}]`)
	c := d.Root().Children()
	count := 0
	for {
		_, err := c.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 10001 {
		t.Fatal(count)
	}
}

func TestControlCharacterEncodingAndCanonicalIdentity(t *testing.T) {
	for i := 0; i < 32; i++ {
		value := "before" + string(rune(i)) + "after"
		want, _ := json.Marshal(value)
		var encoded bytes.Buffer
		if err := WriteString(context.Background(), &encoded, strings.NewReader(value)); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded.Bytes(), want) {
			t.Fatalf("U+%04X: got %s want %s", i, encoded.Bytes(), want)
		}
		object := map[string]any{"content": value, "array": []string{value}}
		canonical, _ := json.Marshal(object)
		doc := parseTest(t, string(canonical))
		encoded.Reset()
		if err := doc.Root().Canonical(context.Background(), &encoded); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded.Bytes(), canonical) {
			t.Fatalf("canonical identity changed for U+%04X", i)
		}
	}
}
