package substrate

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/search"
	"github.com/emontenegr/spidey/service/storage"
)

// Holder owns the atomic engine + embed-queue pointers and
// serializes ReloadProviders / UpdateEngineConfig. Hot readers go
// through Engine() / EmbedQueue() — both are lock-free atomic loads.
// Reload paths take the holder's mutex to ensure two concurrent
// settings saves can't interleave their substrate builds.
//
// onReload, if non-nil, is invoked under the reload lock after the
// new substrate has been swapped in. The consumer wires it to
// runners.StopAll so existing runners (which captured the old engine
// pointer at construction) get rebuilt on the next request.
type Holder struct {
	cfg      *config.Config
	db       *storage.DB
	onReload func()

	current atomic.Pointer[Substrate]
	embeds  atomic.Pointer[search.EmbedQueue]

	mu sync.Mutex
}

// NewHolder constructs an empty Holder. Bootstrap must be called
// before any read of Engine / EmbedQueue / Main / Scorer / Searcher.
//
// onReload is invoked after each successful ReloadProviders /
// UpdateEngineConfig swap; pass nil if you don't need the hook.
func NewHolder(cfg *config.Config, db *storage.DB, onReload func()) *Holder {
	return &Holder{cfg: cfg, db: db, onReload: onReload}
}

// Bootstrap builds the initial Substrate from cfg + db and stores
// it. After this returns successfully, Engine / Main / Scorer /
// Searcher / EmbedQueue all read live values.
func (h *Holder) Bootstrap(ctx context.Context, opts ...Option) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.buildAndSwap(ctx, opts...)
}

// Current returns the live Substrate snapshot. The pointer is stable
// for the duration of a single operation; a concurrent reload may
// produce a different pointer on the next call, but the current
// snapshot remains valid for in-flight reads.
func (h *Holder) Current() *Substrate { return h.current.Load() }

// Engine returns the currently-active RRC engine.
func (h *Holder) Engine() *rrc.Engine {
	s := h.current.Load()
	if s == nil {
		return nil
	}
	return s.Engine
}

// EmbedQueue returns the bounded fan-out pool for post-insert
// embedding work. Nil when no embedder is configured or after a
// reload cleared it. Hot callers (Inserter, OnMessageStored)
// tolerate nil — the startup backfill goroutine catches up later.
func (h *Holder) EmbedQueue() *search.EmbedQueue { return h.embeds.Load() }

// ReloadProviders rebuilds the substrate from the current config and
// atomically swaps it in. Existing runners (captured an old engine
// pointer at construction) are stopped via onReload; next request
// rebuilds them against the fresh substrate. The old embed queue is
// closed off the hot path so its workers drain rather than leak.
//
// Serialized by the holder's mutex so two simultaneous settings
// saves cannot interleave substrate builds and produce an engine
// pointing at half-fresh, half-stale providers.
func (h *Holder) ReloadProviders(ctx context.Context, opts ...Option) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.buildAndSwap(ctx, opts...)
}

// UpdateEngineConfig swaps in a fresh engine that reuses the current
// providers but with a different EngineConfig. Path for settings-
// only edits that don't touch provider URLs / models (threshold
// tweak, MMR lambda, radius size).
//
// Same swap semantics as ReloadProviders: writes the new config into
// cfg.Settings.Engine, rebuilds the substrate, atomic stores the
// new pointer, fires onReload. The config rollback on failure
// restores the prior Engine block so the holder's view of "what
// config produced the current substrate" stays consistent.
func (h *Holder) UpdateEngineConfig(ctx context.Context, ec rrc.EngineConfig, opts ...Option) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	old := h.cfg.Settings.Engine
	h.cfg.Settings.Engine = config.EngineConfig{
		EdgeThreshold:         ec.EdgeThreshold,
		ScoreFloor:            ec.ScoreFloor,
		ZScoreThreshold:       ec.ZScoreThreshold,
		MinBatchStdDev:        ec.MinBatchStdDev,
		RadiusSize:            ec.RadiusSize,
		RerankTopK:            ec.RerankTopK,
		ContextBudgetTokens:   ec.ContextBudgetTokens,
		DiversityLambda:       ec.DiversityLambda,
		BudgetHeadroomPct:     ec.BudgetHeadroomPct,
		PerMsgDelimiterTokens: ec.PerMsgDelimiterTokens,
	}
	if err := h.buildAndSwap(ctx, opts...); err != nil {
		h.cfg.Settings.Engine = old
		return err
	}
	return nil
}

// buildAndSwap is the inner reload sequence. Caller holds h.mu.
// On success: stores the new Substrate atomically, rotates the
// embed queue, fires onReload. On error: leaves prior state intact.
func (h *Holder) buildAndSwap(ctx context.Context, opts ...Option) error {
	subs, err := Build(ctx, h.cfg, h.db, opts...)
	if err != nil {
		return fmt.Errorf("rebuild substrate: %w", err)
	}

	h.current.Store(subs)

	var old *search.EmbedQueue
	if subs.Searcher != nil {
		// 4 workers / 64-job buffer / 2m timeout — see EmbedQueue
		// doc comment for the rationale (single-GPU TEI deadlock
		// observed 2026-04-23).
		old = h.embeds.Swap(search.NewEmbedQueue(subs.Searcher, 4, 64, 2*time.Minute))
	} else {
		old = h.embeds.Swap(nil)
	}
	if old != nil {
		go old.Close()
	}

	if h.onReload != nil {
		h.onReload()
	}
	return nil
}
