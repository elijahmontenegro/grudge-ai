package search

import (
	"context"
	"log"
	"sync"
	"time"
)

// EmbedQueue serializes background embedding work against a fixed
// worker pool and a bounded job buffer.
//
// Background — pre-queue failure mode (2026-04-23 autonomous run):
// the OnMessageStored callback in the resolver was `go
// s.EmbedMessageChunks(context.Background(), id)` — one unbounded
// goroutine per stored message, with no timeout, no cancellation,
// no concurrency limit. During an autonomous burst the runner
// produces tool calls, tool results, and assistant text in rapid
// succession; each message stored spawned another embed request
// against the shared-GPU TEI embed container. Meanwhile the RRC
// engine on the same GPU was trying to run rerank calls for new-
// message scoring. The NVIDIA driver's CUDA context eventually
// deadlocked between the two TEI containers (documented in
// NVIDIA/open-gpu-kernel-modules#968; TEI issue #713 matches the
// "live HTTP, dead inference" signature we observed). Rerank
// went silent, scorer errors started returning from OnMessage,
// and — because scorer errors were being swallowed at the
// RRCLLM boundary (now fixed) — the autonomous loop generated
// 33 blind rounds before anyone noticed.
//
// Design invariants:
//
//   - Bounded concurrency. `workers` caps in-flight embed HTTP
//     requests at a number below what the shared GPU can co-schedule
//     with rerank. 4 is the conservative choice for a single 3080 Ti
//     hosting bge-m3 + bge-reranker-v2-m3.
//
//   - Bounded queue. `buffer` is the backlog tolerated before
//     Enqueue blocks. Blocking is the right backpressure: if
//     embeds cannot keep up, the caller (InsertMessage path)
//     slows, which slows the runner, which slows the autonomous
//     loop — the entire pipeline throttles at the TEI-shaped
//     bottleneck rather than piling up goroutines.
//
//   - Per-job timeout. `context.Background()` with no deadline
//     was the old shape; a wedged TEI would hold a goroutine
//     forever. Each worker creates a per-job context with a
//     finite deadline so a wedged embed fails fast and the
//     worker is available for the next job.
//
//   - Error surface. Individual embed failures are logged at the
//     worker and do not crash the pool. Failures are not fatal
//     to the message (it's already stored); only search recall
//     for that message is degraded. If the backend is broken
//     systemically, the RRC scorer path will fail separately
//     on the next OnMessage and surface through that channel.
type EmbedQueue struct {
	searcher   *Searcher
	jobs       chan string
	wg         sync.WaitGroup
	closeOnce  sync.Once
	jobTimeout time.Duration
}

// NewEmbedQueue constructs the pool and spawns `workers` goroutines.
// Callers must invoke Close at shutdown to drain in-flight jobs and
// release the worker goroutines cleanly.
func NewEmbedQueue(s *Searcher, workers, buffer int, jobTimeout time.Duration) *EmbedQueue {
	if workers < 1 {
		workers = 1
	}
	if buffer < 1 {
		buffer = 1
	}
	if jobTimeout <= 0 {
		jobTimeout = 2 * time.Minute
	}
	q := &EmbedQueue{
		searcher:   s,
		jobs:       make(chan string, buffer),
		jobTimeout: jobTimeout,
	}
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go q.worker()
	}
	return q
}

// Enqueue submits a message ID for background embedding. Blocks
// if the queue is full — that is the desired backpressure; see
// the type comment.
func (q *EmbedQueue) Enqueue(messageID string) {
	q.jobs <- messageID
}

// Close drains the queue and waits for workers to exit. Safe to call
// more than once because closing an already-closed channel panics in
// Go — we guard via sync.Once.
func (q *EmbedQueue) Close() {
	q.closeOnce.Do(func() {
		close(q.jobs)
	})
	q.wg.Wait()
}

func (q *EmbedQueue) worker() {
	defer q.wg.Done()
	for id := range q.jobs {
		ctx, cancel := context.WithTimeout(context.Background(), q.jobTimeout)
		if err := q.searcher.EmbedMessageChunks(ctx, id); err != nil {
			log.Printf("EmbedQueue: embed %s failed: %v", id, err)
		}
		cancel()
	}
}
