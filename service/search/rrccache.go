package search

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/storage"
	"google.golang.org/protobuf/proto"
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

// NearestChunks encodes queryText as a query (asymmetric — the
// embedder applies its query-side prompt), then ranks every chunk in
// chunk_vectors that satisfies predicate by cosine similarity to the
// query vector and returns the top k.
//
// Backed by a brute-force scan over the predicate-filtered set today.
// The interface is the architectural seam: a future HNSW or sqlite-vec
// implementation swaps the scoring loop without changing callers.
// Sub-linear retrieval is the long-term invariance commitment; the
// brute-force path satisfies the API contract immediately, with
// O(filtered-set-size) cost.
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

	predClause, predArgs, err := compilePredicate(predicate)
	if err != nil {
		return nil, fmt.Errorf("NearestChunks: compile predicate: %w", err)
	}

	// Sub-linear KNN via sqlite-vec vec0 — auxiliary columns push the
	// model_id and predicate filters into the MATCH search rather than
	// post-filtering. Result is already ordered by distance ascending.
	rows, err := o.db.NearestChunkVectors(qVec, k, o.model, predClause, predArgs)
	if err != nil {
		return nil, fmt.Errorf("NearestChunks: vec0 KNN: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	topRows := rows
	if k > len(topRows) {
		k = len(topRows)
	}
	topRows = topRows[:k]
	chunkTexts, err := o.fetchChunkTexts(topRows)
	if err != nil {
		return nil, fmt.Errorf("NearestChunks: fetch chunk texts: %w", err)
	}
	out := make([]rrc.ChunkRef, k)
	for i := 0; i < k; i++ {
		r := topRows[i]
		// Convert vec0 distance → similarity in [0, 1]. The schema
		// declares distance_metric=cosine (post-migrationV5), so the
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


// DiversityRerank applies MMR (Carbonell & Goldstein 1998) to the
// candidate set using mean-pooled chunk vectors as the per-message
// representative. The storage-backed oracle's single-vector embedding
// shape (Qwen3-Embedding et al.) maps cleanly to cosine similarity;
// future multi-vector oracles substitute their own representation
// (sum-of-max etc.) at this seam.
//
// effective(C) = λ·orig(C) − (1−λ)·max_sim(C, kept). Greedy
// pick-max-effective across remaining candidates; the first pick is
// the highest-original-score message (anchor).
func (o *ChunkOracle) DiversityRerank(ctx context.Context, candidates []*pb.SelectedMessage, originalScores map[string]float64, lambda float64) ([]*pb.SelectedMessage, error) {
	if len(candidates) <= 1 {
		return candidates, nil
	}

	ids := make([]string, 0, len(candidates))
	for _, s := range candidates {
		ids = append(ids, s.MessageId)
	}
	chunkMap, err := o.ChunksForMessages(ctx, ids)
	if err != nil {
		return candidates, fmt.Errorf("DiversityRerank: load chunks: %w", err)
	}

	// Representative vector per candidate = mean-pool over its chunks'
	// vectors. Chunks without a cached vector are fetched live via
	// EnsureVector — MMR needs every candidate to have a comparable
	// vector, and a missing vector would silently exclude that
	// candidate from the diversity penalty (it'd compare as zero-
	// similarity against everything, inflating its effective score).
	repVecs := make(map[string][]float32, len(ids))
	for _, id := range ids {
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

	// Copy each SelectedMessage before rewriting EffectiveScore so
	// callers whose original slice escaped elsewhere (logging,
	// introspection publish, audit trails) don't observe a surprise
	// mutation. proto.Clone (not value-copy) because the proto type
	// embeds a MessageState containing a mutex — value-copying it
	// trips go vet's copylocks check and risks wedged state under
	// concurrent reflection access.
	remaining := make([]*pb.SelectedMessage, len(candidates))
	for i, s := range candidates {
		remaining[i] = proto.Clone(s).(*pb.SelectedMessage)
	}
	sort.SliceStable(remaining, func(i, j int) bool {
		return originalScores[remaining[i].MessageId] > originalScores[remaining[j].MessageId]
	})

	out := make([]*pb.SelectedMessage, 0, len(candidates))
	out = append(out, remaining[0])
	remaining = remaining[1:]

	for len(remaining) > 0 {
		bestIdx := -1
		bestScore := math.Inf(-1)
		for i, cand := range remaining {
			candVec := repVecs[cand.MessageId]
			var maxSim float64
			for _, kept := range out {
				if candVec == nil {
					continue
				}
				keptVec := repVecs[kept.MessageId]
				if keptVec == nil {
					continue
				}
				s := cosineSim(candVec, keptVec)
				if s > maxSim {
					maxSim = s
				}
			}
			effective := lambda*originalScores[cand.MessageId] - (1.0-lambda)*maxSim
			if effective > bestScore {
				bestScore = effective
				bestIdx = i
			}
		}
		pick := remaining[bestIdx]
		pick.EffectiveScore = float32(bestScore)
		out = append(out, pick)
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}
	return out, nil
}

// cosineSim is the standard cosine similarity; returns 0 for
// zero-length or mismatched vectors. Internal to the diversity rerank
// loop — the engine no longer computes vector math directly.
func cosineSim(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
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
			vecs, err := o.embedder.Embed(ctx, core.RoleDocument, texts)
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
		out, err := e.Embed(ctx, core.RoleDocument, []string{t})
		if err != nil {
			continue
		}
		vecs[i] = out[0]
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
