package kernelv2candidate

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
)

type representationCapability struct {
	mu      sync.Mutex
	calls   [][]string
	failure error
}

func (*representationCapability) Name() string { return "representation-test" }
func (*representationCapability) Space() embedding.Space {
	return embedding.Space{ID: "representation-test/v1", Dimensions: 2}
}
func (c *representationCapability) EmbedDocuments(ctx context.Context, values []string) ([][]float32, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	c.mu.Lock()
	c.calls = append(c.calls, append([]string(nil), values...))
	failure := c.failure
	c.mu.Unlock()
	if failure != nil {
		return nil, failure
	}
	result := make([][]float32, len(values))
	for index := range result {
		result[index] = []float32{float32(len(values[index])), float32(index + 1)}
	}
	return result, nil
}
func (*representationCapability) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, errors.New("unused")
}
func (*representationCapability) Close() error { return nil }

type blockingRepresentationCapability struct {
	representationCapability
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingRepresentationCapability) EmbedDocuments(ctx context.Context, values []string) ([][]float32, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return c.representationCapability.EmbedDocuments(ctx, values)
	}
}

func TestRepresentationLifecycleSealsExactRevisionAndRejectsStaleWait(t *testing.T) {
	capability := &representationCapability{}
	lifecycle := NewRepresentationLifecycle(capability, 320)
	t.Cleanup(func() { _ = lifecycle.Close() })
	one := domain.Information{ID: "asset", Revision: 1, Content: "one"}
	if err := lifecycle.Start(one); err != nil {
		t.Fatal(err)
	}
	vector, err := lifecycle.Wait(context.Background(), one.ID, one.Revision)
	if err != nil || len(vector) != 2 || vector[0] != 3 {
		t.Fatalf("unexpected vector=%v err=%v", vector, err)
	}
	two := domain.Information{ID: "asset", Revision: 2, Content: "second"}
	if err := lifecycle.Start(two); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Wait(context.Background(), one.ID, one.Revision); !errors.Is(err, ErrRepresentationStale) {
		t.Fatalf("stale revision was not rejected: %v", err)
	}
	if _, err := lifecycle.Wait(context.Background(), two.ID, two.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestRepresentationLifecycleBatchesWithinBoundAndReturnsCopies(t *testing.T) {
	capability := &representationCapability{}
	lifecycle := NewRepresentationLifecycle(capability, 8)
	t.Cleanup(func() { _ = lifecycle.Close() })
	assets := []domain.Information{
		{ID: "a", Revision: 1, Content: "1234"},
		{ID: "b", Revision: 1, Content: "5678"},
		{ID: "c", Revision: 1, Content: "9012"},
	}
	for _, asset := range assets {
		if err := lifecycle.Start(asset); err != nil {
			t.Fatal(err)
		}
	}
	first, err := lifecycle.Wait(context.Background(), "a", 1)
	if err != nil {
		t.Fatal(err)
	}
	first[0] = 99
	again, err := lifecycle.Wait(context.Background(), "a", 1)
	if err != nil || again[0] == 99 {
		t.Fatalf("job result was not immutable: %v %v", again, err)
	}
	for _, asset := range assets[1:] {
		if _, err := lifecycle.Wait(context.Background(), asset.ID, asset.Revision); err != nil {
			t.Fatal(err)
		}
	}
	capability.mu.Lock()
	defer capability.mu.Unlock()
	for _, call := range capability.calls {
		total := 0
		for _, value := range call {
			total += len([]byte(value))
		}
		if total > 8 {
			t.Fatalf("unbounded batch: %d", total)
		}
	}
}

func TestRepresentationLifecycleFailureAndCloseFailOpen(t *testing.T) {
	capability := &representationCapability{failure: errors.New("vector unavailable")}
	lifecycle := NewRepresentationLifecycle(capability, 320)
	asset := domain.Information{ID: "asset", Revision: 1, Content: "content"}
	if _, err := lifecycle.Ensure(context.Background(), asset); err == nil || err.Error() != "vector unavailable" {
		t.Fatalf("capability failure was hidden: %v", err)
	}
	if err := lifecycle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Start(domain.Information{ID: "next", Revision: 1, Content: "next"}); !errors.Is(err, ErrRepresentationClosed) {
		t.Fatalf("closed lifecycle accepted work: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := lifecycle.Wait(ctx, "missing", 1); err == nil {
		t.Fatal("missing job was accepted")
	}
}

func TestRepresentationLifecycleConcurrentUpdateReleasesStaleWaiter(t *testing.T) {
	capability := &blockingRepresentationCapability{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	lifecycle := NewRepresentationLifecycle(capability, 320)
	defer func() {
		select {
		case <-capability.release:
		default:
			close(capability.release)
		}
		_ = lifecycle.Close()
	}()
	one := domain.Information{ID: "asset", Revision: 1, Content: "one"}
	if err := lifecycle.Start(one); err != nil {
		t.Fatal(err)
	}
	select {
	case <-capability.started:
	case <-time.After(time.Second):
		t.Fatal("first revision did not start")
	}
	result := make(chan error, 1)
	go func() {
		_, err := lifecycle.Wait(context.Background(), one.ID, one.Revision)
		result <- err
	}()
	if err := lifecycle.Start(domain.Information{ID: "asset", Revision: 2, Content: "two"}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrRepresentationStale) {
			t.Fatalf("stale waiter received %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale waiter remained blocked")
	}
}

func TestRepresentationLifecycleCommitDeterministicallyBoundsCompletedJobs(t *testing.T) {
	capability := &representationCapability{}
	lifecycle := NewRepresentationLifecycle(capability, 320)
	t.Cleanup(func() { _ = lifecycle.Close() })
	for index := 0; index < 256; index++ {
		asset := domain.Information{
			ID: fmt.Sprintf("asset-%03d", index), Revision: 1,
			Content: fmt.Sprintf("durable fact %03d", index),
		}
		if _, err := lifecycle.Ensure(context.Background(), asset); err != nil {
			t.Fatal(err)
		}
		if err := lifecycle.Commit(asset.ID, asset.Revision); err != nil {
			t.Fatal(err)
		}
		lifecycle.mu.Lock()
		jobs := len(lifecycle.jobs)
		lifecycle.mu.Unlock()
		if jobs != 0 {
			t.Fatalf("ready assets retained %d complete jobs", jobs)
		}
	}
	if err := lifecycle.Start(domain.Information{ID: "asset-000", Revision: 1, Content: "durable fact 000"}); !errors.Is(err, ErrRepresentationReady) {
		t.Fatalf("ready revision was regenerated: %v", err)
	}
}

func TestRepresentationLifecycleCommitPreservesAcquiredWaiterThenClearsPayload(t *testing.T) {
	capability := &blockingRepresentationCapability{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	lifecycle := NewRepresentationLifecycle(capability, 320)
	defer lifecycle.Close()
	asset := domain.Information{ID: "asset", Revision: 1, Content: "sealed content"}
	job, err := lifecycle.start(asset, true)
	if err != nil {
		t.Fatal(err)
	}
	<-capability.started
	close(capability.release)
	<-job.done
	if err := lifecycle.Commit(asset.ID, asset.Revision); err != nil {
		t.Fatal(err)
	}
	lifecycle.mu.Lock()
	retainedVector := len(job.vector)
	retainedContent := job.content
	lifecycle.mu.Unlock()
	if retainedVector == 0 || retainedContent == "" {
		t.Fatal("commit cleared the result before an acquired waiter copied it")
	}
	vector, err := lifecycle.await(context.Background(), job)
	if err != nil || len(vector) != 2 {
		t.Fatalf("acquired waiter lost its result: %v %v", vector, err)
	}
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if job.content != "" || len(job.vector) != 0 || job.err != nil {
		t.Fatalf("released payload remained after the final waiter: %#v", job)
	}
}

func TestRepresentationLifecycleDurableFailureRetainsOneRetryableResult(t *testing.T) {
	capability := &representationCapability{}
	lifecycle := NewRepresentationLifecycle(capability, 320)
	t.Cleanup(func() { _ = lifecycle.Close() })
	asset := domain.Information{ID: "asset", Revision: 1, Content: "retryable durable value"}
	first, err := lifecycle.Ensure(context.Background(), asset)
	if err != nil {
		t.Fatal(err)
	}
	// A failed durable write never calls Commit. The same exact revision must
	// remain recoverable without another model execution.
	second, err := lifecycle.Ensure(context.Background(), asset)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("uncommitted exact revision was not recoverable: %v %v", second, err)
	}
	capability.mu.Lock()
	calls := len(capability.calls)
	capability.mu.Unlock()
	if calls != 1 {
		t.Fatalf("durable retry regenerated the vector %d times", calls)
	}
	if err := lifecycle.Commit(asset.ID, asset.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestRepresentationLifecycleEmbeddingFailureCanRetry(t *testing.T) {
	capability := &representationCapability{failure: errors.New("temporary vector failure")}
	lifecycle := NewRepresentationLifecycle(capability, 320)
	t.Cleanup(func() { _ = lifecycle.Close() })
	asset := domain.Information{ID: "asset", Revision: 1, Content: "recoverable value"}
	if _, err := lifecycle.Ensure(context.Background(), asset); err == nil || err.Error() != "temporary vector failure" {
		t.Fatalf("temporary failure was hidden: %v", err)
	}
	capability.mu.Lock()
	capability.failure = nil
	capability.mu.Unlock()
	if _, err := lifecycle.Ensure(context.Background(), asset); err != nil {
		t.Fatalf("failed representation could not retry: %v", err)
	}
	if err := lifecycle.Commit(asset.ID, asset.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestRepresentationLifecycleDoesNotCommitPendingOrOlderRevision(t *testing.T) {
	capability := &blockingRepresentationCapability{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	lifecycle := NewRepresentationLifecycle(capability, 320)
	defer func() {
		select {
		case <-capability.release:
		default:
			close(capability.release)
		}
		_ = lifecycle.Close()
	}()
	one := domain.Information{ID: "asset", Revision: 1, Content: "one"}
	if err := lifecycle.Start(one); err != nil {
		t.Fatal(err)
	}
	<-capability.started
	if err := lifecycle.Commit(one.ID, one.Revision); !errors.Is(err, ErrRepresentationPending) {
		t.Fatalf("pending representation was committed: %v", err)
	}
	lifecycle.Invalidate(one.ID, 2)
	if err := lifecycle.Commit(one.ID, one.Revision); !errors.Is(err, ErrRepresentationStale) {
		t.Fatalf("old revision committed over the new revision: %v", err)
	}
}
