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
//
// Engine atomicity. The engine is held as an atomic.Pointer; reads
// go through the Engine() accessor. ReloadProviders and
// UpdateEngineConfig build a fresh engine via substrate.Build, then
// store the new pointer in one atomic write. There is no mutation
// surface on rrc.Engine — the previous setter-with-externally-held-
// lock antipattern (engineMu plumbed from resolver → runner →
// rrcllm) is gone.
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
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/rrc/chunk"
	"github.com/emontenegr/spidey/rrc/tiktoken"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/agentstate"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/hooks"
	"github.com/emontenegr/spidey/service/prompt"
	runtimerunner "github.com/emontenegr/spidey/service/runner"
	"github.com/emontenegr/spidey/service/substrate"
	"github.com/emontenegr/spidey/service/sandbox"
	"github.com/emontenegr/spidey/service/search"
	skillspkg "github.com/emontenegr/spidey/service/skills"
	"github.com/emontenegr/spidey/service/storage"

	"google.golang.org/adk/tool"
)

// Kernel is the runtime substrate plus the per-thread runtime
// state. Embedded into graph.Resolver so resolvers reach common
// fields (DB, Config, Main, Runners) directly via promotion;
// graph-specific concerns (pubsub topics) live on the resolver.
//
// Engine() is a method, not a field — the underlying pointer is
// rotated atomically on settings changes. Callers must invoke
// Engine() at the point of use; capturing the pointer for a long-
// lived operation (e.g. agent.Runner) is acceptable when paired
// with Runners.StopAll on reload, since stopped runners rebuild
// against the new pointer.
type Kernel struct {
	Config     *config.Config
	DB         *storage.DB
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

	// engine is rotated by ReloadProviders / UpdateEngineConfig.
	// All callers read via Engine().
	engine atomic.Pointer[rrc.Engine]

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

	// reloadMu serializes ReloadProviders / UpdateEngineConfig so
	// two concurrent settings saves can't interleave their
	// substrate builds and produce an engine pointing at half-fresh,
	// half-stale providers.
	reloadMu sync.Mutex
}

// Bootstrap wires the runtime kernel from a loaded config. Returns
// a Kernel ready for the consumer (graph.Resolver or any other
// HTTP layer) to attach pubsub topics on top of and start serving.
//
// Failures here are fatal — no token estimator, no storage, no
// providers means no agent. Sandbox preflight is non-fatal (logs
// and continues; sandboxed=false threads are unaffected).
//
// substrate options pass through to Build for callers that need to
// inject fakes (tests). Production callers pass nothing.
func Bootstrap(ctx context.Context, cfg *config.Config, opts ...substrate.Option) (*Kernel, error) {
	// tiktoken is the committed token estimator for context-budget
	// sizing. If it can't load — corrupt cache, network unreachable
	// for first-run fetch — refuse to start rather than silently
	// degrade to a char-based heuristic that would change the unit
	// every downstream budget check operates in.
	tokenEst, err := tiktoken.New()
	if err != nil {
		return nil, fmt.Errorf("token estimator: %w", err)
	}
	chunk.SetDefaultEstimator(tokenEst)

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

	subs, err := substrate.Build(ctx, cfg, db, opts...)
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
	k.engine.Store(subs.Engine)

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

// Engine returns the currently-active RRC engine. The pointer is
// stable for the duration of a single operation; if a settings
// change races with a long-running call, both the old and new
// engines remain functional — old in-flight ops complete on the
// engine they captured, new ops land on the new engine.
func (k *Kernel) Engine() *rrc.Engine {
	return k.engine.Load()
}

// resolveTemplateDir walks the candidate paths (next to the
// binary, then up one for repo-root dev layout) and returns the
// first existing templates directory. Templates live at templates/
// under the repo root; the spidey binary builds at
// service/spidey.exe (one level deep), so the repo root is one
// directory up. Falls back to the literal "templates" if nothing
// matches; the assembler will error on first use if it's genuinely
// missing.
func resolveTemplateDir() string {
	templateDir := "templates"
	exePath, err := os.Executable()
	if err != nil {
		return templateDir
	}
	candidates := []string{
		filepath.Join(filepath.Dir(exePath), "templates"),
		filepath.Join(filepath.Dir(exePath), "..", "templates"),
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

// ReloadProviders rebuilds the substrate from the current config
// and atomically swaps in a fresh engine. Existing runners are
// stopped — they captured the old engine pointer at construction;
// next getOrCreateRunner builds against the new pointer. The old
// embed queue is closed off the hot path.
//
// Concurrency: serialized by reloadMu so two simultaneous
// settings saves cannot interleave substrate builds. Inside the
// lock, the substrate is built first (failure here leaves the
// kernel state untouched), then the engine is swapped in one
// atomic write.
//
// substrate options pass through to Build (production callers
// pass nothing; tests inject fakes).
func (k *Kernel) ReloadProviders(ctx context.Context, opts ...substrate.Option) error {
	k.reloadMu.Lock()
	defer k.reloadMu.Unlock()

	subs, err := substrate.Build(ctx, k.Config, k.DB, opts...)
	if err != nil {
		return fmt.Errorf("rebuild substrate: %w", err)
	}

	// Engine swap is the only field mutation that needs to be
	// atomic from a hot-reader's perspective; everything else
	// (Main, Classifier, Searcher) is read off the cooler resolver
	// path.
	k.engine.Store(subs.Engine)

	if subs.MainCompleter != nil {
		k.Main = subs.MainCompleter
	}
	if subs.Classifier != nil {
		k.Classifier = subs.Classifier
	}

	// Searcher / EmbedQueue follow the embedder. Close the old
	// queue off the hot path so its workers drain rather than leak.
	var oldQueue *search.EmbedQueue
	if subs.Searcher != nil {
		k.Searcher = subs.Searcher
		oldQueue = k.setEmbedQueue(search.NewEmbedQueue(subs.Searcher, 4, 64, 2*time.Minute))
	} else {
		k.Searcher = nil
		oldQueue = k.setEmbedQueue(nil)
	}
	if oldQueue != nil {
		go oldQueue.Close()
	}

	// Stop all runners. Each holds its old engine + completer at
	// construction; next getOrCreateRunner picks up the fresh
	// pointers from the kernel.
	k.Runners.StopAll()

	return nil
}

// UpdateEngineConfig swaps in a fresh engine that reuses the
// current providers but with a different EngineConfig. The path
// for settings-only edits that don't touch provider URLs / models
// (threshold tweak, MMR lambda, radius size).
//
// Same swap semantics as ReloadProviders: build new from the same
// substrate snapshot (loaded edges + scores from disk, current
// classifier / oracle / persister), atomic Store, stop runners.
//
// substrate options pass through to Build for tests that need to
// inject fakes during the rebuild.
func (k *Kernel) UpdateEngineConfig(ctx context.Context, cfg rrc.EngineConfig, opts ...substrate.Option) error {
	k.reloadMu.Lock()
	defer k.reloadMu.Unlock()

	// Reuse substrate.Build to get a fresh engine wired to the
	// current providers. The new EngineConfig is applied via the
	// config-snapshot path — write into Settings.Engine, then
	// substrate.Build reads it on rebuild.
	old := k.Config.Settings.Engine
	k.Config.Settings.Engine = config.EngineConfig{
		EdgeThreshold:         cfg.EdgeThreshold,
		ScoreFloor:            cfg.ScoreFloor,
		ZScoreThreshold:       cfg.ZScoreThreshold,
		MinBatchStdDev:        cfg.MinBatchStdDev,
		RadiusSize:            cfg.RadiusSize,
		RerankTopK:            cfg.RerankTopK,
		ContextBudgetTokens:   cfg.ContextBudgetTokens,
		DiversityLambda:       cfg.DiversityLambda,
		BudgetHeadroomPct:     cfg.BudgetHeadroomPct,
		PerMsgDelimiterTokens: cfg.PerMsgDelimiterTokens,
	}
	subs, err := substrate.Build(ctx, k.Config, k.DB, opts...)
	if err != nil {
		k.Config.Settings.Engine = old
		return fmt.Errorf("rebuild substrate: %w", err)
	}
	k.engine.Store(subs.Engine)
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

// RemainingBudget reports how much of the autonomous run's original
// duration is still owed for a given persisted agent state. Returns
// (remaining, "") on success; (0, reason) when the row is missing
// fields, has unparseable values, or the budget has expired. The
// reasons are deliberately distinct (not collapsed to a generic
// "invalid") so a boot-time reconcile can log which row it gave up
// on and why. A 5-minute floor applies — see agentstate.ResumeMin.
//
// Kernel exposes this so the graph layer (which has the agent-state
// row in hand from its own DB read) doesn't import service/agentstate
// directly. The function itself is a pure transform over the row
// fields, so no Kernel state is consulted; the method exists for
// import-boundary hygiene.
func (k *Kernel) RemainingBudget(st *storage.AgentState) (time.Duration, string) {
	return agentstate.ComputeRemainingBudget(st)
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
