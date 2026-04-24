package search

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/storage"
)

// ChunkOracle implements rrc.ChunkOracle. Backed by the chunks +
// embeddings tables plus a live embedder for cache misses. Every
// read-through cache miss writes back so steady state is "everything
// prior is stored, only the brand-new message needs live work".
type ChunkOracle struct {
	db       *storage.DB
	embedder core.Embedder
	model    string
}

// NewChunkOracle ties the oracle to a specific embedder model. The
// model string lives in the cache keys, so switching embedders means
// constructing a new oracle (old rows stay under their original
// model_id; the new oracle reads empty and refills).
func NewChunkOracle(db *storage.DB, embedder core.Embedder, model string) *ChunkOracle {
	return &ChunkOracle{db: db, embedder: embedder, model: model}
}

// ChunksForMessages loads chunks+vectors for the given message IDs in
// one transaction. Each message's chunks are ordered by chunk_index.
// Vectors may be nil for chunks not yet embedded — the engine calls
// EnsureVector to fetch those live.
func (o *ChunkOracle) ChunksForMessages(ctx context.Context, messageIDs []string) (map[string][]rrc.ChunkRef, error) {
	if o == nil || o.db == nil || len(messageIDs) == 0 {
		return nil, nil
	}
	chunks, err := o.db.GetChunksForMessages(messageIDs)
	if err != nil {
		return nil, err
	}
	embs, err := o.db.GetChunkEmbeddingsForMessages(messageIDs, o.model)
	if err != nil {
		return nil, err
	}

	type embKey struct {
		MsgID    string
		ChunkIdx int
	}
	vecs := make(map[embKey][]float32)
	for msgID, list := range embs {
		for _, ce := range list {
			vecs[embKey{MsgID: msgID, ChunkIdx: ce.ChunkIndex}] = ce.Vector
		}
	}

	out := make(map[string][]rrc.ChunkRef, len(chunks))
	for msgID, cs := range chunks {
		refs := make([]rrc.ChunkRef, len(cs))
		for i, c := range cs {
			refs[i] = rrc.ChunkRef{
				MessageID:  c.MessageID,
				ChunkIndex: c.ChunkIndex,
				Text:       c.Text,
				Vector:     vecs[embKey{MsgID: c.MessageID, ChunkIdx: c.ChunkIndex}],
			}
		}
		out[msgID] = refs
	}
	return out, nil
}

// EnsureVector embeds a chunk live if it isn't cached. Called by the
// engine for brand-new chunks that backfill hasn't reached yet.
func (o *ChunkOracle) EnsureVector(ctx context.Context, ref rrc.ChunkRef) ([]float32, error) {
	if len(ref.Vector) > 0 {
		return ref.Vector, nil
	}
	if o == nil || o.db == nil {
		return nil, nil
	}
	// Re-check the cache — the backfill may have populated it since
	// ChunksForMessages ran.
	if v, ok, err := o.db.GetChunkEmbedding(ref.MessageID, ref.ChunkIndex, o.model); err == nil && ok {
		return v, nil
	}
	if o.embedder == nil || ref.Text == "" {
		return nil, nil
	}
	vec, err := o.embedder.Embed(ctx, ref.Text)
	if err != nil {
		return nil, err
	}
	if err := o.db.InsertChunkEmbedding(ref.MessageID, ref.ChunkIndex, o.model, vec); err != nil {
		log.Printf("ChunkOracle: InsertChunkEmbedding(%s[%d], %s): %v", ref.MessageID, ref.ChunkIndex, o.model, err)
	}
	return vec, nil
}

// BackfillEmbeddings closes the gap between the chunks table and the
// embedding cache for the configured model. Runs in a goroutine from
// main.go at startup — non-blocking so the service accepts requests
// immediately; in-flight EnsureVector calls for not-yet-backfilled
// chunks just embed live via the same code path.
//
// Idempotent and resumable: the LEFT JOIN query naturally shrinks
// across restarts, so a crashed or canceled backfill picks up where
// it left off on next boot.
func (o *ChunkOracle) BackfillEmbeddings(ctx context.Context) {
	if o == nil || o.db == nil || o.embedder == nil {
		return
	}
	missing, err := o.db.ChunksMissingEmbeddings(o.model)
	if err != nil {
		log.Printf("Backfill: ChunksMissingEmbeddings(model=%s): %v", o.model, err)
		return
	}
	if len(missing) == 0 {
		return
	}
	log.Printf("Backfill: embedding %d chunks for model %s", len(missing), o.model)

	const (
		chunkSize   = 32 // matches TEI max_client_batch_size
		concurrency = 4  // matches TEI max_batch_requests
	)

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var done atomicCounter

	start := time.Now()
	for i := 0; i < len(missing); i += chunkSize {
		end := i + chunkSize
		if end > len(missing) {
			end = len(missing)
		}
		group := missing[i:end]
		wg.Add(1)
		sem <- struct{}{}
		go func(group []storage.ChunkMissing) {
			defer wg.Done()
			defer func() { <-sem }()

			texts := make([]string, len(group))
			for j, g := range group {
				texts[j] = g.Text
			}
			vecs, err := o.embedder.EmbedBatch(ctx, texts)
			if err != nil || len(vecs) != len(group) {
				// Fall back to one-at-a-time on batch failure so the
				// whole chunk isn't lost.
				vecs = embedOneByOne(ctx, o.embedder, texts)
			}
			for j, g := range group {
				if j >= len(vecs) || vecs[j] == nil {
					continue
				}
				if err := o.db.InsertChunkEmbedding(g.MessageID, g.ChunkIndex, o.model, vecs[j]); err != nil {
					log.Printf("Backfill: InsertChunkEmbedding(%s[%d]): %v", g.MessageID, g.ChunkIndex, err)
					continue
				}
				done.inc()
			}
		}(group)
	}
	wg.Wait()
	log.Printf("Backfill: embedded %d/%d chunks (model=%s) in %v",
		done.value(), len(missing), o.model, time.Since(start))
}

func embedOneByOne(ctx context.Context, e core.Embedder, texts []string) [][]float32 {
	vecs := make([][]float32, len(texts))
	for i, t := range texts {
		v, err := e.Embed(ctx, t)
		if err != nil {
			continue
		}
		vecs[i] = v
	}
	return vecs
}

type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (c *atomicCounter) inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *atomicCounter) value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
