package kernelv2candidate

import (
	"context"
	"errors"
	"sync"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

const RepresentationLifecyclePolicy = "sealed-current-revision-raw-to-ready-formal-release/v2"

var (
	ErrRepresentationClosed  = errors.New("检索表示生命周期已经关闭")
	ErrRepresentationStale   = errors.New("检索表示已被新的资产版本取代")
	ErrRepresentationPending = errors.New("检索表示仍在生成")
	ErrRepresentationReady   = errors.New("检索表示已进入派生存储和索引")
)

type representationKey struct {
	assetID  string
	revision uint64
}

type representationJob struct {
	content string
	done    chan struct{}
	vector  []float32
	err     error
	waiters int
	retired bool
}

// RepresentationLifecycle owns only discardable execution state for exact
// current-revision document vectors. The durable pending record remains in the
// derived store owned by core; authoritative content remains in AssetAuthority.
// One bounded worker reuses the declared VectorCapability without introducing
// an additional model, space or persistent source of truth.
type RepresentationLifecycle struct {
	capability   contract.VectorCapability
	maximumBytes int

	ctx    context.Context
	cancel context.CancelFunc
	queue  chan representationKey
	done   chan struct{}

	mu     sync.Mutex
	jobs   map[representationKey]*representationJob
	latest map[string]uint64
	ready  map[string]uint64
	closed bool
}

func NewRepresentationLifecycle(capability contract.VectorCapability, maximumBytes int) *RepresentationLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	lifecycle := &RepresentationLifecycle{
		capability: capability, maximumBytes: maximumBytes,
		ctx: ctx, cancel: cancel, queue: make(chan representationKey, 64), done: make(chan struct{}),
		jobs: make(map[representationKey]*representationJob), latest: make(map[string]uint64),
		ready: make(map[string]uint64),
	}
	go lifecycle.run()
	return lifecycle
}

// Start seals exactly one current-revision job. Older results remain unable to
// satisfy Wait because callers must present both asset identity and revision.
func (l *RepresentationLifecycle) Start(asset domain.Information) error {
	_, err := l.start(asset, false)
	return err
}

func (l *RepresentationLifecycle) start(asset domain.Information, acquire bool) (*representationJob, error) {
	if l == nil || l.capability == nil {
		return nil, errors.New("向量能力不存在")
	}
	if asset.ID == "" || asset.Revision == 0 || asset.Content == "" {
		return nil, errors.New("检索表示任务身份不完整")
	}
	key := representationKey{assetID: asset.ID, revision: asset.Revision}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil, ErrRepresentationClosed
	}
	if latest := l.latest[asset.ID]; latest > asset.Revision {
		l.mu.Unlock()
		return nil, ErrRepresentationStale
	}
	if l.ready[asset.ID] == asset.Revision {
		l.mu.Unlock()
		return nil, ErrRepresentationReady
	}
	if existing := l.jobs[key]; existing != nil {
		if acquire {
			existing.waiters++
		}
		l.mu.Unlock()
		return existing, nil
	}
	job := &representationJob{content: asset.Content, done: make(chan struct{})}
	if acquire {
		job.waiters = 1
	}
	l.jobs[key] = job
	l.latest[asset.ID] = asset.Revision
	delete(l.ready, asset.ID)
	for existing := range l.jobs {
		if existing.assetID == asset.ID && existing.revision < asset.Revision {
			obsolete := l.jobs[existing]
			obsolete.err = ErrRepresentationStale
			select {
			case <-obsolete.done:
			default:
				close(obsolete.done)
			}
			l.retireLocked(existing, obsolete)
		}
	}
	select {
	case l.queue <- key:
		l.mu.Unlock()
		return job, nil
	default:
		job.waiters = 0
		l.retireLocked(key, job)
		l.mu.Unlock()
		return nil, errors.New("检索表示任务队列已满")
	}
}

// Wait returns only a vector produced for the exact current revision. A
// restart simply recreates a job from authority through Ensure; no result is
// accepted by asset identity alone.
func (l *RepresentationLifecycle) Wait(ctx context.Context, assetID string, revision uint64) ([]float32, error) {
	if l == nil {
		return nil, errors.New("检索表示生命周期不存在")
	}
	key := representationKey{assetID: assetID, revision: revision}
	l.mu.Lock()
	job := l.jobs[key]
	latest := l.latest[assetID]
	ready := l.ready[assetID]
	if job != nil && latest <= revision {
		job.waiters++
	}
	l.mu.Unlock()
	if latest > revision {
		return nil, ErrRepresentationStale
	}
	if job == nil {
		if ready == revision {
			return nil, ErrRepresentationReady
		}
		return nil, errors.New("检索表示任务不存在")
	}
	return l.await(ctx, job)
}

func (l *RepresentationLifecycle) await(ctx context.Context, job *representationJob) ([]float32, error) {
	select {
	case <-ctx.Done():
		l.mu.Lock()
		l.releaseWaiterLocked(job)
		l.mu.Unlock()
		return nil, ctx.Err()
	case <-job.done:
		l.mu.Lock()
		vector := append([]float32(nil), job.vector...)
		err := job.err
		l.releaseWaiterLocked(job)
		l.mu.Unlock()
		return vector, err
	}
}

func (l *RepresentationLifecycle) Ensure(ctx context.Context, asset domain.Information) ([]float32, error) {
	job, err := l.start(asset, true)
	if err != nil {
		return nil, err
	}
	return l.await(ctx, job)
}

// Invalidate retires only representations older than the newly accepted
// authority revision. It never creates work and therefore also covers updates
// whose new content is not represented by this lifecycle.
func (l *RepresentationLifecycle) Invalidate(assetID string, revision uint64) {
	if l == nil || assetID == "" || revision == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.latest[assetID] >= revision {
		return
	}
	l.latest[assetID] = revision
	delete(l.ready, assetID)
	for key, job := range l.jobs {
		if key.assetID != assetID || key.revision >= revision {
			continue
		}
		job.err = ErrRepresentationStale
		select {
		case <-job.done:
		default:
			close(job.done)
		}
		l.retireLocked(key, job)
	}
}

// Commit releases an exact revision only after its vector has been durably
// written and indexed by the owning core service. Existing waiters keep their
// job reference until they copy the result; new callers must use the durable
// derived record and index instead of regenerating the vector.
func (l *RepresentationLifecycle) Commit(assetID string, revision uint64) error {
	if l == nil {
		return errors.New("检索表示生命周期不存在")
	}
	key := representationKey{assetID: assetID, revision: revision}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.latest[assetID] > revision {
		return ErrRepresentationStale
	}
	if l.ready[assetID] == revision {
		return nil
	}
	job := l.jobs[key]
	if job == nil {
		return errors.New("检索表示任务不存在")
	}
	select {
	case <-job.done:
	default:
		return ErrRepresentationPending
	}
	if job.err != nil {
		return job.err
	}
	if len(job.vector) == 0 {
		return errors.New("检索表示向量不完整")
	}
	l.ready[assetID] = revision
	l.retireLocked(key, job)
	return nil
}

func (l *RepresentationLifecycle) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		<-l.done
		return nil
	}
	l.closed = true
	l.cancel()
	l.mu.Unlock()
	<-l.done
	return nil
}

func (l *RepresentationLifecycle) run() {
	defer close(l.done)
	var pending *representationKey
	for {
		var first representationKey
		if pending != nil {
			first = *pending
			pending = nil
		} else {
			select {
			case <-l.ctx.Done():
				l.failOpenJobs(ErrRepresentationClosed)
				return
			case first = <-l.queue:
			}
		}
		{
			keys := []representationKey{first}
			values := []string{}
			l.mu.Lock()
			firstJob := l.jobs[first]
			if firstJob != nil {
				values = append(values, firstJob.content)
			}
			l.mu.Unlock()
			if firstJob == nil {
				continue
			}
			total := len([]byte(firstJob.content))
			for len(keys) < 32 {
				select {
				case next := <-l.queue:
					l.mu.Lock()
					nextJob := l.jobs[next]
					l.mu.Unlock()
					if nextJob == nil {
						continue
					}
					size := len([]byte(nextJob.content))
					if l.maximumBytes > 0 && total+size > l.maximumBytes {
						pending = &next
						goto embed
					}
					keys = append(keys, next)
					values = append(values, nextJob.content)
					total += size
				default:
					goto embed
				}
			}
		embed:
			vectors, err := l.capability.EmbedDocuments(l.ctx, values)
			if err == nil && len(vectors) != len(keys) {
				err = errors.New("本地向量能力返回数量无效")
			}
			for index, key := range keys {
				if err != nil {
					l.complete(key, nil, err)
					continue
				}
				if len(vectors[index]) != l.capability.Space().Dimensions {
					l.complete(key, nil, errors.New("本地向量能力返回维度无效"))
					continue
				}
				l.complete(key, vectors[index], nil)
			}
		}
	}
}

func (l *RepresentationLifecycle) complete(key representationKey, vector []float32, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	job := l.jobs[key]
	if job == nil {
		return
	}
	job.vector = append([]float32(nil), vector...)
	job.err = err
	select {
	case <-job.done:
	default:
		close(job.done)
	}
	if err != nil {
		l.retireLocked(key, job)
	}
}

func (l *RepresentationLifecycle) failOpenJobs(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, job := range l.jobs {
		select {
		case <-job.done:
		default:
			job.err = err
			close(job.done)
		}
		l.retireLocked(key, job)
	}
}

func (l *RepresentationLifecycle) retireLocked(key representationKey, job *representationJob) {
	if current := l.jobs[key]; current == job {
		delete(l.jobs, key)
	}
	job.retired = true
	if job.waiters == 0 {
		job.content = ""
		job.vector = nil
		job.err = nil
	}
}

func (l *RepresentationLifecycle) releaseWaiterLocked(job *representationJob) {
	if job.waiters > 0 {
		job.waiters--
	}
	if job.retired && job.waiters == 0 {
		job.content = ""
		job.vector = nil
		job.err = nil
	}
}
