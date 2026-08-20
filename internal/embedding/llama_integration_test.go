package embedding

import (
	"context"
	"math"
	"os"
	"testing"
	"time"
)

func TestManagedSelectedBundle(t *testing.T) {
	root := os.Getenv("OWNWARD_EMBEDDING_TEST_BUNDLE")
	if root == "" {
		t.Skip("未提供真实向量能力包")
	}
	provider, err := OpenManaged(root)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	documents, err := provider.EmbedDocuments(ctx, []string{
		"我每周一晚上进行力量训练，地点是社区健身房。",
		"数据库迁移必须先完成备份，再执行版本升级。",
	})
	if err != nil {
		t.Fatal(err)
	}
	query, err := provider.EmbedQuery(ctx, "周一的健身安排是什么？")
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 2 || len(query) != 512 || len(documents[0]) != 512 || len(documents[1]) != 512 {
		t.Fatalf("unexpected vector dimensions: documents=%d query=%d", len(documents), len(query))
	}
	if math.Abs(vectorNorm(query)-1) > 1e-5 || math.Abs(vectorNorm(documents[0])-1) > 1e-5 {
		t.Fatal("vectors are not L2 normalized")
	}
	if cosine(query, documents[0]) <= cosine(query, documents[1]) {
		t.Fatal("selected bundle did not preserve expected semantic ordering")
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if provider.command != nil || provider.done != nil {
		t.Fatal("managed runtime was not released")
	}
}

func cosine(left, right []float32) float64 {
	result := float64(0)
	for index := range left {
		result += float64(left[index] * right[index])
	}
	return result
}
