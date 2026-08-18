package derived

import (
	"fmt"
	"math"
	"testing"
)

func BenchmarkSemanticSearch100K384D(b *testing.B) {
	const dimensions = 384
	records := make([]Record, 100_000)
	for index := range records {
		vector := make([]float32, dimensions)
		for dimension := range vector {
			vector[dimension] = float32(math.Sin(float64((index + 1) * (dimension + 1))))
		}
		normalizeBenchmarkVector(vector)
		records[index] = Record{AssetID: fmt.Sprintf("I%06d", index), AssetRevision: 1, Status: "ready", Embedding: vector}
	}
	index := NewIndex(records)
	query := append([]float32(nil), records[42_424].Embedding...)
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		_ = index.Search(query, nil, 10)
	}
}

func normalizeBenchmarkVector(vector []float32) {
	length := float64(0)
	for _, value := range vector {
		length += float64(value * value)
	}
	divisor := float32(math.Sqrt(length))
	for index := range vector {
		vector[index] /= divisor
	}
}
