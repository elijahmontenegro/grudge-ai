// Package substrate constructs the providers + engine + storage
// triple that the runtime kernel depends on. Build returns a
// Substrate value the consumer hands to NewResolver (and, in the
// future, to a non-GraphQL kernel.Bootstrap).
//
// What lives here: every provider construction, engine setup,
// score persister wiring, chunk oracle wiring, edge/score hydration
// — the things that turn a config snapshot plus an open DB into a
// live RRC substrate. What doesn't: HTTP, GraphQL, signal handling,
// MCP/skills/hooks/assembler (those are runner-side concerns the
// kernel composes on top of the substrate).
//
// ReloadProviders in service/graph performs the in-place hot-swap
// equivalent for settings changes; it stays separate because its
// concurrent-safety properties are not the same as a fresh boot.
package substrate

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/adapter/tei"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/search"
	"github.com/emontenegr/spidey/service/storage"
)

// backfillChunks populates the chunks table for every message
// that has none. Cheap enough to block boot — no network calls,
// pure text-chunking in Go.
func backfillChunks(db *storage.DB, chunkCfg rrc.ChunkConfig) error {
	ids, err := db.MessagesWithoutChunks()
	if err != nil {
		return fmt.Errorf("MessagesWithoutChunks: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	log.Printf("Chunk backfill: processing %d pre-existing messages", len(ids))
	start := time.Now()
	var withChunks, withoutText int
	for _, id := range ids {
		msg, err := db.GetMessage(id)
		if err != nil || msg == nil {
			continue
		}
		text := rrc.TextFromBlocks(msg.Content)
		if text == "" {
			withoutText++
			continue
		}
		rcs := rrc.ChunkText(text, chunkCfg)
		if len(rcs) == 0 {
			withoutText++
			continue
		}
		schunks := make([]storage.Chunk, len(rcs))
		for i, c := range rcs {
			schunks[i] = storage.Chunk{
				ChunkIndex: c.Index,
				Text:       c.Text,
				ByteStart:  c.ByteStart,
				ByteEnd:    c.ByteEnd,
				TokenEst:   c.TokenEst,
			}
		}
		if err := db.InsertChunks(id, schunks); err != nil {
			log.Printf("Chunk backfill: InsertChunks(%s): %v", id, err)
			continue
		}
		withChunks++
	}
	log.Printf("Chunk backfill: chunked %d messages (%d text-only, %d empty) in %v",
		withChunks, withChunks, withoutText, time.Since(start))
	return nil
}

// Substrate is the wired-up runtime substrate consumers receive
// from Build. Every field is non-nil unless the corresponding
// config block is absent (e.g. classifier == nil when no
// classifier provider is configured).
type Substrate struct {
	DB     *storage.DB
	Engine *rrc.Engine
	Config *config.Config

	MainCompleter core.Completer
	Classifier    core.Classifier
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
// the registered adapters. Tests inject fakes (gated classifier,
// stub oracle) to drive Engine.Assemble through real code paths
// without standing up a TEI server.
type Option func(*options)

type options struct {
	classifier  core.Classifier
	chunkOracle rrc.ChunkOracle
}

// WithClassifier overrides the classifier substrate would otherwise
// build from the "classifier" / "embedder" providers in cfg. The
// override wins unconditionally; cfg-derived URLs are ignored when
// it's set.
//
// The composite-classifier construction (NLI + embedding similarity
// from two URLs) lives inside tei. Tests that don't have those
// services wire a single Scorer here and exercise the rest of
// substrate as-is.
func WithClassifier(c core.Classifier) Option {
	return func(o *options) { o.classifier = c }
}

// WithChunkOracle overrides the chunk oracle substrate would
// otherwise build from the embedder provider. Same semantics as
// WithClassifier — production callers pass nothing; tests inject a
// stub that returns canned chunks/vectors.
func WithChunkOracle(co rrc.ChunkOracle) Option {
	return func(o *options) { o.chunkOracle = co }
}

// Build wires the substrate from a loaded config and an open DB.
// Returns a Substrate ready for the kernel to consume.
//
// Build is intentionally tolerant of an unconfigured config block
// (first run): the corresponding Substrate fields stay nil, and
// the consumer (resolver, kernel) is expected to render a
// not-yet-configured UX rather than crash. classifier == nil ||
// MainCompleter == nil produces a warning here so boot logs
// surface the situation early.
//
// Options override fields that would otherwise come from cfg —
// see WithClassifier / WithChunkOracle.
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
		c, err := p.Completer(mainCfg.Model)
		if err != nil {
			return nil, fmt.Errorf("main completer (%s/%s): %w", mainCfg.Adapter, mainCfg.Model, err)
		}
		s.MainCompleter = c
	}

	// Composite classifier: NLI + embedding similarity. classifier
	// config points to NLI; embedder config points to embedding.
	var nliURL, embedURL string
	if clsCfg, ok := cfg.Settings.Providers["classifier"]; ok {
		nliURL = clsCfg.BaseURL
		s.RerankerModelID = clsCfg.Model
	}
	if embCfg, ok := cfg.Settings.Providers["embedder"]; ok {
		embedURL = embCfg.BaseURL
		s.EmbedModelID = embCfg.Model
	}
	if o.classifier != nil {
		s.Classifier = o.classifier
		log.Printf("Classifier: injected (test/override)")
	} else if nliURL != "" || embedURL != "" {
		s.Classifier = tei.NewCompositeClassifier(nliURL, embedURL)
		log.Printf("Composite classifier: NLI=%q, embed=%q", nliURL, embedURL)
	}

	// Embedder for semantic search (separate from classifier).
	if embCfg, ok := cfg.Settings.Providers["embedder"]; ok {
		p, err := core.NewProvider(embCfg.ToCore())
		if err != nil {
			return nil, fmt.Errorf("embedder provider: %w", err)
		}
		e, err := p.Embedder(embCfg.Model)
		if err != nil {
			return nil, fmt.Errorf("embedder (%s/%s): %w", embCfg.Adapter, embCfg.Model, err)
		}
		s.Embedder = e
	}

	if s.Classifier == nil || s.MainCompleter == nil {
		log.Printf("WARNING: providers not fully configured — configure at http://spidey.localhost:8420/settings")
	}

	// RRC engine. Config precedence: zero Settings.Engine → DefaultConfig
	// (first run / pre-engine-block legacy config). Otherwise trust the
	// persisted snapshot verbatim — zero in an individual field is
	// intentional (WeightCE=0 → pure-temporal, ScoreFloor=0 → no
	// cutoff).
	rrcCfg := rrc.DefaultConfig()
	if se := cfg.Settings.Engine; se != (config.EngineConfig{}) {
		rrcCfg.EdgeThreshold = se.EdgeThreshold
		rrcCfg.ScoreFloor = se.ScoreFloor
		rrcCfg.WeightCE = se.WeightCE
		rrcCfg.WeightTemp = se.WeightTemp
		rrcCfg.ZScoreThreshold = se.ZScoreThreshold
		rrcCfg.MinBatchStdDev = se.MinBatchStdDev
		rrcCfg.RadiusSize = se.RadiusSize
		rrcCfg.RerankTopK = se.RerankTopK
		rrcCfg.ContextBudgetTokens = se.ContextBudgetTokens
		// Preserve DefaultConfig values for new tunables when the
		// persisted settings predate them.
		if se.DiversityLambda > 0 {
			rrcCfg.DiversityLambda = se.DiversityLambda
		}
		if se.BudgetHeadroomPct > 0 {
			rrcCfg.BudgetHeadroomPct = se.BudgetHeadroomPct
		}
		if se.PerMsgDelimiterTokens > 0 {
			rrcCfg.PerMsgDelimiterTokens = se.PerMsgDelimiterTokens
		}
		if se.NLIFusionWeight > 0 {
			rrcCfg.NLIFusionWeight = se.NLIFusionWeight
		}
		if se.MinPerThreadInTopK > 0 {
			rrcCfg.MinPerThreadInTopK = se.MinPerThreadInTopK
		}
	}
	if rrcCfg.Chunk != (rrc.ChunkConfig{}) {
		db.SetChunkConfig(rrcCfg.Chunk)
	}

	// Chunk backfill for pre-existing messages. Messages inserted
	// before chunk-table migration have no rows — they'd be
	// invisible to RRC scoring. Synchronous on startup because
	// embedding backfill below depends on chunks existing first;
	// pure text-chunking in Go, no network calls.
	if err := backfillChunks(db, rrcCfg.Chunk); err != nil {
		log.Printf("Chunk backfill: %v", err)
	}

	// Searcher + ChunkOracle share the same embedder and model id.
	// Construct before the engine so the oracle can flow in as an
	// engine option. WithChunkOracle override wins over the
	// embedder-derived default (used by tests that don't have a
	// real embedder service to point at).
	if o.chunkOracle != nil {
		s.ChunkOracle = o.chunkOracle
	} else if s.Embedder != nil && s.EmbedModelID != "" {
		s.Searcher = search.NewSearcher(s.Embedder, s.EmbedModelID, db)
		s.ChunkOracle = search.NewChunkOracle(db, s.Embedder, s.EmbedModelID)
	}

	// Engine constructed in one shot with everything wired:
	//   - hydrated DAG / score cache (loaded from DB)
	//   - chunk oracle
	//   - score persister (write-through to DB on every new chunk-pair score)
	// The engine has no public mutation surface — settings changes
	// rebuild it via kernel.UpdateEngineConfig / ReloadProviders.
	engineOpts := []rrc.Option{}
	if edges, err := db.AllEdges(); err == nil && len(edges) > 0 {
		engineOpts = append(engineOpts, rrc.WithLoadedEdges(edges))
		log.Printf("Loaded %d edges", len(edges))
	}
	if s.RerankerModelID != "" {
		if scores, err := db.ChunkScoresForModel(s.RerankerModelID); err == nil && len(scores) > 0 {
			converted := make([]rrc.PersistedScore, 0, len(scores))
			for k, v := range scores {
				converted = append(converted, rrc.PersistedScore{
					FromMsgID:    k.FromID,
					FromChunkIdx: k.FromIdx,
					ToMsgID:      k.ToID,
					ToChunkIdx:   k.ToIdx,
					Score:        v,
				})
			}
			engineOpts = append(engineOpts, rrc.WithLoadedScores(converted))
			log.Printf("Loaded %d chunk-pair scores (model=%s)", len(scores), s.RerankerModelID)
		}
		modelID := s.RerankerModelID
		engineOpts = append(engineOpts, rrc.WithScorePersister(func(fromID string, fromIdx int, toID string, toIdx int, score float64) {
			if err := db.InsertChunkScore(fromID, fromIdx, toID, toIdx, modelID, score); err != nil {
				log.Printf("InsertChunkScore(%s[%d], %s[%d], %s): %v", fromID, fromIdx, toID, toIdx, modelID, err)
			}
		}))
	}
	if s.ChunkOracle != nil {
		engineOpts = append(engineOpts, rrc.WithChunkOracle(s.ChunkOracle))
	}
	s.Engine = rrc.NewEngine(rrcCfg, s.Classifier, engineOpts...)

	// Backfill chunk embeddings in the background. Non-blocking —
	// service accepts requests immediately; OnMessage misses on
	// not-yet-backfilled chunks just embed live. Skip when the
	// oracle was injected (it may not implement the production
	// BackfillEmbeddings protocol).
	if real, ok := s.ChunkOracle.(*search.ChunkOracle); ok {
		go real.BackfillEmbeddings(context.Background())
	}

	return s, nil
}
