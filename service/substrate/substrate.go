// Package substrate constructs the providers + engine + storage
// triple the rest of the service depends on. Build returns an
// immutable Substrate value; Holder wraps Build + an atomic.Pointer
// + ReloadProviders / UpdateEngineConfig so settings changes can
// swap the live substrate in place without restart.
//
// What lives here: every provider construction, engine setup,
// score persister wiring, chunk oracle wiring, edge/score hydration
// — the things that turn a config snapshot plus an open DB into a
// live RRC substrate. What doesn't: HTTP, GraphQL, signal handling,
// MCP/skills/hooks/assembler (those are runner-side concerns the
// composition root assembles around the substrate).
package substrate

import (
	"context"
	"fmt"
	"log"
	"math"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
	"github.com/elijahmontenegro/grudge/service/config"
	"github.com/elijahmontenegro/grudge/service/oracle"
	"github.com/elijahmontenegro/grudge/service/search"
	"github.com/elijahmontenegro/grudge/service/storage"
)

// Substrate is the wired-up runtime substrate consumers receive
// from Build. Every field is non-nil unless the corresponding
// config block is absent (e.g. Scorer == nil when no
// scorer provider is configured).
type Substrate struct {
	DB     *storage.DB
	Engine *rrc.Engine
	Config *config.Config

	MainCompleter core.Completer
	Scorer        core.Scorer
	Embedder      core.Embedder

	Searcher    *search.Searcher
	ChunkOracle rrc.ChunkOracle

	// Model IDs identify the provider+model under which scores and
	// embeddings are persisted. Empty when the corresponding
	// provider isn't configured. Switching a model in settings
	// rotates the key; old rows stay tagged under their original
	// id, harmless.
	RerankerModelID string
	EmbedModelID    string
}

// Option configures Build at the seams that aren't expressible
// through the config alone. Production callers pass nothing — every
// substrate component constructs from cfg.Settings.Providers via
// the registered adapters. Tests inject fakes (gated scorer,
// stub oracle) to drive Engine.Assemble through real code paths
// without standing up a TEI server.
type Option func(*options)

type options struct {
	scorer      core.Scorer
	chunkOracle rrc.ChunkOracle
	estimator   chunk.TokenEstimator
}

// WithTokenEstimator supplies the token estimator the engine config
// carries (chunking, budget sizing, wire estimation). Required: Build
// fails without one — a substrate with no token units cannot enforce
// any budget contract.
func WithTokenEstimator(e chunk.TokenEstimator) Option {
	return func(o *options) { o.estimator = e }
}

// WithScorer overrides the scorer substrate would otherwise
// build from the "scorer" provider in cfg. Production callers
// pass nothing; tests inject a fake Scorer to drive the engine
// without standing up a TEI server.
func WithScorer(s core.Scorer) Option {
	return func(o *options) { o.scorer = s }
}

// WithChunkOracle overrides the chunk oracle substrate would
// otherwise build from the embedder provider. Same semantics as
// WithScorer — production callers pass nothing; tests inject a
// stub that returns canned chunks/vectors.
func WithChunkOracle(co rrc.ChunkOracle) Option {
	return func(o *options) { o.chunkOracle = co }
}

// Build wires the substrate from a loaded config and an open DB.
// Returns a Substrate ready for the Holder to install.
//
// Build is intentionally tolerant of an unconfigured config block
// (first run): the corresponding Substrate fields stay nil, and the
// consumer is expected to render a not-yet-configured UX rather
// than crash. Scorer == nil || MainCompleter == nil produces a
// warning here so boot logs surface the situation early.
//
// Options override fields that would otherwise come from cfg —
// see WithScorer / WithChunkOracle.
func Build(ctx context.Context, cfg *config.Config, db *storage.DB, opts ...Option) (*Substrate, error) {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	s := &Substrate{DB: db, Config: cfg}

	if mainCfg, ok := cfg.Settings.Providers["main"]; ok && mainCfg.Adapter != "" {
		p, err := core.NewProvider(mainCfg.ToCore())
		if err != nil {
			return nil, fmt.Errorf("main provider: %w", err)
		}
		cp, ok := p.(core.CompleterProvider)
		if !ok {
			return nil, fmt.Errorf("%w: adapter %q is not a CompleterProvider", core.ErrUnsupported, mainCfg.Adapter)
		}
		c, err := cp.Completer(mainCfg.Model)
		if err != nil {
			return nil, fmt.Errorf("main completer (%s/%s): %w", mainCfg.Adapter, mainCfg.Model, err)
		}
		s.MainCompleter = c
	}

	// Scorer (relevance scoring substrate). Cross-encoder reranker
	// via zerank/TEI in production; the override path lets tests inject
	// a fake. Constructed via the same core.NewProvider factory the
	// embedder uses below — uniform shape per provider role.
	if o.scorer != nil {
		s.Scorer = o.scorer
		log.Printf("Scorer: injected (test/override)")
	} else if scrCfg, ok := cfg.Settings.Providers["scorer"]; ok && scrCfg.Adapter != "" {
		p, err := core.NewProvider(scrCfg.ToCore())
		if err != nil {
			return nil, fmt.Errorf("scorer provider: %w", err)
		}
		sp, ok := p.(core.ScorerProvider)
		if !ok {
			return nil, fmt.Errorf("%w: adapter %q is not a ScorerProvider", core.ErrUnsupported, scrCfg.Adapter)
		}
		sc, err := sp.Scorer(scrCfg.Model)
		if err != nil {
			return nil, fmt.Errorf("scorer (%s/%s): %w", scrCfg.Adapter, scrCfg.Model, err)
		}
		s.Scorer = sc
		// The artifact-binding identity is the full endpoint identity, not
		// the bare model name: an empty Model (TEI-style endpoint-defined
		// scorers) must never wildcard-match a foreign artifact, and the
		// same model name behind a different endpoint is a different score
		// distribution.
		s.RerankerModelID = scrCfg.Adapter + "/" + scrCfg.Model + "@" + scrCfg.BaseURL
		log.Printf("Scorer: %s/%s @ %s", scrCfg.Adapter, scrCfg.Model, scrCfg.BaseURL)
	}

	// Embedder (cosine prefilter + semantic search).
	if embCfg, ok := cfg.Settings.Providers["embedder"]; ok && embCfg.Adapter != "" {
		p, err := core.NewProvider(embCfg.ToCore())
		if err != nil {
			return nil, fmt.Errorf("embedder provider: %w", err)
		}
		ep, ok := p.(core.EmbedderProvider)
		if !ok {
			return nil, fmt.Errorf("%w: adapter %q is not an EmbedderProvider", core.ErrUnsupported, embCfg.Adapter)
		}
		e, err := ep.Embedder(embCfg.Model)
		if err != nil {
			return nil, fmt.Errorf("embedder (%s/%s): %w", embCfg.Adapter, embCfg.Model, err)
		}
		s.Embedder = e
		s.EmbedModelID = embCfg.Model
	}

	// Self-adapting embedding dimension. Probe the configured embedder for
	// its native vector width and reconcile the vector cache to it before the
	// ChunkOracle / backfill are constructed (they must see the final table).
	// A dimension change drops and rebuilds chunk_vectors; the backfill
	// goroutine then re-embeds the corpus under the new dim. A transient
	// probe failure degrades to skip-rebuild rather than crashing boot.
	if s.Embedder != nil && s.EmbedModelID != "" {
		if vecs, err := s.Embedder.Embed(ctx, core.RoleDocument, []string{"probe"}); err == nil && len(vecs) > 0 && len(vecs[0]) > 0 {
			if err := db.EnsureEmbeddingDim(len(vecs[0]), s.EmbedModelID); err != nil {
				return nil, fmt.Errorf("ensure embedding dim: %w", err)
			}
		} else {
			log.Printf("WARNING: embedding dim probe failed, skipping vector-table reconcile: %v", err)
		}
	}

	if s.Scorer == nil || s.MainCompleter == nil {
		log.Printf("WARNING: providers not fully configured — configure at http://grudge.localhost:8420/settings")
	}

	// A completely absent Engine block uses canonical defaults. Once
	// present, the persisted snapshot is authoritative.
	rrcCfg := rrc.DefaultConfig()
	if o.estimator == nil {
		return nil, fmt.Errorf("substrate.Build: no token estimator (pass WithTokenEstimator)")
	}
	rrcCfg.Chunk.Estimator = o.estimator
	if se := cfg.Settings.Engine; se != (config.EngineConfig{}) {
		rrcCfg = se.ApplyTo(rrcCfg)
	}

	// Acceptance carries no fitted artifact and no persistent state:
	// every threshold is measured by the selection event about itself
	// (the rank-CFAR noise reference), interpreted within that event,
	// and discarded. The one hand-set value judgment is LossRatio.
	log.Printf("Acceptance: detection-theoretic (rank CFAR, R=%d ref/event, scorer=%s) | stance s0=-log2(1-LossRatio)=%.2f bits",
		rrc.ReferenceSampleSize, s.RerankerModelID, -math.Log2(1-rrcCfg.LossRatio))

	// Searcher + ChunkOracle share the same embedder and model id.
	// Construct before the engine so the oracle can flow in as an
	// engine option. WithChunkOracle override wins over the
	// embedder-derived default (used by tests that don't have a
	// real embedder service to point at).
	if o.chunkOracle != nil {
		s.ChunkOracle = o.chunkOracle
	} else if s.Embedder != nil && s.EmbedModelID != "" {
		oc, err := oracle.NewChunkOracle(db, s.Embedder, s.EmbedModelID)
		if err != nil {
			return nil, fmt.Errorf("build chunk oracle: %w", err)
		}
		s.ChunkOracle = oc
		// Every embedding-insert path (lazy EnsureVector, the post-insert
		// embed queue, the backfill) feeds the ANN index through this hook,
		// so the index stays current without those writers importing it.
		db.SetEmbeddingObserver(oc.IndexAdd)
		// Search shares the oracle's in-RAM index (same embedder + model) so it
		// queries the same global graph.
		s.Searcher = search.NewSearcher(s.Embedder, s.EmbedModelID, db, oc.Index())
	}

	// Engine constructed in one shot with everything wired:
	//   - hydrated DAG / score cache (loaded from DB)
	//   - chunk oracle
	//   - score persister (write-through to DB on every new chunk-pair score)
	// The engine has no public mutation surface — settings changes
	// rebuild it via Holder.UpdateEngineConfig / ReloadProviders.
	engineOpts := []rrc.Option{}
	if edges, err := db.AllEdges(); err == nil && len(edges) > 0 {
		engineOpts = append(engineOpts, rrc.WithLoadedEdges(edges))
		log.Printf("Loaded %d edges", len(edges))
	}
	if s.RerankerModelID != "" {
		if scores, err := db.LocalContextScoresForModel(s.RerankerModelID); err == nil && len(scores) > 0 {
			converted := make([]rrc.PersistedScore, 0, len(scores))
			for k, v := range scores {
				converted = append(converted, rrc.PersistedScore{
					LocalContextFingerprint: k.LocalContextFingerprint,
					LocalContextChunkIndex:  k.LocalContextChunkIndex,
					CandidateMsgID:          k.CandidateID,
					CandidateChunkIdx:       k.CandidateChunkIdx,
					Score:                   v,
				})
			}
			engineOpts = append(engineOpts, rrc.WithLoadedScores(converted))
			log.Printf("Loaded %d Local Context/candidate scores (model=%s)", len(scores), s.RerankerModelID)
		}
		modelID := s.RerankerModelID
		engineOpts = append(engineOpts, rrc.WithScorePersister(func(fingerprint string, localContextChunkIndex int, candidateID string, candidateChunkIndex int, score float64) {
			if err := db.InsertLocalContextScore(fingerprint, localContextChunkIndex, candidateID, candidateChunkIndex, modelID, score); err != nil {
				log.Printf("InsertLocalContextScore(%s[%d], %s[%d], %s): %v", fingerprint, localContextChunkIndex, candidateID, candidateChunkIndex, modelID, err)
			}
		}))
	}
	if s.ChunkOracle != nil {
		engineOpts = append(engineOpts, rrc.WithChunkOracle(s.ChunkOracle))
	}
	// Instrument identity: every formed edge's observations (raw sims,
	// contribution weights) are stamped with the scorer that measured
	// them — retroactively unrecoverable, so it rides config from day one.
	rrcCfg.ScorerModelID = s.RerankerModelID
	s.Engine = rrc.NewEngine(rrcCfg, s.Scorer, engineOpts...)

	return s, nil
}
