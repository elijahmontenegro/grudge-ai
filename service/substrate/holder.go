package substrate

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
	"github.com/elijahmontenegro/grudge/rrc/calibrate/seed"
	"github.com/elijahmontenegro/grudge/rrc/calibrate/seedfit"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
	"github.com/elijahmontenegro/grudge/service/config"
	"github.com/elijahmontenegro/grudge/service/datadir"
	"github.com/elijahmontenegro/grudge/service/search"
	"github.com/elijahmontenegro/grudge/service/storage"
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
	cfg       *config.Config
	db        *storage.DB
	estimator chunk.TokenEstimator
	onReload  func()

	current atomic.Pointer[Substrate]
	embeds  atomic.Pointer[search.EmbedQueue]

	// calibrating is true while a background seed-set fit is in flight —
	// see maybeCalibrate. Guards fit-stacking across rapid reloads.
	calibrating atomic.Bool

	mu sync.Mutex
}

// NewHolder constructs an empty Holder. Bootstrap must be called
// before any read of Engine / EmbedQueue / Main / Scorer / Searcher.
//
// estimator is the token estimator every substrate this holder builds
// runs on (chunking, budget sizing) — a construction dependency, not a
// setting. onReload is invoked after each successful ReloadProviders /
// UpdateEngineConfig swap; pass nil if you don't need the hook.
func NewHolder(cfg *config.Config, db *storage.DB, estimator chunk.TokenEstimator, onReload func()) *Holder {
	return &Holder{cfg: cfg, db: db, estimator: estimator, onReload: onReload}
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

// Main returns the currently-active main completer (the user's LLM
// for reasoning, tool use, responses).
func (h *Holder) Main() core.Completer {
	s := h.current.Load()
	if s == nil {
		return nil
	}
	return s.MainCompleter
}

// Scorer returns the currently-active scorer (cross-encoder
// reranker). Nil if no scorer is configured.
func (h *Holder) Scorer() core.Scorer {
	s := h.current.Load()
	if s == nil {
		return nil
	}
	return s.Scorer
}

// Searcher returns the currently-active full-text + vector searcher.
// Nil if no embedder is configured.
func (h *Holder) Searcher() *search.Searcher {
	s := h.current.Load()
	if s == nil {
		return nil
	}
	return s.Searcher
}

// EmbedQueue returns the bounded fan-out pool for post-insert
// embedding work. Nil when no embedder is configured or after a
// reload cleared it. Hot callers tolerate nil when embedding is not
// configured; a configured runtime creates the queue before serving.
func (h *Holder) EmbedQueue() *search.EmbedQueue { return h.embeds.Load() }

// Enqueue routes a message id into the bounded embed queue. Tolerates
// a nil queue (embedder not configured / settings reload cleared it)
// — silent no-op so a slow or down embedder doesn't block message
// inserts. Satisfies runtime.EmbedEnqueuer.
func (h *Holder) Enqueue(messageID string) {
	if q := h.embeds.Load(); q != nil {
		q.Enqueue(messageID)
	}
}

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
// only edits that don't touch provider URLs / models (loss-ratio
// stance, MMR lambda, Local Context size).
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
	h.cfg.Settings.Engine = config.EngineConfigFromRRC(ec)
	if err := h.buildAndSwap(ctx, opts...); err != nil {
		h.cfg.Settings.Engine = old
		return err
	}
	return nil
}

// MutateSettings applies a settings mutation, persists it, and rebuilds
// the substrate — all under the holder's lock. This is the ONLY safe
// way to write cfg.Settings after boot: background calibration stages
// re-enter Build at arbitrary moments (up to their context lifetime
// after the triggering reload) and read cfg.Settings under h.mu, so an
// unlocked writer is a data race against them.
func (h *Holder) MutateSettings(ctx context.Context, mutate func(*config.Settings) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := mutate(&h.cfg.Settings); err != nil {
		return err
	}
	if err := h.cfg.Save(); err != nil {
		return err
	}
	return h.buildAndSwap(ctx)
}

// buildAndSwap is the inner reload sequence. Caller holds h.mu.
// On success: stores the new Substrate atomically, rotates the
// embed queue, fires onReload. On error: leaves prior state intact.
func (h *Holder) buildAndSwap(ctx context.Context, opts ...Option) error {
	subs, err := Build(ctx, h.cfg, h.db, append([]Option{WithTokenEstimator(h.estimator)}, opts...)...)
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

	// Self-calibration: if this substrate has a scorer but no fitted
	// calibrator for it (Build fell back to the bootstrap), fit one in the
	// background from the embedded seed set and swap it in live. Same
	// self-service posture as the embedding backfill — the user configures
	// a scorer; calibration is the system's job, not a command to run.
	h.maybeCalibrate(subs)
	return nil
}

// maybeCalibrate launches a background seed-set fit when the freshly-swapped
// substrate is running on the bootstrap calibrator. Caller holds h.mu (it is
// invoked from buildAndSwap), so reading cfg/subs here is race-free; the
// goroutine itself touches only immutable copies and re-enters the holder
// through ReloadProviders.
func (h *Holder) maybeCalibrate(subs *Substrate) {
	if subs.Scorer == nil || subs.RerankerModelID == "" {
		return // nothing to calibrate against
	}
	if !h.calibrating.CompareAndSwap(false, true) {
		return // a calibration stage is already in flight
	}

	scorer := subs.Scorer
	scorerModelID := subs.RerankerModelID
	calPath := datadir.CalibratorPath(h.cfg.DataDir)
	// The live calibrator: the bootstrap when no artifact loaded, the
	// persisted fit otherwise. Seed fits carry its structural-lift
	// ratio forward (see seedfit.Fit); the health check judges it
	// against the live scorer.
	prior := subs.Engine.Config().Calibrator
	fitted := subs.CalibratorFitted

	go func() {
		defer func() {
			h.calibrating.Store(false)
			// Scorer-swap staleness: if the user swapped scorers while
			// this stage ran, the artifact we produced is for the OLD
			// scorer and the new one is sitting on the bootstrap with
			// nothing scheduled. Re-evaluate once against the current
			// substrate; convergent because it only fires on identity
			// change.
			if cur := h.current.Load(); cur != nil && cur.RerankerModelID != scorerModelID {
				h.maybeCalibrate(cur)
			}
		}()

		// Independent context: the caller's reload ctx ends with the
		// request that triggered it, but calibration is a background job
		// that should survive it. Bounded so a wedged scorer or judge
		// can't leak the goroutine forever (the mass replay's judge
		// calls dominate the budget).
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		// Stage 1 — cold start: no fitted artifact for this scorer.
		if !fitted {
			log.Printf("[Calibrate] no fitted calibrator for scorer=%s — fitting from seed set in background", scorerModelID)
			var cal calibrate.Calibrator
			var res *seedfit.Result
			var err error
			// Transport failures get a bounded retry: the ordinary boot
			// race is spidey up before the scorer container finishes
			// warming, and nothing external schedules another reload.
			// Validity refusals (ErrInvalid) do NOT retry — a collapsed
			// scorer stays collapsed for the next 30 seconds too.
			for attempt := 1; ; attempt++ {
				cal, res, err = seedfit.EnsureFitted(ctx, scorer, scorerModelID, calPath, seed.Pairs(), prior)
				if err == nil || errors.Is(err, calibrate.ErrInvalid) || attempt >= 5 {
					break
				}
				log.Printf("[Calibrate] seed fit attempt %d failed (retrying in 30s): %v", attempt, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(30 * time.Second):
				}
			}
			if err != nil {
				// Fail loudly, stay on bootstrap. The next reload retries.
				// The fit's validity gate lands here too: a collapsed
				// scorer cannot persist an artifact.
				log.Printf("[Calibrate] seed fit failed (staying on bootstrap): %v", err)
				return
			}
			if res != nil {
				log.Printf("[Calibrate] fitted %s over %d samples (%d pos/%d neg, log-loss %.4f): A=%.3f B=%.3f C=%.3f",
					scorerModelID, res.Samples, res.Positives, res.Negatives, res.LogLoss,
					cal.A, cal.B, cal.C)
			}

			// Swap in via the normal rebuild: Build loads the artifact we
			// just wrote. A concurrent scorer swap is handled by the
			// deferred identity re-check above — the nested reload's own
			// maybeCalibrate is CAS-suppressed while this goroutine runs,
			// so the re-check is the mechanism, not the reload.
			if err := h.ReloadProviders(ctx); err != nil {
				log.Printf("[Calibrate] live swap failed (fit persisted; applies on next boot): %v", err)
			}
			return
		}

		// Stage 2 — health: the artifact fits ONCE per scorer-id, but the
		// scorer behind an unchanged id can drift (a vLLM upgrade shifting
		// the chat template). Re-verify the persisted calibrator against
		// the LIVE scorer on the deterministic seed subsample, judged by
		// the same absolute validity predicate every fit passes through.
		if err := seedfit.Health(ctx, scorer, seed.Pairs(), prior); err != nil {
			if !errors.Is(err, calibrate.ErrInvalid) {
				// Could not check (scorer unreachable, transport failure):
				// skip — never refit on a question that wasn't answered.
				log.Printf("[Calibrate] scorer health check skipped: %v", err)
				return
			}
			// The pairing is broken NOW. Refit against the live scorer —
			// the fit's own validity gate means a still-broken scorer
			// refuses to produce an artifact, so the persisted one is
			// never overwritten by garbage.
			log.Printf("[Calibrate] scorer health check FAILED for %s — refitting: %v", scorerModelID, err)
			res, ferr := seedfit.Fit(ctx, scorer, seed.Pairs(), prior)
			if ferr != nil {
				log.Printf("[Calibrate] refit refused (scorer still broken; keeping persisted artifact): %v", ferr)
				return
			}
			if err := calibrate.Save(calPath, calibrate.Artifact{
				Calibrator:    res.Calibrator,
				ScorerModelID: scorerModelID,
				Samples:       res.Samples,
				LogLoss:       res.LogLoss,
			}); err != nil {
				log.Printf("[Calibrate] refit persist failed: %v", err)
				return
			}
			log.Printf("[Calibrate] refitted %s over %d samples (log-loss %.4f): A=%.3f B=%.3f C=%.3f",
				scorerModelID, res.Samples, res.LogLoss, res.Calibrator.A, res.Calibrator.B, res.Calibrator.C)
			if err := h.ReloadProviders(ctx); err != nil {
				log.Printf("[Calibrate] live swap failed (refit persisted; applies on next boot): %v", err)
			}
			return
		}

	}()
}
