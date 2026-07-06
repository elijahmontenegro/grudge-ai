// Package rrcbench measures RRC's load-bearing promise — that per-step
// assembly compute is invariant under corpus growth — against the real
// substrate. It stands up a real SQLite DB, the real sqlite-vec (vec0)
// KNN, and the real rrc.Engine; only the embedder, scorer, and (absent)
// completer are synthetic, so the four suspected O(N)-per-step operations
// run exactly as they do in production:
//
//  1. corpus load     — DB.ThreadCorpus/AllCorpus: a per-message protobuf
//     deserialize over the full history, every tick
//     (adkbridge/rrcllm.go loads it fresh each call).
//  2. protocol index  — rrc.NewProtocolIndex over the whole corpus.
//  3. eligibility scan — the `for _, m := range corpus` loop in
//     SelectPrerequisites.
//  4. vector KNN       — DB.NearestChunkVectors -> vec0 MATCH. The storage
//     comments call this "sub-linear ANN"; sqlite-vec
//     v0.1.6 has no ANN index, so this harness measures
//     whether it is in fact a linear brute-force scan.
//
// Two axes are reported, deliberately kept apart:
//   - call COUNTS (scorer calls / candidate pairs) — deterministic; the
//     algorithmic-invariance guard. The scorer is the expensive per-step
//     operation, and its call count MUST stay flat as N grows. This is the
//     regression guard (TestInvariance_ScorerWorkStaysFlat).
//   - wall-clock per stage — noisy, informational; quantifies the O(N)
//     substrate leaks and settles the vec0 KNN complexity question
//     (TestInvariance_StageLatencyCurve).
//
// Only this package can be faithful: rrc must not import service (module
// boundary), so a harness wiring the real vec0 + storage + engine together
// has to live above them here in service/.
package rrcbench

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/elijahmontenegro/grudge/core"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/rrc/tiktoken"
	"github.com/elijahmontenegro/grudge/service/oracle"
	"github.com/elijahmontenegro/grudge/service/storage"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// benchModel is the embedder model id the planted vectors live under.
const benchModel = "bench-embed"

// benchDim must match storage's bootstrap embedding dimension — a fresh
// storage.Open() creates the vec0 table at defaultEmbeddingDim (1024) and
// nothing here rebuilds it. Inserting a vector of any other width fails
// loudly at the vec0 INSERT, which is the correct fail-fast signal.
const benchDim = 1024

// benchBaseTime seeds deterministic, strictly increasing message
// timestamps so ordering by created_at is stable across runs.
var benchBaseTime = time.Unix(1_700_000_000, 0).UTC()

// unitVector deterministically maps a seed string to a dim-dimensional
// unit vector: same seed, same vector (reproducible), yet spread across
// the sphere so vec0 KNN does real distance work instead of degenerating
// on identical vectors.
func unitVector(seed string, dim int) []float32 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	r := rand.New(rand.NewSource(int64(h.Sum64())))
	v := make([]float32, dim)
	var norm float64
	for i := range v {
		x := r.NormFloat64()
		v[i] = float32(x)
		norm += x * x
	}
	if norm == 0 {
		v[0] = 1
		return v
	}
	inv := float32(1.0 / math.Sqrt(norm))
	for i := range v {
		v[i] *= inv
	}
	return v
}

// benchText is a few deterministic sentences with the index woven in, so
// each message chunks non-trivially and its embedding (seeded by the text)
// lands at a distinct point in the vector space.
func benchText(i int) string {
	return fmt.Sprintf(
		"Message %d discusses topic %d and depends on the framing established earlier. "+
			"It refines point %d with detail %d and raises question %d for later resolution. "+
			"The reasoning in step %d follows from the prior turn about subject %d.",
		i, i%37, i%13, i%101, i%7, i, i%29)
}

// hashEmbedder is a deterministic, GPU-free core.Embedder. It maps text to
// the same unit vector unitVector would and counts how many texts it
// embeds — the per-Assemble query-embed count must not grow with N.
type hashEmbedder struct {
	dim   int
	calls *int64
}

func (h hashEmbedder) Embed(_ context.Context, _ core.EmbedRole, texts []string) ([][]float32, error) {
	atomic.AddInt64(h.calls, int64(len(texts)))
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = unitVector(t, h.dim)
	}
	return out, nil
}

// countingScorer is a deterministic, GPU-free rrc.Scorer. It records how
// many times it is called and how many candidate pairs it scores per
// Assemble — the load-bearing invariance signal. A real scorer is the
// expensive per-step cost; if these counts grow with N, the invariance
// promise is broken at the level that actually matters.
type countingScorer struct {
	calls *int64
	pairs *int64
}

func (s countingScorer) Score(_ context.Context, query string, candidates []string) ([]float64, error) {
	atomic.AddInt64(s.calls, 1)
	atomic.AddInt64(s.pairs, int64(len(candidates)))
	out := make([]float64, len(candidates))
	for i, c := range candidates {
		// Deterministic pseudo-similarity in [0,1] from the text pair, spread
		// enough that batches clear MinBatchStdDev and some edges form (so the
		// DAG grows and the Select walk has real work).
		h := fnv.New32a()
		_, _ = h.Write([]byte(query))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(c))
		out[i] = float64(h.Sum32()%1000) / 1000.0
	}
	return out, nil
}

// countText extracts a wire message's text for token counting — the
// projection Assemble's shed loop needs so the budget can bind.
func countText(m *llmv1.LLMMessage) string {
	var b []byte
	for _, blk := range m.Content {
		if t := blk.GetText(); t != nil {
			b = append(b, t.Text...)
		}
	}
	return string(b)
}

// corpusHandles bundles the real components under test with the per-tick
// inputs one Assemble needs, plus the synthetic stubs' call counters.
type corpusHandles struct {
	db       *storage.DB
	oracle   *oracle.ChunkOracle
	engine   *rrc.Engine
	threadID string

	corpus []*threadv1.Message // full corpus, as the bridge passes it to Assemble
	anchor *threadv1.Message   // the in-flight turn's final message
	local  []*threadv1.Message // Local Context window (ends at anchor)

	embedCalls  *int64
	scorerCalls *int64
	scorerPairs *int64
	dim         int
}

// buildCorpus plants an n-message thread into db — each message carrying a
// deterministic unit-vector embedding, one chunk, and provenance edges to a
// bounded set of priors plus a shared deep root — then wires the real
// ChunkOracle and rrc.Engine over it, loading the planted edges the way
// production loads them at boot. Only the embedder and scorer are synthetic.
func buildCorpus(db *storage.DB, threadID string, n, localWindow, dim int) (*corpusHandles, error) {
	if n <= 0 || localWindow <= 0 || localWindow > n {
		return nil, fmt.Errorf("buildCorpus: bad sizes n=%d localWindow=%d", n, localWindow)
	}
	if err := db.CreateThread(&threadv1.Thread{Id: threadID, CreatedAt: timestamppb.New(benchBaseTime)}); err != nil {
		return nil, fmt.Errorf("create thread: %w", err)
	}

	msgs := make([]*threadv1.Message, n)
	for i := range n {
		id := fmt.Sprintf("m%07d", i)
		text := benchText(i)
		role := threadv1.Role_ROLE_USER
		if i%2 == 1 {
			role = threadv1.Role_ROLE_ASSISTANT
		}
		// Plain text messages. NewProtocolIndex's cost is its O(N) scan over
		// every message and content block (leak 2), which runs whether or not
		// any block is a tool call; unpaired tool calls would only break exact
		// protocol closure without changing what this harness measures.
		content := []*threadv1.ContentBlock{
			{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: text}}},
		}
		msg := &threadv1.Message{
			Id: id, ThreadId: threadID, Role: role, Content: content,
			Position: int64(i), TurnId: fmt.Sprintf("turn-%07d", i/2),
			CreatedAt: timestamppb.New(benchBaseTime.Add(time.Duration(i) * time.Second)),
		}
		chunk := storage.Chunk{
			MessageID: id, ChunkIndex: 0, Text: text,
			ByteStart: 0, ByteEnd: len(text), TokenEst: len(text) / 4,
		}
		if err := db.InsertMessage(msg, []storage.Chunk{chunk}); err != nil {
			return nil, fmt.Errorf("insert message %s: %w", id, err)
		}
		if err := db.InsertChunkEmbedding(id, 0, benchModel, unitVector(text, dim)); err != nil {
			return nil, fmt.Errorf("insert embedding %s: %w", id, err)
		}
		msgs[i] = msg
	}

	// Provenance edges: contributor -> anchor (older -> newer), matching
	// RecordProvenance's direction. Each message draws from its two immediate
	// predecessors plus the shared deep root (message 0), so the backward mass
	// walk from any cone has bounded fan-in and one high-mass root — the shape
	// provenanceMassWalk is designed for.
	for i := 1; i < n; i++ {
		priors := []int{i - 1}
		if i-2 >= 0 {
			priors = append(priors, i-2)
		}
		if i > 4 {
			priors = append(priors, 0)
		}
		for _, p := range priors {
			edge := &rrcv1.Edge{
				FromMessageId: msgs[p].Id, ToMessageId: msgs[i].Id,
				Score: 0.9, Source: rrcv1.EdgeSource_EDGE_SOURCE_PROVENANCE,
				DetectedAt:   timestamppb.New(benchBaseTime.Add(time.Duration(i) * time.Second)),
				FromThreadId: threadID, ToThreadId: threadID,
			}
			if err := db.InsertEdge(edge); err != nil {
				return nil, fmt.Errorf("insert edge %s->%s: %w", msgs[p].Id, msgs[i].Id, err)
			}
		}
	}

	est, err := tiktoken.New()
	if err != nil {
		return nil, fmt.Errorf("tiktoken estimator: %w", err)
	}
	cfg := rrc.DefaultConfig()
	cfg.Chunk.Estimator = est

	var embedCalls, scorerCalls, scorerPairs int64
	embedder := hashEmbedder{dim: dim, calls: &embedCalls}
	o, err := oracle.NewChunkOracle(db, embedder, benchModel)
	if err != nil {
		return nil, fmt.Errorf("build chunk oracle: %w", err)
	}
	scorer := countingScorer{calls: &scorerCalls, pairs: &scorerPairs}

	edges, err := db.AllEdges()
	if err != nil {
		return nil, fmt.Errorf("load edges: %w", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := rrc.NewEngine(cfg, scorer,
		rrc.WithChunkOracle(o), rrc.WithLoadedEdges(edges), rrc.WithLogger(logger))

	return &corpusHandles{
		db: db, oracle: o, engine: eng, threadID: threadID,
		corpus: msgs, anchor: msgs[n-1], local: msgs[n-localWindow : n],
		embedCalls: &embedCalls, scorerCalls: &scorerCalls, scorerPairs: &scorerPairs, dim: dim,
	}, nil
}

// assembleOnce runs one production-shaped Assemble against the size-N
// corpus: detection (KNN + scorer + provenance reach), Select (DAG walk),
// MMR, and shed. SerializedLocalContext is left nil so Assemble builds it
// exactly as the bridge does when it has a fresh event.
func (h *corpusHandles) assembleOnce(ctx context.Context) (rrc.AssembleResult, error) {
	return h.engine.Assemble(ctx, rrc.AssembleRequest{
		Anchor:       h.anchor,
		LocalContext: h.local,
		Store:        h.db,
		Scope:        threadv1.SelectionScope_SELECTION_SCOPE_THREAD,
		ThreadID:     h.threadID,
		Budget:       40000,
		HeadroomPct:  0.9,
		PerMsgDelim:  5,
		CountText:    countText,
	})
}

// resetCounters zeroes the stub call counters so a measurement reflects one
// Assemble, not the cumulative total.
func (h *corpusHandles) resetCounters() {
	atomic.StoreInt64(h.embedCalls, 0)
	atomic.StoreInt64(h.scorerCalls, 0)
	atomic.StoreInt64(h.scorerPairs, 0)
}

// loadInt64 reads a counter set by the stubs.
func loadInt64(p *int64) int64 { return atomic.LoadInt64(p) }

// insertVectorCorpus plants n bare text messages, each with one chunk and
// one deterministic embedding, and nothing else — no provenance edges, no
// engine. It exists to isolate the vec0 KNN scan (leak 4) from every other
// stage, so its latency-vs-N slope answers the linear-vs-sub-linear
// question cleanly. Vectors live under benchModel, the KNN partition key.
func insertVectorCorpus(db *storage.DB, threadID string, n, dim int) error {
	if err := db.CreateThread(&threadv1.Thread{Id: threadID, CreatedAt: timestamppb.New(benchBaseTime)}); err != nil {
		return fmt.Errorf("create thread: %w", err)
	}
	for i := range n {
		id := fmt.Sprintf("v%07d", i)
		text := benchText(i)
		msg := &threadv1.Message{
			Id: id, ThreadId: threadID, Role: threadv1.Role_ROLE_USER,
			Content:   []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: text}}}},
			Position:  int64(i),
			CreatedAt: timestamppb.New(benchBaseTime.Add(time.Duration(i) * time.Second)),
		}
		if err := db.InsertMessage(msg, []storage.Chunk{{MessageID: id, ChunkIndex: 0, Text: text, ByteEnd: len(text)}}); err != nil {
			return fmt.Errorf("insert message %s: %w", id, err)
		}
		if err := db.InsertChunkEmbedding(id, 0, benchModel, unitVector(text, dim)); err != nil {
			return fmt.Errorf("insert embedding %s: %w", id, err)
		}
	}
	return nil
}
