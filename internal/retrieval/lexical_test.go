package retrieval

import (
	"fmt"
	"testing"

	"github.com/HJSunDev/ownward/internal/domain"
)

func TestSearchTreatsUnscopedInformationAsGenerallyApplicable(t *testing.T) {
	index := NewLexical([]domain.Information{
		{ID: "general", Content: "删除目录前校验绝对路径"},
		{ID: "windows", Content: "Windows 删除目录使用 LiteralPath", Contexts: []domain.Context{{Key: "platform", Value: "windows"}}},
		{ID: "linux", Content: "Linux 删除目录使用 rm", Contexts: []domain.Context{{Key: "platform", Value: "linux"}}},
	})
	results := index.Search("删除目录", []domain.Context{{Key: "platform", Value: "windows"}}, 10)
	if len(results) != 2 {
		t.Fatalf("unexpected results: %#v", results)
	}
	for _, result := range results {
		if result.Information.ID == "linux" {
			t.Fatal("incompatible scoped information leaked into results")
		}
	}
}

func TestUpsertRemovesStaleTermsWithoutRebuildingWholeIndex(t *testing.T) {
	index := NewLexical([]domain.Information{{ID: "one", Content: "旧线索 alpha"}})
	index.Upsert(domain.Information{ID: "one", Content: "新线索 beta"})
	if results := index.Search("alpha", nil, 10); len(results) != 0 {
		t.Fatalf("stale term remained searchable: %#v", results)
	}
	results := index.Search("beta", nil, 10)
	if len(results) != 1 || results[0].Information.ID != "one" {
		t.Fatalf("updated term is not searchable: %#v", results)
	}
}

func TestGenerationRolloverKeepsOnlyCurrentTermsSearchable(t *testing.T) {
	index := NewLexical([]domain.Information{{ID: "one", Content: "旧线索 alpha"}})
	index.generation = ^uint32(0)
	index.Upsert(domain.Information{ID: "one", Content: "新线索 beta"})
	if results := index.Search("alpha", nil, 10); len(results) != 0 {
		t.Fatalf("stale term survived generation rollover: %#v", results)
	}
	results := index.Search("beta", nil, 10)
	if len(results) != 1 || results[0].Information.ID != "one" {
		t.Fatalf("current term was lost during generation rollover: %#v", results)
	}
}

func TestSearchPrioritizesNamedTermsOverGenericQuestionWording(t *testing.T) {
	index := NewLexical([]domain.Information{
		{ID: "fiber", Content: "Fiber 是 React 协调器的工作单元结构，使渲染工作能够被拆分、排序、暂停和恢复。"},
		{ID: "generic", Content: "复杂问题应根据中间证据改变线索并继续搜索。"},
		{ID: "react", Content: "React 使用声明式组件描述界面。"},
	})
	results := index.Search("Fiber 在 React 中解决什么问题？", nil, 10)
	if len(results) == 0 || results[0].Information.ID != "fiber" {
		t.Fatalf("named terms did not dominate generic question wording: %#v", results)
	}
}

func BenchmarkSearch100K(b *testing.B) {
	values := make([]domain.Information, 100_000)
	for index := range values {
		values[index] = domain.Information{
			ID:      fmt.Sprintf("I%06d", index),
			Kind:    domain.KindKnowledge,
			Content: fmt.Sprintf("个人信息长期记录 %d，属于主题分组 %d，并包含可复用的实践经验。", index, index%100),
		}
	}
	index := NewLexical(values)
	b.Run("explicit_identity", func(b *testing.B) {
		for iteration := 0; iteration < b.N; iteration++ {
			_ = index.Search("I099999", nil, 10)
		}
	})
	b.Run("broad_common_terms", func(b *testing.B) {
		for iteration := 0; iteration < b.N; iteration++ {
			_ = index.Search("主题分组 group42", nil, 10)
		}
	})
	b.Run("parallel_8", func(b *testing.B) {
		b.SetParallelism(8)
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = index.Search("主题分组 group42", nil, 10)
			}
		})
	})
}
