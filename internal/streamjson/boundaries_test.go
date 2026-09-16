package streamjson

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func TestSortedNotesMatchOriginalStringOrdering(t *testing.T) {
	ctx := resourcebudget.WithDisk(context.Background(), resourcebudget.NewDisk(8*resourcebudget.MiB))
	s, err := NewSortedStrings(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	values := []string{"z", "a", "a", "中文", "", strings.Repeat("a", BufferBytes) + "2", strings.Repeat("a", BufferBytes) + "1", "<>&\\\n"}
	for _, v := range values {
		if err = s.Add(strings.NewReader(v)); err != nil {
			t.Fatal(err)
		}
	}
	var got bytes.Buffer
	if err = s.WriteJSON(&got); err != nil {
		t.Fatal(err)
	}
	sort.Strings(values)
	want, _ := json.Marshal(values)
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatal("依据排序或转义不同")
	}
}
func TestDiskBudgetIncludesIndexAndConcurrentDocuments(t *testing.T) {
	b, _ := resourcebudget.New(resourcebudget.MiB, 0)
	disk := resourcebudget.NewDisk(4096)
	ctx := resourcebudget.WithDisk(context.Background(), disk)
	dir := t.TempDir()
	d, err := Parse(ctx, dir, strings.NewReader(`{"v":"`+strings.Repeat("a", 2500)+`"}`), b, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(ctx, dir, strings.NewReader(`"`+strings.Repeat("b", 2000)+`"`), b, 4096); err == nil {
		t.Fatal("暂存未共享额度")
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	if disk.Used() != 0 {
		t.Fatal("失败材料未释放额度", disk.Used())
	}
	// 正文不足1KiB，但节点索引超出共享额度。
	if _, err = Parse(ctx, dir, strings.NewReader("["+strings.Repeat("0,", 100)+"0]"), b, 4096); err == nil {
		t.Fatal("索引未计入额度")
	}
	if disk.Used() != 0 {
		t.Fatal("索引失败后仍占额度")
	}
}

type selectorText string

func (s selectorText) Open(context.Context) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(s))), nil
}
func TestSelectorCrossBlockAndRepeatedText(t *testing.T) {
	ctx := resourcebudget.WithDisk(context.Background(), resourcebudget.NewDisk(4*resourcebudget.MiB))
	content := strings.Repeat("a", BufferBytes-2) + "甲乙丙丁甲乙丙戊"
	start, end, err := ResolveSelector(ctx, t.TempDir(), selectorText(content), selectorText("丁"), selectorText("甲乙丙"), selectorText("戊"))
	if err != nil || start != BufferBytes+2 || end != BufferBytes+5 {
		t.Fatal(start, end, err)
	}
	if _, _, err = ResolveSelector(ctx, t.TempDir(), selectorText(content), selectorText(""), selectorText("甲乙丙"), selectorText("")); err == nil {
		t.Fatal("重复定位未拒绝")
	}
}
