package graph

import (
	"context"
	"sync"

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/search"
	"github.com/emontenegr/spidey/service/storage"
)

// Resolver is the root resolver, holding all service dependencies.
type Resolver struct {
	DB       *storage.DB
	Engine   *rrc.Engine
	Config   *config.Config
	Searcher *search.Searcher
	Main     core.Completer // main model completer

	// Selection results for introspection (keyed by event ID)
	selectionResults map[string]*pb.SelectionResult
	// Latest selection event per thread
	latestSelection map[string]string // threadID -> eventID

	// RWMutex for Engine access — Engine is NOT goroutine-safe.
	engineMu sync.RWMutex

	// Active runners per thread — for autonomous/plan mode
	runners    map[string]*runnerEntry
	runnersMu  sync.Mutex

	// Per-thread subscription channels
	streamSubs   map[string][]chan *StreamEvent
	agentSubs    map[string][]chan *AgentState
	toolSubs     map[string][]chan *ToolExecution
	subagentSubs map[string][]chan *SubagentProgress
	threadSubs   []chan *ThreadStateEvent
	mu           sync.RWMutex
}

// NewResolver creates a resolver with all dependencies.
func NewResolver(db *storage.DB, engine *rrc.Engine, cfg *config.Config, searcher *search.Searcher, main core.Completer) *Resolver {
	return &Resolver{
		DB:           db,
		Engine:       engine,
		Config:       cfg,
		Searcher:     searcher,
		Main:         main,
		runners:      make(map[string]*runnerEntry),
		streamSubs:   make(map[string][]chan *StreamEvent),
		agentSubs:    make(map[string][]chan *AgentState),
		toolSubs:     make(map[string][]chan *ToolExecution),
		subagentSubs: make(map[string][]chan *SubagentProgress),
	}
}

// ReloadProviders re-creates providers from the current config. Called after settings save.
func (r *Resolver) ReloadProviders() error {
	cfg := r.Config

	// Rebuild main completer
	if mainCfg, ok := cfg.Settings.Providers["main"]; ok && mainCfg.Adapter != "" {
		p, err := config.BuildProvider(mainCfg)
		if err != nil {
			return err
		}
		completer, _ := p.Completer(mainCfg.Model)
		if completer != nil {
			r.Main = completer
		}
	}

	// Rebuild classifier for engine
	var nliURL, embedURL string
	if clsCfg, ok := cfg.Settings.Providers["classifier"]; ok {
		nliURL = clsCfg.BaseURL
	}
	if embCfg, ok := cfg.Settings.Providers["embedder"]; ok {
		embedURL = embCfg.BaseURL
	}
	if nliURL != "" || embedURL != "" {
		// Engine classifier is set at construction — can't swap.
		// But the composite classifier uses HTTP, so the URLs are what matter.
		// For a full reload, we'd need to recreate the engine. For now, log it.
		_ = nliURL
		_ = embedURL
	}

	return nil
}

// runnerEntry tracks an active runner and its cancellation.
type runnerEntry struct {
	runner *agent.Runner
	cancel context.CancelFunc
}

// getOrCreateRunner returns the active runner for a thread, creating one if needed.
func (r *Resolver) getOrCreateRunner(threadID string) (*agent.Runner, error) {
	r.runnersMu.Lock()
	defer r.runnersMu.Unlock()

	if entry, ok := r.runners[threadID]; ok {
		return entry.runner, nil
	}

	thread, _ := r.DB.GetThread(threadID)
	var workingDirs []string
	if thread != nil {
		workingDirs = thread.WorkingDirs
	}
	tools, _ := agent.BuildTools(agent.ToolDeps{
		Sandboxed:   thread != nil && thread.Sandboxed,
		WorkingDirs: workingDirs,
		Tasks:       agent.NewTaskStore(),
		ThreadID:    threadID,
		IsPlanMode: func() bool {
			st, _ := r.DB.GetAgentState(threadID)
			return st != nil && st.Mode == 2
		},
	})

	modelName := ""
	if mainCfg, ok := r.Config.Settings.Providers["main"]; ok {
		modelName = mainCfg.Model
	}

	runner, err := agent.NewRunner(r.Engine, r.Main, r.DB, threadID, tools, modelName)
	if err != nil {
		return nil, err
	}

	// Wire streaming deltas to GraphQL subscriptions
	runner.SetStreamCallback(func(delta, thinking string, done bool) {
		event := &StreamEvent{MessageID: threadID, Done: done}
		if delta != "" {
			event.Delta = &delta
		}
		if thinking != "" {
			event.Thinking = &thinking
		}
		r.publishStream(threadID, event)
	})

	r.runners[threadID] = &runnerEntry{runner: runner}
	return runner, nil
}

// stopRunner stops and removes a thread's runner.
func (r *Resolver) stopRunner(threadID string) {
	r.runnersMu.Lock()
	defer r.runnersMu.Unlock()
	if entry, ok := r.runners[threadID]; ok {
		if entry.cancel != nil {
			entry.cancel()
		}
		delete(r.runners, threadID)
	}
}

// subscribe/publish helpers for subscriptions
func (r *Resolver) subscribeStream(threadID string) chan *StreamEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan *StreamEvent, 16)
	r.streamSubs[threadID] = append(r.streamSubs[threadID], ch)
	return ch
}

func (r *Resolver) publishStream(threadID string, event *StreamEvent) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, ch := range r.streamSubs[threadID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (r *Resolver) subscribeAgentState(threadID string) chan *AgentState {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan *AgentState, 16)
	r.agentSubs[threadID] = append(r.agentSubs[threadID], ch)
	return ch
}

func (r *Resolver) publishAgentState(threadID string, state *AgentState) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, ch := range r.agentSubs[threadID] {
		select {
		case ch <- state:
		default:
		}
	}
}

func (r *Resolver) subscribeToolExec(threadID string) chan *ToolExecution {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan *ToolExecution, 16)
	r.toolSubs[threadID] = append(r.toolSubs[threadID], ch)
	return ch
}

func (r *Resolver) subscribeSubagent(threadID string) chan *SubagentProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan *SubagentProgress, 16)
	r.subagentSubs[threadID] = append(r.subagentSubs[threadID], ch)
	return ch
}

func (r *Resolver) subscribeThreadState() chan *ThreadStateEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan *ThreadStateEvent, 16)
	r.threadSubs = append(r.threadSubs, ch)
	return ch
}
