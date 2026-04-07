package graph

import (
	"sync"

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
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
		streamSubs:   make(map[string][]chan *StreamEvent),
		agentSubs:    make(map[string][]chan *AgentState),
		toolSubs:     make(map[string][]chan *ToolExecution),
		subagentSubs: make(map[string][]chan *SubagentProgress),
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
