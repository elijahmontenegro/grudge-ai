package graph

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/adapter/tei"
	"github.com/emontenegr/spidey/core/retry"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/hooks"
	"github.com/emontenegr/spidey/service/prompt"
	"github.com/emontenegr/spidey/service/runtime/pubsub"
	runtimerunner "github.com/emontenegr/spidey/service/runtime/runner"
	"github.com/emontenegr/spidey/service/search"
	"github.com/emontenegr/spidey/service/skills"
	"github.com/emontenegr/spidey/service/storage"
	"google.golang.org/adk/tool"
)

// Resolver is the root resolver, holding all service dependencies.
type Resolver struct {
	DB       *storage.DB
	Engine   *rrc.Engine
	Config   *config.Config
	Searcher *search.Searcher
	// embedQueue is an atomic.Pointer so ReloadProviders can swap it
	// during a settings change without racing against the hot
	// OnMessageStored / storeMessage readers on every message insert.
	// Use r.EmbedQueue() to read, r.setEmbedQueue() to write.
	embedQueue atomic.Pointer[search.EmbedQueue]
	Main       core.Completer // main model completer

	// Prompt assembler — composes system prompts from templates
	Assembler *prompt.Assembler

	// Hooks dispatcher — lifecycle event hooks (fail-closed)
	Hooks *hooks.Dispatcher

	// Loaded skills from user + project directories
	Skills []skills.Skill

	// MCP tools loaded from config
	MCPTools []tool.Tool

	// Plan content per thread (populated when agent exits plan mode)
	planContent   map[string]string
	planContentMu sync.RWMutex

	// Selection results for introspection (keyed by event ID)
	selectionResults map[string]*pb.SelectionResult
	// Latest selection event per thread
	latestSelection map[string]string // threadID -> eventID
	// Outbound citation count: how many times each message was selected as a prerequisite
	citationCount map[string]int // messageID -> count

	// Tool approval channels — keyed by callId
	pendingApprovals   map[string]chan bool
	pendingAnswers     map[string]chan string // callId -> text response for AskUserQuestion
	pendingThreadIDs   map[string]string     // callId -> threadID for denial feedback
	pendingApprovalsMu sync.Mutex

	// (Engine is goroutine-safe via its own internal RWMutex; the
	// resolver no longer owns or passes a shared lock.)

	// Active runners per thread — for autonomous/plan mode.
	// Lifecycle (insert / lookup / delete / iterate) lives on the
	// Registry; the resolver still owns the side-effects around
	// stop (subagent merge, pubsub) since they reach into other
	// kernel surfaces.
	runners *runtimerunner.Registry

	// Per-thread fan-out topics for UI subscriptions. Each is a
	// thin instance of pubsub.Topic / pubsub.Broadcast — the five
	// near-identical sub/pub maps that used to live here are now
	// one generic primitive parameterized per event shape.
	streams   *pubsub.Topic[*StreamEvent]
	agents    *pubsub.Topic[*AgentState]
	tools     *pubsub.Topic[*ToolExecution]
	subagents *pubsub.Topic[*SubagentProgress]
	threads   *pubsub.Broadcast[*ThreadStateEvent]
	mu        sync.RWMutex
}

// NewResolver creates a resolver with all dependencies.
//
// When a Searcher is supplied, we also construct an EmbedQueue with
// bounded concurrency. Every post-insert embed request routes through
// it — see the EmbedQueue doc comment for the failure mode this
// replaces (unbounded goroutine fan-out against a shared GPU, which
// deadlocked TEI's CUDA context on 2026-04-23).
func NewResolver(db *storage.DB, engine *rrc.Engine, cfg *config.Config, searcher *search.Searcher, main core.Completer) *Resolver {
	r := &Resolver{
		DB:               db,
		Engine:           engine,
		Config:           cfg,
		Searcher:         searcher,
		Main:             main,
		planContent:      make(map[string]string),
		selectionResults: make(map[string]*pb.SelectionResult),
		latestSelection:  make(map[string]string),
		citationCount:    make(map[string]int),
		pendingApprovals: make(map[string]chan bool),
		pendingAnswers:   make(map[string]chan string),
		pendingThreadIDs: make(map[string]string),
		runners:          runtimerunner.NewRegistry(),
		streams:          pubsub.NewTopic[*StreamEvent](),
		agents:           pubsub.NewTopic[*AgentState](),
		tools:            pubsub.NewTopic[*ToolExecution](),
		subagents:        pubsub.NewTopic[*SubagentProgress](),
		threads:          pubsub.NewBroadcast[*ThreadStateEvent](),
	}
	if searcher != nil {
		// 4 workers: conservative for a single consumer-class GPU
		// hosting both reranker and embedder simultaneously. 64-job
		// buffer: one autonomous round generates up to ~15 messages
		// (assistant text + tool call + tool result, sometimes chained);
		// 64 absorbs ~4 back-to-back rounds before backpressure kicks
		// in, which is well inside normal latency. 2-minute job
		// timeout: healthy embed is sub-second; 2m is the fail-fast
		// ceiling so a wedged TEI releases the worker instead of
		// holding it forever as context.Background() used to.
		r.embedQueue.Store(search.NewEmbedQueue(searcher, 4, 64, 2*time.Minute))
	}
	return r
}

// EmbedQueue returns the current bounded-fan-out pool for post-insert
// embedding work. Nil if no embedder is configured or after a settings
// reload removed the embedder. Callers must tolerate nil.
func (r *Resolver) EmbedQueue() *search.EmbedQueue {
	return r.embedQueue.Load()
}

// setEmbedQueue atomically swaps in a new queue, returning the old one
// so the caller can Close() it off the hot path. The swap is lock-free
// so hot readers (OnMessageStored, storeMessage) don't take a mutex on
// every message insert.
func (r *Resolver) setEmbedQueue(q *search.EmbedQueue) *search.EmbedQueue {
	return r.embedQueue.Swap(q)
}

// ReloadProviders re-creates all providers from the current config.
// Called after settings save. Updates the engine's classifier and completer,
// rebuilds the main completer, and kills stale runners so they pick up new providers.
func (r *Resolver) ReloadProviders() error {
	cfg := r.Config

	// Main completer
	if mainCfg, ok := cfg.Settings.Providers["main"]; ok && mainCfg.Adapter != "" {
		p, err := core.NewProvider(mainCfg.ToCore())
		if err != nil {
			return fmt.Errorf("main provider: %w", err)
		}
		completer, err := p.Completer(mainCfg.Model)
		if err != nil {
			return fmt.Errorf("main completer %s/%s: %w", mainCfg.Adapter, mainCfg.Model, err)
		}
		r.Main = completer
	}

	// Classifier + vector provider. Both are keyed by model ID so a
	// model change doesn't silently corrupt old cached rows (readers
	// filter by the current model; old rows stay under their old key
	// and never conflict). Rewiring the persister here is what makes
	// settings changes take effect for scoring without a restart.
	var nliURL, embedURL, entailerURL, nliModelID, embedModelID string
	if clsCfg, ok := cfg.Settings.Providers["classifier"]; ok {
		nliURL = clsCfg.BaseURL
		nliModelID = clsCfg.Model
	}
	if embCfg, ok := cfg.Settings.Providers["embedder"]; ok {
		embedURL = embCfg.BaseURL
		embedModelID = embCfg.Model
	}
	// Optional composite NLI stage. A separate TEI instance serving an
	// MNLI-trained classifier (DeBERTa-v3-base-mnli or similar) layered
	// on top of the reranker for directional-entailment scoring. Empty
	// URL → no entailer wired, OnMessage falls back to reranker-only
	// (today's behavior). The NLIFusionWeight engine config controls
	// the fusion α when an entailer is wired.
	if enCfg, ok := cfg.Settings.Providers["entailer"]; ok {
		entailerURL = enCfg.BaseURL
	}
	if nliURL != "" || embedURL != "" || entailerURL != "" {
		classifier := tei.NewCompositeClassifier(nliURL, embedURL)
		r.Engine.Lock()
		r.Engine.SetClassifier(classifier)

		// Wire (or un-wire) the entailer atomically under the engine
		// lock so OnMessage doesn't observe a half-swapped state.
		if ent := tei.NewEntailer(entailerURL); ent != nil {
			r.Engine.SetEntailer(ent)
			log.Printf("[Providers] entailer wired: url=%s", entailerURL)
		} else {
			r.Engine.SetEntailer(nil)
		}

		// Re-install the score persister with the (possibly new) model
		// ID. nil-clears first so a model change doesn't keep writing
		// under the old key if the new config has no classifier.
		r.Engine.SetScorePersister(nil)
		if nliModelID != "" {
			db := r.DB
			modelID := nliModelID
			r.Engine.SetScorePersister(func(fromID string, fromIdx int, toID string, toIdx int, score float64) {
				if err := db.InsertChunkScore(fromID, fromIdx, toID, toIdx, modelID, score); err != nil {
					log.Printf("InsertChunkScore(%s[%d], %s[%d], %s): %v", fromID, fromIdx, toID, toIdx, modelID, err)
				}
			})
		}

		// Re-wire the chunk oracle. If the embedder changed, this
		// uses the new embedder/model. Its own embed-live path
		// writes through under the new model_id; old rows stay.
		if classifier.Embedder() != nil && embedModelID != "" {
			co := search.NewChunkOracle(r.DB, classifier.Embedder(), embedModelID)
			r.Engine.SetChunkOracle(co)
			go co.BackfillEmbeddings(context.Background())
		} else {
			r.Engine.SetChunkOracle(nil)
		}
		r.Engine.Unlock()

		// Rebuild Searcher + EmbedQueue to point at the new embedder.
		// Without this, the post-insert embed path (OnMessageStored /
		// storeMessage → EmbedQueue.Enqueue → Searcher.EmbedMessageChunks)
		// keeps calling the old embedder forever — embeds silently go
		// to the previous endpoint with the previous model ID. The old
		// queue is Close()'d off the hot path so its workers drain
		// cleanly rather than leaking; Close blocks until in-flight
		// jobs finish, which is fine because setEmbedQueue has already
		// atomically swapped in the new queue and new inserts are
		// routing there.
		var oldQueue *search.EmbedQueue
		if classifier.Embedder() != nil && embedModelID != "" {
			newSearcher := search.NewSearcher(classifier.Embedder(), embedModelID, r.DB)
			r.Searcher = newSearcher
			oldQueue = r.setEmbedQueue(search.NewEmbedQueue(newSearcher, 4, 64, 2*time.Minute))
		} else {
			// Embedder removed from config — tear down search/embed
			// path entirely rather than leave it pointing at stale
			// state.
			r.Searcher = nil
			oldQueue = r.setEmbedQueue(nil)
		}
		if oldQueue != nil {
			go oldQueue.Close()
		}
	}

	// Kill all existing runners — they hold references to old providers.
	// Next getOrCreateRunner call builds a fresh runner with the new config.
	r.runners.StopAll()

	return nil
}

// getOrCreateRunner returns the active runner for a thread, creating
// one via the runtime/runner factory if no entry exists. The factory
// owns every concern previously inlined here — askCh goroutine,
// ToolDeps construction, prompt assembly, retry wrapping, callback
// wiring. The resolver supplies the bridge interfaces (Pubsub /
// Approvals / PlanStore / Selections / EmbedEnqueuer) via runtimeDeps;
// see runtimebridge.go for the implementations.
func (r *Resolver) getOrCreateRunner(threadID string) (*agent.Runner, error) {
	if entry, ok := r.runners.Get(threadID); ok {
		return entry.Runner, nil
	}
	entry, err := runtimerunner.Build(threadID, r.runtimeDeps())
	if err != nil {
		return nil, fmt.Errorf("build runner %s: %w", threadID, err)
	}
	r.runners.Set(threadID, entry)
	return entry.Runner, nil
}

// storeMessage inserts a message (which cascades into chunk creation
// via the DB's chunker hook) and fires eager embedding of those
// chunks into the search/RRC cache. The embed is async — failure
// falls back to the startup backfill goroutine and the live-embed
// path in ChunkOracle.EnsureVector, so a slow or down embedder
// doesn't block a user mutation.
func (r *Resolver) storeMessage(msg *pb.Message, _text string) error {
	if err := r.DB.InsertMessage(msg); err != nil {
		return err
	}
	if q := r.EmbedQueue(); q != nil {
		q.Enqueue(msg.Id)
	}
	return nil
}

// stopRunner stops and removes a thread's runner.
// For subagent forks, merges edges and scores back into the parent before cleanup.
func (r *Resolver) stopRunner(threadID string) {
	entry, ok := r.runners.Get(threadID)
	if !ok {
		return
	}
	// Merge subagent fork back into parent
	if entry.ParentThreadID != "" {
		if parentEntry, pOk := r.runners.Get(entry.ParentThreadID); pOk {
			if err := parentEntry.Runner.MergeSubagent(entry.Runner); err != nil {
				log.Printf("Subagent merge %s → %s: %v", threadID, entry.ParentThreadID, err)
			}
		}
		r.publishSubagent(entry.ParentThreadID, &SubagentProgress{
			ThreadID: entry.ParentThreadID, ForkThreadID: threadID,
			Status: "completed",
		})
	}
	// Cancel the in-flight turn (if any) before cancelling the
	// autonomous-loop ctx. The turn's ctx is a child of whatever
	// caller ctx ADK is running under; entry.Cancel is the
	// autonomous outer loop's ctx. A non-autonomous streaming send
	// has no entry.Cancel — the turn-cancel is the only lever that
	// reaches it.
	if entry.Runner != nil {
		entry.Runner.CancelTurn()
	}
	if entry.Cancel != nil {
		entry.Cancel()
	}
	if entry.AskCh != nil {
		close(entry.AskCh)
	}
	r.runners.Delete(threadID)
}

// unsubscribeOnDone waits for ctx.Done, then calls cleanup under the lock.
func unsubscribeOnDone(ctx context.Context, cleanup func()) {
	go func() {
		<-ctx.Done()
		cleanup()
	}()
}

// subscribe/publish thin delegates to the per-shape pubsub topics.
// Resolver methods stay in this package so the GraphQL resolver
// boilerplate doesn't need to know about pubsub.
func (r *Resolver) subscribeStream(threadID string) chan *StreamEvent {
	return r.streams.Subscribe(threadID)
}

func (r *Resolver) publishStream(threadID string, event *StreamEvent) {
	r.streams.Publish(threadID, event)
}

func (r *Resolver) subscribeAgentState(threadID string) chan *AgentState {
	return r.agents.Subscribe(threadID)
}

func (r *Resolver) publishAgentState(threadID string, state *AgentState) {
	r.agents.Publish(threadID, state)
}

// publishRetryStatus emits an AgentState with the retry field filled
// in, other fields pulled from the DB so a retry event doesn't wipe
// the frontend's view of status/mode/round_count. When ev.Final is
// true AND ev.Err is nil it's a success clear — publish with retry=nil
// so the UI hides the retry indicator.
func (r *Resolver) publishRetryStatus(threadID string, ev retry.Event) {
	// Log every retry event server-side so "why is this happening" has
	// an actual answer in spidey.log instead of being stuck in the UI's
	// retry subscription buffer. Final+no-error = success clear; Final
	// with an error = terminal; otherwise an in-flight retry with a
	// delay before the next attempt.
	switch {
	case ev.Final && ev.Err == nil:
		log.Printf("RETRY: thread=%s attempt=%d/%d cleared (success)", threadID, ev.Attempt, ev.MaxAttempts)
	case ev.Final && ev.Err != nil:
		log.Printf("RETRY: thread=%s attempt=%d/%d TERMINAL err=%q", threadID, ev.Attempt, ev.MaxAttempts, ev.Err.Error())
	default:
		log.Printf("RETRY: thread=%s attempt=%d/%d nextDelay=%s err=%q", threadID, ev.Attempt, ev.MaxAttempts, ev.NextDelay, ev.Err.Error())
	}

	st, _ := r.DB.GetAgentState(threadID)
	base := &AgentState{ThreadID: threadID}
	if st != nil {
		switch st.Status {
		case storage.AgentStatusRunning:
			base.Status = AgentStatusRunning
		case storage.AgentStatusPaused:
			base.Status = AgentStatusPaused
		default:
			base.Status = AgentStatusIdle
		}
		switch st.Mode {
		case storage.AgentModeAutonomous:
			base.Mode = AgentModeAutonomous
		case storage.AgentModePlan:
			base.Mode = AgentModePlan
		default:
			base.Mode = AgentModeNormal
		}
		base.RoundCount = st.RoundCount
		if st.DurationLimit != "" {
			d := st.DurationLimit
			base.DurationLimit = &d
		}
	}

	// Success clear: retry is done and succeeded. Frontend hides the indicator.
	if ev.Final && ev.Err == nil {
		r.publishAgentState(threadID, base)
		return
	}

	rs := &RetryStatus{
		Attempt:     ev.Attempt,
		MaxAttempts: ev.MaxAttempts,
		NextDelayMs: int(ev.NextDelay / 1e6),
		Final:       ev.Final,
	}
	if ev.Err != nil {
		msg := ev.Err.Error()
		rs.Error = &msg
	}
	base.Retry = rs
	r.publishAgentState(threadID, base)
}

func (r *Resolver) subscribeToolExec(threadID string) chan *ToolExecution {
	return r.tools.Subscribe(threadID)
}

func (r *Resolver) publishToolExec(threadID string, event *ToolExecution) {
	r.tools.Publish(threadID, event)
}

func (r *Resolver) subscribeSubagent(threadID string) chan *SubagentProgress {
	return r.subagents.Subscribe(threadID)
}

func (r *Resolver) publishSubagent(threadID string, event *SubagentProgress) {
	r.subagents.Publish(threadID, event)
}

func (r *Resolver) subscribeThreadState() chan *ThreadStateEvent {
	return r.threads.Subscribe()
}

func (r *Resolver) publishThreadState(event *ThreadStateEvent) {
	r.threads.Publish(event)
}
