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

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/service/storage"
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

// NearestChunks encodes queryText asymmetrically, then runs sqlite-vec
// KNN. It expands the raw result set until k predicate-eligible chunks
// survive or the model partition is exhausted.
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

	requestK := k
	var topRows []storage.ChunkVectorRow

	// model_id is a vec0 partition key. Thread and message exclusions
	// are applied after each KNN page, so ineligible rows cannot consume
	// the caller's effective k.
	for {
		rows, err := o.db.NearestChunkVectors(qVec, requestK, o.model, "", nil)
		if err != nil {
			return nil, fmt.Errorf("NearestChunks: vec0 KNN: %w", err)
		}
		topRows = topRows[:0]
		for _, row := range rows {
			if matchesPredicate(predicate, row) {
				topRows = append(topRows, row)
				if len(topRows) == k {
					break
				}
			}
		}
		if len(topRows) == k || len(rows) < requestK {
			break
		}
		requestK *= 2
	}
	if len(topRows) == 0 {
		return nil, nil
	}
	k = len(topRows)
	chunkTexts, err := o.fetchChunkTexts(topRows)
	if err != nil {
		return nil, fmt.Errorf("NearestChunks: fetch chunk texts: %w", err)
	}
	out := make([]rrc.ChunkRef, k)
	for i := 0; i < k; i++ {
		r := topRows[i]
		// Convert vec0 distance → similarity in [0, 1]. The schema
		// declares distance_metric=cosine, so the
		// returned Distance is a cosine distance and `1 - distance`
		// is the cosine similarity. Clamp out of caution against
		// floating-point drift just outside the unit interval.
		sim := 1.0 - r.Distance
		if sim < 0 {
			sim = 0
		}
		if sim > 1 {
			sim = 1
		}
		out[i] = rrc.ChunkRef{
			MessageID:      r.MessageID,
			ChunkIndex:     r.ChunkIndex,
			Text:           chunkTexts[chunkKey{r.MessageID, r.ChunkIndex}],
			Vector:         r.Vector,
			RetrievalScore: sim,
		}
	}
	return out, nil
}

// matchesPredicate delegates to the canonical in-memory evaluator in
// rrc so every oracle backend shares identical predicate semantics.
func matchesPredicate(predicate rrc.Predicate, row storage.ChunkVectorRow) bool {
	return rrc.EvalPredicate(predicate, rrc.CandidateAttrs{
		MessageID: row.MessageID,
		ThreadID:  row.ThreadID,
		Metadata:  map[string]string{"model_id": row.ModelID},
	})
}

type chunkKey struct {
	MessageID  string
	ChunkIndex int
}

func (o *ChunkOracle) fetchChunkTexts(rows []storage.ChunkVectorRow) (map[chunkKey]string, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	msgIDSet := make(map[string]struct{})
	for _, r := range rows {
		msgIDSet[r.MessageID] = struct{}{}
	}
	msgIDs := make([]string, 0, len(msgIDSet))
	for id := range msgIDSet {
		msgIDs = append(msgIDs, id)
	}
	chunks, err := o.db.GetChunksForMessages(msgIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[chunkKey]string, len(rows))
	for _, cs := range chunks {
		for _, c := range cs {
			out[chunkKey{c.MessageID, c.ChunkIndex}] = c.Text
		}
	}
	return out, nil
}

// RepresentativeVectors returns one comparable vector per message:
// mean-pooled chunk vectors under this oracle's single-vector embedding
// shape (Qwen3-Embedding et al.). Future multi-vector oracles substitute
// their own representation (sum-of-max etc.) at this seam. The MMR
// greedy loop itself is engine-owned (rrc.Engine.ApplyMMR); this method
// is the only backend-specific piece.
//
// Chunks without a cached vector are fetched live via EnsureVector —
// MMR needs every candidate to have a comparable vector, and a missing
// vector would silently exclude that candidate from the diversity
// penalty (it would compare as zero-similarity against everything,
// inflating its effective score).
func (o *ChunkOracle) RepresentativeVectors(ctx context.Context, messageIDs []string) (map[string][]float32, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}
	chunkMap, err := o.ChunksForMessages(ctx, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("RepresentativeVectors: load chunks: %w", err)
	}
	repVecs := make(map[string][]float32, len(messageIDs))
	for _, id := range messageIDs {
		chunks := chunkMap[id]
		if len(chunks) == 0 {
			continue
		}
		var sumVec []float32
		var n int
		for i := range chunks {
			v := chunks[i].Vector
			if v == nil {
				live, verr := o.EnsureVector(ctx, chunks[i])
				if verr != nil || live == nil {
					continue
				}
				v = live
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
	vec := vecs[0]
	if err := o.db.InsertChunkEmbedding(ref.MessageID, ref.ChunkIndex, o.model, vec); err != nil {
		log.Printf("ChunkOracle: InsertChunkEmbedding(%s[%d], %s): %v", ref.MessageID, ref.ChunkIndex, o.model, err)
	}
	return vec, nil
}
