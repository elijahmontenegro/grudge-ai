package search

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"

	"github.com/elijahmontenegro/grudge/core"
	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/service/storage"
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

func matchesPredicate(predicate rrc.Predicate, row storage.ChunkVectorRow) bool {
	if predicate == nil {
		return true
	}
	switch p := predicate.(type) {
	case rrc.PredAll:
		return true
	case rrc.PredThread:
		return row.ThreadID == p.ThreadID
	case rrc.PredScope:
		return p.Scope == rrc.ScopeAll || row.ThreadID == p.CurrentThread
	case rrc.PredExcludeMessageIDs:
		for _, id := range p.MessageIDs {
			if row.MessageID == id {
				return false
			}
		}
		return true
	case rrc.PredAnd:
		for _, child := range p.Children {
			if !matchesPredicate(child, row) {
				return false
			}
		}
		return true
	case rrc.PredOr:
		for _, child := range p.Children {
			if matchesPredicate(child, row) {
				return true
			}
		}
		return false
	case rrc.PredNot:
		return !matchesPredicate(p.Inner, row)
	case rrc.PredHasMetadata:
		switch p.Key {
		case "thread_id":
			return row.ThreadID == p.Value
		case "model_id":
			return row.ModelID == p.Value
		default:
			return false
		}
	default:
		return false
	}
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
