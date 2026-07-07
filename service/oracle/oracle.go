// Package oracle is the storage-backed rrc.ChunkOracle: chunk/vector
// access over the chunks + chunk_vectors tables with a live embedder
// for cache misses. It adapts grudge's SQLite substrate to the
// engine's retrieval seams; the user-facing semantic-search feature
// lives separately in service/search.
package oracle

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync"
	"sync/atomic"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/service/annindex"
	"github.com/elijahmontenegro/grudge/service/chunkkey"
	"github.com/elijahmontenegro/grudge/service/storage"
)

// ChunkOracle implements rrc.ChunkOracle. Retrieval runs through an in-RAM
// ANN index (service/annindex) rather than a brute-force vec0 scan, so
// candidate generation is sub-linear in the corpus — the corpus-invariance
// fix. The vec0 table stays the durable float store: the ANN index yields a
// binary-quantized shortlist, which is reranked against exact float vectors
// fetched from vec0 by rowid (an indexed point lookup, not a partition scan).
type ChunkOracle struct {
	db       *storage.DB
	embedder core.Embedder
	model    string
	index    *annindex.Index

	// threadByMsg is the RAM-resident thread of every message, so thread-scope
	// predicate filtering over a shortlist never queries the database. Loaded
	// at boot, kept current by the embedding observer (IndexAdd).
	threadMu    sync.RWMutex
	threadByMsg map[string]string

	// examined counts candidates the NearestChunks widen has filtered — a
	// deterministic work signal the invariance guard reads to prove THREAD-scope
	// retrieval work does not grow as other threads accumulate.
	examined atomic.Int64
}

// NewChunkOracle ties the oracle to a specific embedder model and builds the
// ANN index over every vector already stored under that model. The model
// string lives in the cache keys, so switching embedders means constructing
// a new oracle (old rows stay under their original model_id; the new oracle
// reads empty and refills). Building the index scans the stored vectors
// once — the same O(N) boot work the substrate already does hydrating the
// DAG; per-query retrieval is what becomes invariant.
func NewChunkOracle(db *storage.DB, embedder core.Embedder, model string) (*ChunkOracle, error) {
	o := &ChunkOracle{
		db:          db,
		embedder:    embedder,
		model:       model,
		index:       annindex.New(annindex.Config{}),
		threadByMsg: make(map[string]string),
	}
	if db != nil {
		// Thread map first: the index build routes each vector into its thread
		// partition, so the thread must be resolvable before Add.
		threads, err := db.MessageThreads()
		if err != nil {
			return nil, fmt.Errorf("NewChunkOracle: load thread map: %w", err)
		}
		o.threadByMsg = threads // boot is single-threaded; the observer is wired after
		embs, err := db.AllChunkEmbeddingsForModel(model)
		if err != nil {
			return nil, fmt.Errorf("NewChunkOracle: build ANN index: %w", err)
		}
		for _, e := range embs {
			o.index.Add(chunkKeyStr(e.MessageID, e.ChunkIndex), threads[e.MessageID], e.Vector)
		}
	}
	return o, nil
}

// IndexAdd adds a freshly-embedded chunk vector to the ANN index and records
// its message's thread in the RAM predicate map. Wired as storage's embedding
// observer at boot (SetEmbeddingObserver), so every insert path keeps both
// current without the writers knowing they exist. Index add is idempotent.
// Embeddings written under a different model are ignored — the index is
// per-model and mixing widths/models would corrupt distances.
func (o *ChunkOracle) IndexAdd(messageID string, chunkIndex int, modelID, threadID string, vec []float32) {
	if o == nil || modelID != o.model {
		return
	}
	// Set the thread before the graph insert: the residual filter reads
	// threadByMsg[mid], so a searchable chunk must already have its thread.
	o.threadMu.Lock()
	o.threadByMsg[messageID] = threadID
	o.threadMu.Unlock()
	if o.index != nil {
		o.index.Add(chunkKeyStr(messageID, chunkIndex), threadID, vec)
	}
}

// Index returns the shared in-RAM ANN index. The search consumer
// (service/search) queries the same global graph through this accessor
// instead of a separate brute-force vec0 scan, so both retrieval paths ride
// one index kept current by the same embedding observer.
func (o *ChunkOracle) Index() *annindex.Index {
	if o == nil {
		return nil
	}
	return o.index
}

// chunkKeyStr / messageIDFromKey / chunkIdxFromKey delegate to the shared
// chunkkey encoding so the oracle (writer) and service/search (reader) agree.
func chunkKeyStr(messageID string, chunkIndex int) string {
	return chunkkey.Make(messageID, chunkIndex)
}

func messageIDFromKey(key string) string {
	mid, _ := chunkkey.Split(key)
	return mid
}

// ChunksForMessages loads chunk text for the given message IDs, ordered by
// chunk_index, from the chunks table (indexed by message_id — bounded, not a
// vec0 scan). Vectors are left nil: the caller (the provenance-reach scorer
// path) ranks by text, and anything that needs a vector gets it from the ANN
// index or EnsureVector. This keeps the reach path off the O(N) vec0 scan.
func (o *ChunkOracle) ChunksForMessages(_ context.Context, messageIDs []string) (map[string][]rrc.ChunkRef, error) {
	if o == nil || o.db == nil || len(messageIDs) == 0 {
		return nil, nil
	}
	chunks, err := o.db.GetChunksForMessages(messageIDs)
	if err != nil {
		return nil, err
	}

	out := make(map[string][]rrc.ChunkRef, len(chunks))
	for msgID, cs := range chunks {
		refs := make([]rrc.ChunkRef, len(cs))
		for i, c := range cs {
			refs[i] = rrc.ChunkRef{
				MessageID:  c.MessageID,
				ChunkIndex: c.ChunkIndex,
				Text:       c.Text,
			}
		}
		out[msgID] = refs
	}
	return out, nil
}

// annOverfetch is the shortlist multiple: the ANN index gathers k*annOverfetch
// Hamming-nearest candidates and reranks them by int8 asymmetric score. On the
// real Qwen3 embedder x8 reaches the int8 ceiling (~0.95 recall@10, measured in
// service/rrcbench); wider buys nothing.
const annOverfetch = 8

// NearestChunks embeds queryText and retrieves the k nearest chunks from the
// in-RAM ANN index (service/annindex): a Hamming-navigated graph plus int8
// asymmetric rerank, sub-linear and never reading a vector off disk. Routing
// applies the scope structurally (THREAD -> the thread's partition, ALL -> the
// global graph); the residual predicate is the local-context exclusion set,
// which the shortlist widens past if it eats into k. Returns the top-k in
// asymmetric order.
func (o *ChunkOracle) NearestChunks(ctx context.Context, queryText string, k int, predicate rrc.Predicate) ([]rrc.ChunkRef, error) {
	if o == nil || o.db == nil || o.embedder == nil {
		return nil, nil
	}
	if k <= 0 || queryText == "" {
		return nil, nil
	}

	qVecs, err := o.embedder.Embed(ctx, core.RoleQuery, []string{queryText})
	if err != nil {
		return nil, fmt.Errorf("NearestChunks: embed query: %w", err)
	}
	if len(qVecs) == 0 || len(qVecs[0]) == 0 {
		return nil, nil
	}
	qVec := qVecs[0]
	qNorm := l2norm(qVec)

	// Route to the thread's partition when the predicate pins one thread; else
	// search the global graph. The predicate is still applied in full below, so
	// routing only changes which graph is walked, never the result set.
	searchThread := ""
	if t, ok := rrc.ThreadScopeOf(predicate); ok && t != "" {
		searchThread = t
	}
	// Compile the predicate once (builds the exclusion-id set for O(1) lookup)
	// and reuse the constant metadata map, so the per-candidate filter — run for
	// every candidate on every widen iteration — does no rescanning or allocation.
	match := rrc.CompilePredicate(predicate)
	meta := map[string]string{"model_id": o.model}

	// Over-fetch a shortlist, filter by the residual predicate, and collapse it
	// to one candidate per message — its highest-scoring chunk. k means k
	// distinct candidate MESSAGES: the engine scores message-granular (bestScore
	// keyed by message id), so returning raw chunks lets a multi-chunk message
	// eat several slots and crowd out real candidates — measured at ~27 distinct
	// messages from a top-64 chunk shortlist on real data, 58% of the budget
	// lost. Widen until k distinct messages survive or the searched population is
	// exhausted (len(cands) < m); a large local-context exclusion sits at the top
	// of the shortlist, so the widen fetches past it. Bounded by the exclusion
	// set and the crowding, not the corpus — a doubling or two, no magic cap.
	var picked []annindex.Candidate
	for m := k * annOverfetch; ; m *= 2 {
		cands, _ := o.index.Search(qVec, m, searchThread) // asymmetric-reranked, nearest first
		if len(cands) == 0 {
			return nil, nil
		}
		o.examined.Add(int64(len(cands)))
		picked = picked[:0]
		seen := make(map[string]bool, k)
		o.threadMu.RLock()
		for _, c := range cands {
			mid := messageIDFromKey(c.Key)
			if seen[mid] {
				continue // already kept this message's best (higher-scoring) chunk
			}
			if !match(rrc.CandidateAttrs{MessageID: mid, ThreadID: o.threadByMsg[mid], Metadata: meta}) {
				continue
			}
			seen[mid] = true
			picked = append(picked, c)
		}
		o.threadMu.RUnlock()
		if len(picked) >= k || len(cands) < m {
			break
		}
	}
	if len(picked) > k {
		picked = picked[:k]
	}
	if len(picked) == 0 {
		return nil, nil
	}

	texts, err := o.chunkTextsForMessages(uniqueMessageIDs(picked))
	if err != nil {
		return nil, fmt.Errorf("NearestChunks: fetch chunk texts: %w", err)
	}
	out := make([]rrc.ChunkRef, len(picked))
	o.threadMu.RLock()
	for i, c := range picked {
		mid, cidx := messageIDFromKey(c.Key), chunkIdxFromKey(c.Key)
		out[i] = rrc.ChunkRef{
			MessageID:      mid,
			ChunkIndex:     cidx,
			ThreadID:       o.threadByMsg[mid],
			Text:           texts[chunkKey{mid, cidx}],
			RetrievalScore: normalizedScore(c.Score, qNorm),
		}
	}
	o.threadMu.RUnlock()
	return out, nil
}

// uniqueMessageIDs collapses shortlist chunk keys to their distinct message ids.
func uniqueMessageIDs(cands []annindex.Candidate) []string {
	seen := make(map[string]bool, len(cands))
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		if mid := messageIDFromKey(c.Key); !seen[mid] {
			seen[mid] = true
			out = append(out, mid)
		}
	}
	return out
}

// chunkTextsForMessages loads chunk text for the winning messages keyed by
// (message_id, chunk_index) — a bounded, indexed read of the chunks table,
// never a vec0 scan.
func (o *ChunkOracle) chunkTextsForMessages(messageIDs []string) (map[chunkKey]string, error) {
	chunks, err := o.db.GetChunksForMessages(messageIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[chunkKey]string)
	for _, cs := range chunks {
		for _, c := range cs {
			out[chunkKey{c.MessageID, c.ChunkIndex}] = c.Text
		}
	}
	return out, nil
}

func chunkIdxFromKey(key string) int {
	_, idx := chunkkey.Split(key)
	return idx
}

// normalizedScore maps a raw asymmetric score to a [0,1] cosine-like prior for
// RetrievalScore. The asymmetric score is q·dequant(v); dividing by |q| yields
// ~cosine for the normalized embeddings this store holds. Ranking is unchanged
// (|q| is constant across a query's candidates); this only shapes the value
// downstream reads when no reranker is configured.
func normalizedScore(raw, qNorm float64) float64 {
	if qNorm == 0 {
		return 0
	}
	s := raw / qNorm
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

func l2norm(v []float32) float64 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	return math.Sqrt(n)
}

type chunkKey struct {
	MessageID  string
	ChunkIndex int
}

// RepresentativeVectors returns one comparable vector per message —
// mean-pooled chunk vectors — for the engine's MMR diversity loop. Chunk
// indices come from the chunks table (indexed by message_id, so bounded, not
// a vec0 scan); the vectors come from the in-RAM ANN index (int8, dequantized).
// It never reads a vector off disk: the same principle as retrieval. int8 is
// fine here — MMR is an approximate diversity penalty, not exact scoring.
func (o *ChunkOracle) RepresentativeVectors(_ context.Context, messageIDs []string) (map[string][]float32, error) {
	if o == nil || o.db == nil || len(messageIDs) == 0 {
		return nil, nil
	}
	chunkMap, err := o.db.GetChunksForMessages(messageIDs)
	if err != nil {
		return nil, fmt.Errorf("RepresentativeVectors: load chunks: %w", err)
	}
	repVecs := make(map[string][]float32, len(messageIDs))
	for id, chunks := range chunkMap {
		var sumVec []float32
		var n int
		for _, c := range chunks {
			v, ok := o.index.Vector(chunkKeyStr(c.MessageID, c.ChunkIndex))
			if !ok {
				continue
			}
			if sumVec == nil {
				sumVec = make([]float32, len(v))
			}
			if len(v) != len(sumVec) {
				continue
			}
			for k, x := range v {
				sumVec[k] += x
			}
			n++
		}
		if n > 0 {
			for k := range sumVec {
				sumVec[k] /= float32(n)
			}
			repVecs[id] = sumVec
		}
	}
	return repVecs, nil
}

// EnsureVector embeds a chunk live if it isn't cached. Called by the
// engine when a selected candidate lacks a cached vector.
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
	vecs, err := o.embedder.Embed(ctx, core.RoleDocument, []string{ref.Text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, nil
	}
	vec := vecs[0]
	// InsertChunkEmbedding fires the embedding observer (thread + vector), which
	// is what keeps the ANN index and the RAM thread map current — no separate
	// IndexAdd here.
	if err := o.db.InsertChunkEmbedding(ref.MessageID, ref.ChunkIndex, o.model, vec); err != nil {
		log.Printf("ChunkOracle: InsertChunkEmbedding(%s[%d], %s): %v", ref.MessageID, ref.ChunkIndex, o.model, err)
	}
	return vec, nil
}
