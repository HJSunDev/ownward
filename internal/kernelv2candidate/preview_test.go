package kernelv2candidate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSourcePreviewOwnsTextAndBound(t *testing.T) {
	for _, text := range []string{"short complete source", strings.Repeat("unrelated preamble ", 40) + "The evening inspection now uses violet linen, superseding red felt.", strings.Repeat("无关历史。", 80) + "夜间检查组现在使用紫色布料。"} {
		got := SourcePreview(text, "evening inspection 夜间检查", 240)
		if !strings.Contains(text, strings.Trim(got, "…")) || utf8.RuneCountInString(got) > 240 {
			t.Fatalf("not a bounded source excerpt: %q", got)
		}
		if len(text) < 240 && got != text {
			t.Fatal("short source was altered")
		}
		if strings.Contains(text, "violet linen") && !strings.Contains(got, "violet linen") {
			t.Fatalf("lost query-bearing source: %q", got)
		}
		if strings.Contains(text, "紫色布料") && !strings.Contains(got, "紫色布料") {
			t.Fatalf("lost multilingual source: %q", got)
		}
	}
}

func TestSourcePreviewNoQueryAndExtremeInput(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 2, 3, 10, 240} {
		for _, content := range []string{strings.Repeat("a", 400), strings.Repeat("😀", 300), strings.Repeat("letters and punctuation. ", 40)} {
			got := SourcePreview(content, "missing", n)
			if utf8.RuneCountInString(got) > max(n, 0) || !strings.Contains(content, strings.Trim(got, "…")) {
				t.Fatalf("invalid preview for %d", n)
			}
		}
	}
}

func BenchmarkSourcePreview(b *testing.B) {
	content := strings.Repeat("A record concerns a different group and an old decision. ", 100) + "The current evening inspection uses violet linen."
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		SourcePreview(content, "current evening inspection", 240)
	}
}

func TestGroundedCueReachesPreviewWithoutImportingAnotherSourcesFact(t *testing.T) {
	for _, fact := range []string{
		"User: The archive group reserved five rooms at Cedar Hall.",
		"Assistant: The archive group reserved seven rooms at Birch Hall.",
		"用户：档案小组在青石楼预订了五个房间。",
	} {
		content := strings.Repeat("Archive group planning rooms and reservations: compare venues and make a booking. 档案小组房间预订建议。\n", 30) + fact
		got := SourcePreview(content, "archive group rooms 档案房间", 240, fact,
			"User: The archive group reserved ninety rooms in another source.")
		if !strings.Contains(got, fact) || strings.Contains(got, "ninety") || !strings.Contains(content, strings.Trim(got, "…")) {
			t.Fatalf("source-grounded cue was not delivered: %q", got)
		}
	}
}
