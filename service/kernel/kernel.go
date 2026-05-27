// Package kernel is the application composition root — a Bootstrap
// that wires together storage, the substrate holder (substrate +
// engine + embed-queue atomic-swap), prompt assembly, hooks, skills,
// MCP tools, the per-thread state caches (plans, selections,
// approvals), and the per-thread runner registry.
//
// What lives here vs. what doesn't:
//
//   - Kernel is a composition root. The state-bearing maps and
//     atomic-swap machinery live in their own focused packages
//     (service/plans, service/selections, service/approvals,
//     service/substrate's Holder). Kernel just holds pointers to
//     them and provides delegating methods for back-compat with
//     graph.Resolver's embed pattern.
//
//   - Pubsub is graph-typed (gqlgen-generated event structs) and
//     stays in the graph layer. The runtime emits plain Go structs
//     and the graph adapter translates.
//
// Engine atomicity. The substrate.Holder owns the engine pointer;
// reads go through k.Engine() which forwards to Holder.Engine().
// ReloadProviders / UpdateEngineConfig delegate to the Holder; the
// onReload hook refreshes Kernel's Main/Scorer/Searcher fields and
// stops all in-flight runners against the now-stale engine.
package kernel

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/rrc/chunk"
	"github.com/emontenegr/spidey/rrc/tiktoken"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/approvals"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/hooks"
	"github.com/emontenegr/spidey/service/messages"
	"github.com/emontenegr/spidey/service/plans"
	"github.com/emontenegr/spidey/service/prompt"
	"github.com/emontenegr/spidey/service/runtime"
	"github.com/emontenegr/spidey/service/sandbox"
	"github.com/emontenegr/spidey/service/search"
	"github.com/emontenegr/spidey/service/selections"
	skillspkg "github.com/emontenegr/spidey/service/skills"
	"github.com/emontenegr/spidey/service/storage"
	"github.com/emontenegr/spidey/service/substrate"

	"google.golang.org/adk/tool"
)

// Kernel composes the long-lived application state. Embedded into
// graph.Resolver so resolvers reach common fields directly via
// promotion. Each substantive concern lives in its own package;
// Kernel is the composition root that owns the wiring.
type Kernel struct {
	Config    *config.Config
	DB        *storage.DB
	Substrate *substrate.Holder

	// Main / Scorer / Searcher mirror the current substrate's
	// completer / scorer / searcher. Refreshed by the substrate
	// Holder's onReload callback whenever ReloadProviders or
	// UpdateEngineConfig swaps a new substrate in.
	Main     core.Completer
	Scorer   core.Scorer
	Searcher *search.Searcher

	Assembler *prompt.Assembler
	Hooks     *hooks.Dispatcher
	Skills    []skillspkg.Skill
	MCPTools  []tool.Tool
	Inserter  *messages.Inserter

	// Runners is the per-thread runner registry. The runtime
	// factory owns construction; lifecycle (cancel/close on stop,
	// merge on subagent exit) is orchestrated by the consumer.
	Runners *runtime.Registry

	// Focused per-concern sub-types. Kernel's GetPlan / SetPlan /
	// RecordSelection / RegisterApproval / etc. methods delegate to
	// these (plans.go, selections.go, approvals.go in this package).
	Plans      *plans.Cache
	Selections *selections.Tracker
	Approvals  *approvals.Registry
}

// Bootstrap wires the runtime kernel from a loaded config. Returns
// a Kernel ready for the consumer (graph.Resolver or any other
// HTTP layer) to attach pubsub topics on top of and start serving.
//
// Failures here are fatal — no token estimator, no storage, no
// providers means no agent. Sandbox preflight is non-fatal (logs
// and continues; sandboxed=false threads are unaffected).
//
// substrate options pass through to the initial substrate.Holder
// Bootstrap for callers that need to inject fakes (tests).
// Production callers pass nothing.
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

	assembler := prompt.NewAssembler()
	hookDispatcher := hooks.NewDispatcher(cfg.Settings.Hooks)
	loadedSkills := skillspkg.LoadAll(filepath.Join(cfg.DataDir, "skills"), nil)
	if len(loadedSkills) > 0 {
		log.Printf("Loaded %d skills", len(loadedSkills))
	}

	k := &Kernel{
		Config:    cfg,
		DB:        db,
		Assembler: assembler,
		Hooks:     hookDispatcher,
		Skills:    loadedSkills,
		MCPTools:  mcpTools,

		Runners: runtime.NewRegistry(),

		Plans:      plans.New(),
		Selections: selections.New(db),
		Approvals:  approvals.New(),
	}

	// Substrate.Holder owns the engine + embed-queue atomic pointers
	// and serializes reloads. The onReload closure refreshes Kernel's
	// Main / Scorer / Searcher mirrors and stops in-flight runners
	// against the now-stale substrate.
	k.Substrate = substrate.NewHolder(cfg, db, func() {
		cur := k.Substrate.Current()
		if cur == nil {
			return
		}
		k.Main = cur.MainCompleter
		k.Scorer = cur.Scorer
		k.Searcher = cur.Searcher
		k.Runners.StopAll()
	})

	if err := k.Substrate.Bootstrap(ctx, opts...); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("substrate: %w", err)
	}

	// Inserter centralizes message-store + chunk derivation + embed
	// enqueue. Closures pull live engine config + embed-queue across
	// atomic substrate swaps. Both the graph layer and the runner
	// route inserts through it so chunk derivation lives in one place.
	k.Inserter = messages.New(
		db,
		func() chunk.Config { return k.Engine().Config().Chunk },
		k.Enqueue,
	)

	return k, nil
}

// Shutdown closes the underlying storage. Callers should also
// stop active runners (Runners.StopAll) before invoking Shutdown
// if they want clean drain semantics.
func (k *Kernel) Shutdown() error {
	return k.DB.Close()
}

// Engine returns the currently-active RRC engine. Delegates to the
// substrate Holder's atomic.Pointer load.
func (k *Kernel) Engine() *rrc.Engine { return k.Substrate.Engine() }

// EmbedQueue returns the current bounded-fan-out pool for post-
// insert embedding work. Nil if no embedder is configured or after
// a settings reload removed the embedder. Callers tolerate nil.
func (k *Kernel) EmbedQueue() *search.EmbedQueue { return k.Substrate.EmbedQueue() }

// ReloadProviders rebuilds the substrate from the current config
// and atomically swaps it in. The onReload closure fires after the
// swap and refreshes Kernel.Main / Scorer / Searcher plus stops all
// in-flight runners.
//
// substrate options pass through (production callers pass nothing;
// tests inject fakes).
func (k *Kernel) ReloadProviders(ctx context.Context, opts ...substrate.Option) error {
	return k.Substrate.ReloadProviders(ctx, opts...)
}

// UpdateEngineConfig swaps in a fresh engine that reuses the
// current providers but with a different EngineConfig. The path
// for settings-only edits that don't touch provider URLs / models
// (threshold tweak, MMR lambda, radius size).
func (k *Kernel) UpdateEngineConfig(ctx context.Context, cfg rrc.EngineConfig, opts ...substrate.Option) error {
	return k.Substrate.UpdateEngineConfig(ctx, cfg, opts...)
}
