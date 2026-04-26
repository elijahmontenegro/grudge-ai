// Package kernel is the runtime kernel: a Bootstrap-time
// composition of every long-lived runtime concern (substrate,
// runner registry, plan-content cache, selection introspection,
// tool-approval channels, embed queue) plus the methods that
// satisfy the runner-factory interfaces (Approvals, PlanStore,
// Selections, EmbedEnqueuer).
//
// What lives here vs. what doesn't:
//
//   - Kernel owns runtime state — the maps, channels, registry,
//     and substrate references that survive across mutations and
//     queries. A non-GraphQL consumer can import kernel and get a
//     working agent without touching the graph layer.
//
//   - Pubsub is graph-typed (gqlgen-generated event structs) and
//     stays in the graph layer. The runtime emits plain Go structs
//     and the graph adapter translates.
//
// Bootstrap consolidates every step main.go used to inline:
// tiktoken, storage open + thread-name backfill, sandbox preflight,
// substrate.Build, MCP toolset loading, prompt assembler, hooks
// dispatcher, skills loading. main.go shrinks to a thin wrapper.
package kernel

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/adapter/tei"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/rrc/tiktoken"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/hooks"
	"github.com/emontenegr/spidey/service/prompt"
	runtimerunner "github.com/emontenegr/spidey/service/runtime/runner"
	"github.com/emontenegr/spidey/service/runtime/substrate"
	"github.com/emontenegr/spidey/service/sandbox"
	"github.com/emontenegr/spidey/service/search"
	skillspkg "github.com/emontenegr/spidey/service/skills"
	"github.com/emontenegr/spidey/service/storage"

	"google.golang.org/adk/tool"
)

// Kernel is the runtime substrate plus the per-thread runtime
// state. Embedded into graph.Resolver so resolvers reach common
// fields (DB, Engine, Config, Main) directly via promotion;
// graph-specific concerns (pubsub topics) live on the resolver.
type Kernel struct {
	Config     *config.Config
	DB         *storage.DB
	Engine     *rrc.Engine
	Main       core.Completer
	Classifier core.Classifier
	Searcher   *search.Searcher
	Assembler  *prompt.Assembler
	Hooks      *hooks.Dispatcher
	Skills     []skillspkg.Skill
	MCPTools   []tool.Tool

	// Runners is the per-thread runner registry. The runtime
	// factory owns construction; lifecycle (cancel/close on stop,
	// merge on subagent exit) is orchestrated by the consumer.
	Runners *runtimerunner.Registry

	// embedQueue is atomic.Pointer so ReloadProviders can swap it
	// during a settings change without racing against the hot
	// OnMessageStored / storeMessage readers on every message
	// insert. Use EmbedQueue() to read, setEmbedQueue() to write.
	embedQueue atomic.Pointer[search.EmbedQueue]

	// Plan-content cache per thread. Set by the ExitPlan tool,
	// read by AgentState publishes to attach to outgoing events.
	planContent   map[string]string
	planContentMu sync.RWMutex

	// Selection introspection caches. selectionResults is keyed
	// by event id (sel-<msg_id>); latestSelection maps thread →
	// most recent event id; citationCount tallies how many times
	// each message was selected as a prerequisite.
	selectionResults map[string]*pb.SelectionResult
	latestSelection  map[string]string
	citationCount    map[string]int
	selectionMu      sync.RWMutex

	// Tool-approval channel registry. Each map entry is a chan
	// the runtime owns and the graph mutations send into.
	pendingApprovals map[string]chan bool
	pendingAnswers   map[string]chan string
	pendingThreadIDs map[string]string
	pendingMu        sync.Mutex
}

// Bootstrap wires the runtime kernel from a loaded config. Returns
// a Kernel ready for the consumer (graph.Resolver or any other
// HTTP layer) to attach pubsub topics on top of and start serving.
//
// Failures here are fatal — no token estimator, no storage, no
// providers means no agent. Sandbox preflight is non-fatal (logs
// and continues; sandboxed=false threads are unaffected).
func Bootstrap(ctx context.Context, cfg *config.Config) (*Kernel, error) {
	// tiktoken is the committed token estimator for context-budget
	// sizing. If it can't load — corrupt cache, network unreachable
	// for first-run fetch — refuse to start rather than silently
	// degrade to a char-based heuristic that would change the unit
	// every downstream budget check operates in.
	tokenEst, err := tiktoken.New()
	if err != nil {
		return nil, fmt.Errorf("token estimator: %w", err)
	}
	rrc.SetDefaultEstimator(tokenEst)

	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	if n := db.BackfillThreadNames(); n > 0 {
		log.Printf("Named %d unnamed threads from first message", n)
	}

	// Sandbox preflight — non-fatal. Threads with sandboxed=true
	// will fail at Bash-call time with the same error if the image
	// is not built; logging here makes the situation visible at
	// boot instead of surprising the user mid-turn.
	if err := sandbox.CheckReady(); err != nil {
		log.Printf("Sandbox not ready: %v (sandboxed=false threads unaffected)", err)
	} else {
		log.Printf("Sandbox ready: image %s", sandbox.Image)
	}

	subs, err := substrate.Build(ctx, cfg, db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("substrate: %w", err)
	}

	templateDir := resolveTemplateDir()

	// MCP toolsets from settings.
	var mcpConfigs []agent.MCPServerConfig
	for _, srv := range cfg.Settings.MCPServers {
		mcpConfigs = append(mcpConfigs, agent.MCPServerConfig{
			Name: srv.Name, Endpoint: srv.Endpoint, Enabled: srv.Enabled,
		})
	}
	mcpToolsets := agent.LoadMCPTools(mcpConfigs)
	if len(mcpToolsets) > 0 {
		log.Printf("Loaded %d MCP toolsets", len(mcpToolsets))
	}
	mcpTools, err := agent.MCPToolsAsTools(mcpToolsets)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mcp tools: %w", err)
	}

	assembler := prompt.NewAssembler(templateDir)
	log.Printf("Prompt templates: %s", templateDir)

	hookDispatcher := hooks.NewDispatcher(cfg.Settings.Hooks)

	loadedSkills := skillspkg.LoadAll(filepath.Join(cfg.DataDir, "skills"), nil)
	if len(loadedSkills) > 0 {
		log.Printf("Loaded %d skills", len(loadedSkills))
	}

	k := &Kernel{
		Config:     cfg,
		DB:         db,
		Engine:     subs.Engine,
		Main:       subs.MainCompleter,
		Classifier: subs.Classifier,
		Searcher:   subs.Searcher,
		Assembler:  assembler,
		Hooks:      hookDispatcher,
		Skills:     loadedSkills,
		MCPTools:   mcpTools,

		Runners: runtimerunner.NewRegistry(),

		planContent:      make(map[string]string),
		selectionResults: make(map[string]*pb.SelectionResult),
		latestSelection:  make(map[string]string),
		citationCount:    make(map[string]int),
		pendingApprovals: make(map[string]chan bool),
		pendingAnswers:   make(map[string]chan string),
		pendingThreadIDs: make(map[string]string),
	}

	if subs.Searcher != nil {
		// 4 workers / 64-job buffer / 2m timeout — see EmbedQueue
		// doc comment for the rationale (single-GPU TEI deadlock
		// observed 2026-04-23).
		k.embedQueue.Store(search.NewEmbedQueue(subs.Searcher, 4, 64, 2*time.Minute))
	}

	return k, nil
}

// Shutdown closes the underlying storage. Callers should also
// stop active runners (Runners.StopAll) before invoking Shutdown
// if they want clean drain semantics.
func (k *Kernel) Shutdown() error {
	return k.DB.Close()
}

// resolveTemplateDir walks the candidate paths (next to the
// binary, common dev layouts) and returns the first existing
// templates directory. Falls back to the literal "templates" if
// nothing matches; the assembler will error on first use if it's
// genuinely missing.
func resolveTemplateDir() string {
	templateDir := "templates"
	exePath, err := os.Executable()
	if err != nil {
		return templateDir
	}
	candidates := []string{
		filepath.Join(filepath.Dir(exePath), "templates"),
		filepath.Join(filepath.Dir(exePath), "..", "templates"),
		filepath.Join(filepath.Dir(exePath), "..", "..", "..", "templates"),
		"templates",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	return templateDir
}

// EmbedQueue returns the current bounded-fan-out pool for
// post-insert embedding work. Nil if no embedder is configured or
// after a settings reload removed the embedder. Callers must
// tolerate nil.
func (k *Kernel) EmbedQueue() *search.EmbedQueue {
	return k.embedQueue.Load()
}

// setEmbedQueue atomically swaps in a new queue, returning the old
// one so the caller can Close() it off the hot path. The swap is
// lock-free so hot readers (Enqueue) don't take a mutex on every
// message insert.
func (k *Kernel) setEmbedQueue(q *search.EmbedQueue) *search.EmbedQueue {
	return k.embedQueue.Swap(q)
}

// ReloadProviders re-creates all providers from the current
// config. Called after settings save. Updates the engine's
// classifier and completer, rebuilds the main completer, and kills
// stale runners so they pick up new providers on next
// getOrCreateRunner.
func (k *Kernel) ReloadProviders() error {
	cfg := k.Config

	// Main completer.
	if mainCfg, ok := cfg.Settings.Providers["main"]; ok && mainCfg.Adapter != "" {
		p, err := core.NewProvider(mainCfg.ToCore())
		if err != nil {
			return fmt.Errorf("main provider: %w", err)
		}
		completer, err := p.Completer(mainCfg.Model)
		if err != nil {
			return fmt.Errorf("main completer %s/%s: %w", mainCfg.Adapter, mainCfg.Model, err)
		}
		k.Main = completer
	}

	// Classifier + vector provider. Both are keyed by model ID so
	// a model change doesn't silently corrupt old cached rows
	// (readers filter by the current model; old rows stay under
	// their old key and never conflict). Rewiring the persister
	// here is what makes settings changes take effect for scoring
	// without a restart.
	var nliURL, embedURL, entailerURL, nliModelID, embedModelID string
	if clsCfg, ok := cfg.Settings.Providers["classifier"]; ok {
		nliURL = clsCfg.BaseURL
		nliModelID = clsCfg.Model
	}
	if embCfg, ok := cfg.Settings.Providers["embedder"]; ok {
		embedURL = embCfg.BaseURL
		embedModelID = embCfg.Model
	}
	if enCfg, ok := cfg.Settings.Providers["entailer"]; ok {
		entailerURL = enCfg.BaseURL
	}
	if nliURL != "" || embedURL != "" || entailerURL != "" {
		classifier := tei.NewCompositeClassifier(nliURL, embedURL)
		k.Engine.Lock()
		k.Engine.SetClassifier(classifier)

		// Wire (or un-wire) the entailer atomically under the
		// engine lock so OnMessage doesn't observe a half-swapped
		// state.
		if ent := tei.NewEntailer(entailerURL); ent != nil {
			k.Engine.SetEntailer(ent)
			log.Printf("[Providers] entailer wired: url=%s", entailerURL)
		} else {
			k.Engine.SetEntailer(nil)
		}

		// Re-install the score persister with the (possibly new)
		// model ID. nil-clears first so a model change doesn't
		// keep writing under the old key if the new config has no
		// classifier.
		k.Engine.SetScorePersister(nil)
		if nliModelID != "" {
			db := k.DB
			modelID := nliModelID
			k.Engine.SetScorePersister(func(fromID string, fromIdx int, toID string, toIdx int, score float64) {
				if err := db.InsertChunkScore(fromID, fromIdx, toID, toIdx, modelID, score); err != nil {
					log.Printf("InsertChunkScore(%s[%d], %s[%d], %s): %v", fromID, fromIdx, toID, toIdx, modelID, err)
				}
			})
		}

		// Re-wire the chunk oracle. If the embedder changed, this
		// uses the new embedder/model. Its own embed-live path
		// writes through under the new model_id; old rows stay.
		if classifier.Embedder() != nil && embedModelID != "" {
			co := search.NewChunkOracle(k.DB, classifier.Embedder(), embedModelID)
			k.Engine.SetChunkOracle(co)
			go co.BackfillEmbeddings(context.Background())
		} else {
			k.Engine.SetChunkOracle(nil)
		}
		k.Engine.Unlock()

		// Rebuild Searcher + EmbedQueue to point at the new
		// embedder. Without this, the post-insert embed path keeps
		// calling the old embedder forever — embeds silently go to
		// the previous endpoint with the previous model ID. The
		// old queue is Close()'d off the hot path so its workers
		// drain cleanly rather than leaking.
		var oldQueue *search.EmbedQueue
		if classifier.Embedder() != nil && embedModelID != "" {
			newSearcher := search.NewSearcher(classifier.Embedder(), embedModelID, k.DB)
			k.Searcher = newSearcher
			oldQueue = k.setEmbedQueue(search.NewEmbedQueue(newSearcher, 4, 64, 2*time.Minute))
		} else {
			// Embedder removed from config — tear down search/embed
			// path entirely rather than leave it pointing at stale
			// state.
			k.Searcher = nil
			oldQueue = k.setEmbedQueue(nil)
		}
		if oldQueue != nil {
			go oldQueue.Close()
		}
	}

	// Kill all existing runners — they hold references to old
	// providers. Next getOrCreateRunner call builds a fresh runner
	// against the new config.
	k.Runners.StopAll()

	return nil
}

// --- runner.PlanStore ---------------------------------------------------

// GetPlan returns the cached plan content for a thread, or empty
// if none is set.
func (k *Kernel) GetPlan(threadID string) string {
	k.planContentMu.RLock()
	defer k.planContentMu.RUnlock()
	return k.planContent[threadID]
}

// SetPlan stores plan content for a thread, overwriting any prior
// content.
func (k *Kernel) SetPlan(threadID, content string) {
	k.planContentMu.Lock()
	k.planContent[threadID] = content
	k.planContentMu.Unlock()
}

// ClearPlan removes any cached plan content for a thread.
// approvePlan / rejectPlan call this to drop the artifact.
func (k *Kernel) ClearPlan(threadID string) {
	k.planContentMu.Lock()
	delete(k.planContent, threadID)
	k.planContentMu.Unlock()
}

// --- runner.Selections + graph reads -----------------------------------

// RecordSelection persists a selection event in the in-memory
// citation tally and writes it through to the selections table for
// audit. Called by the runner factory's selection callback.
func (k *Kernel) RecordSelection(threadID string, result *pb.SelectionResult) {
	k.selectionMu.Lock()
	k.selectionResults[result.EventId] = result
	k.latestSelection[threadID] = result.EventId
	for _, sel := range result.Selected {
		k.citationCount[sel.MessageId]++
	}
	k.selectionMu.Unlock()

	// Event IDs are synthesized as sel-<target_message_id> in the
	// engine. Strip the prefix to recover the target for the
	// selections table FK.
	targetID := result.EventId
	if len(targetID) > 4 && targetID[:4] == "sel-" {
		targetID = targetID[4:]
	}
	if err := k.DB.SaveSelection(result, targetID, threadID); err != nil {
		log.Printf("SaveSelection(event=%s target=%s): %v", result.EventId, targetID, err)
	}
}

// GetSelection returns the in-memory cached selection for an event
// id. Used by graph.queryResolver.SelectionResult as the hot path
// before falling back to DB lookup.
func (k *Kernel) GetSelection(eventID string) (*pb.SelectionResult, bool) {
	k.selectionMu.RLock()
	defer k.selectionMu.RUnlock()
	res, ok := k.selectionResults[eventID]
	return res, ok
}

// LatestSelectionID returns the most recent selection event id for
// a thread, or empty if none recorded this session.
func (k *Kernel) LatestSelectionID(threadID string) (string, bool) {
	k.selectionMu.RLock()
	defer k.selectionMu.RUnlock()
	id, ok := k.latestSelection[threadID]
	return id, ok
}

// CitationCount returns how many times this message has been
// selected as a prerequisite this session.
func (k *Kernel) CitationCount(messageID string) int {
	k.selectionMu.RLock()
	defer k.selectionMu.RUnlock()
	return k.citationCount[messageID]
}

// --- runner.Approvals + graph reads ------------------------------------

// RegisterApproval registers a buffered approval channel for a
// pending tool call. Returns the receive side and an unregister
// thunk; callers (the runner factory) defer the unregister.
func (k *Kernel) RegisterApproval(callID, threadID string) (<-chan bool, func()) {
	ch := make(chan bool, 1)
	k.pendingMu.Lock()
	k.pendingApprovals[callID] = ch
	k.pendingThreadIDs[callID] = threadID
	k.pendingMu.Unlock()
	return ch, func() {
		k.pendingMu.Lock()
		delete(k.pendingApprovals, callID)
		delete(k.pendingThreadIDs, callID)
		k.pendingMu.Unlock()
	}
}

// RegisterAnswer registers a buffered answer channel for a pending
// AskUserQuestion. Returns the receive side and an unregister
// thunk.
func (k *Kernel) RegisterAnswer(callID string) (<-chan string, func()) {
	ch := make(chan string, 1)
	k.pendingMu.Lock()
	k.pendingAnswers[callID] = ch
	k.pendingMu.Unlock()
	return ch, func() {
		k.pendingMu.Lock()
		delete(k.pendingAnswers, callID)
		k.pendingMu.Unlock()
	}
}

// SendApproval delivers an approval/denial verdict to the waiting
// goroutine. Returns false if no approval is pending under callID
// (stale answer, expired wait). Used by graph
// approveToolCall/denyToolCall mutations.
func (k *Kernel) SendApproval(callID string, approved bool) bool {
	k.pendingMu.Lock()
	ch, ok := k.pendingApprovals[callID]
	k.pendingMu.Unlock()
	if !ok {
		return false
	}
	ch <- approved
	return true
}

// SendAnswer delivers a question answer to the waiting AskUser
// goroutine. Returns false if no question is pending under callID.
// Used by graph answerQuestion mutation.
func (k *Kernel) SendAnswer(callID, answer string) bool {
	k.pendingMu.Lock()
	ch, ok := k.pendingAnswers[callID]
	k.pendingMu.Unlock()
	if !ok {
		return false
	}
	ch <- answer
	return true
}

// ThreadIDForCall returns the thread id associated with a pending
// approval, used by denyToolCall to synthesize the per-thread
// denial-reason system message.
func (k *Kernel) ThreadIDForCall(callID string) string {
	k.pendingMu.Lock()
	defer k.pendingMu.Unlock()
	return k.pendingThreadIDs[callID]
}

// --- runner.EmbedEnqueuer ---------------------------------------------

// Enqueue routes a message id into the bounded embed queue.
// Tolerates a nil queue (embedder not configured / settings reload
// cleared it) — silent no-op falls back to the startup backfill
// goroutine and the live-embed path in ChunkOracle.EnsureVector,
// so a slow or down embedder doesn't block message inserts.
func (k *Kernel) Enqueue(messageID string) {
	if q := k.EmbedQueue(); q != nil {
		q.Enqueue(messageID)
	}
}
