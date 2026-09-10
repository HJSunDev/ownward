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

type previousIndexedRecordLayout struct {
	record    Record
	vector    vectorLocation
	hasVector bool
}

func TestRecordRuntimeFootprint(t *testing.T) {
	actual := unsafe.Sizeof(Record{})
	v0 := unsafe.Sizeof(v0RecordLayout{})
	// 遗忘新增完整输入引用与已知性标记；不允许除此以外恢复冗余运行态。
	provenance := unsafe.Sizeof(struct {
		Inputs []semantics.CandidateReference
		Known  bool
	}{})
	if actual > v0+provenance {
		t.Fatalf("current runtime record exceeded provenance budget: current=%d budget=%d", actual, v0+provenance)
	}
}

func TestIndexedRecordDoesNotStoreDerivableVectorState(t *testing.T) {
	actual := unsafe.Sizeof(indexedRecord{})
	previous := unsafe.Sizeof(previousIndexedRecordLayout{})
	if actual >= previous {
		t.Fatalf("indexed record retained derivable vector state: current=%d previous=%d", actual, previous)
	}
	if actual != unsafe.Sizeof(Record{})+unsafe.Sizeof(vectorLocation{}) {
		t.Fatalf("indexed record contains unexpected padding or state: current=%d", actual)
	}
}
