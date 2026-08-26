package derived

import (
	"testing"
	"time"
	"unsafe"

	"github.com/HJSunDev/ownward/internal/semantics"
)

type v0RecordLayout struct {
	Schema         string
	AssetID        string
	AssetRevision  uint64
	GeneratedAt    time.Time
	Provider       string
	Status         string
	Error          string
	Analysis       semantics.Analysis
	SemanticWork   *semantics.Work
	SemanticResult *semantics.Submission
	EmbeddingSpace string
	Embedding      []float32
}

func TestRecordRuntimeFootprint(t *testing.T) {
	actual := unsafe.Sizeof(Record{})
	v0 := unsafe.Sizeof(v0RecordLayout{})
	if actual > v0 {
		t.Fatalf("current runtime record grew beyond V0: current=%d V0=%d", actual, v0)
	}
}
