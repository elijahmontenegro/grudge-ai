package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/adapter/tei"
	completerretry "github.com/emontenegr/spidey/core/completer/retry"
	"github.com/emontenegr/spidey/core/retry"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/adoc"
	"github.com/emontenegr/spidey/service/agent"
	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/hooks"
	"github.com/emontenegr/spidey/service/prompt"
	"github.com/emontenegr/spidey/service/sandbox"
	"github.com/emontenegr/spidey/service/search"
	"github.com/emontenegr/spidey/service/skills"
	"github.com/emontenegr/spidey/service/storage"
	"google.golang.org/adk/tool"
)

// parseAnswerPayload interprets the raw answer string the frontend sent
// to answerQuestion. For multi-question tool calls the frontend sends a
// JSON object keyed by question text; for a single question it may send
// either a JSON map or a plain string, so both shapes are accepted.
// Missing questions resolve to empty strings so the model sees a
// complete map.
func parseAnswerPayload(raw string, questions []agent.AskUserQuestion) map[string]string {
	var asMap map[string]string
	if err := json.Unmarshal([]byte(raw), &asMap); err == nil && asMap != nil {
		out := make(map[string]string, len(questions))
		for _, q := range questions {
			out[q.Question] = asMap[q.Question]
		}
		return out
	}
	out := make(map[string]string, len(questions))
	for i, q := range questions {
		if i == 0 {
			out[q.Question] = raw
		} else {
			out[q.Question] = ""
		}
	}
	return out
}

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

	// Active runners per thread — for autonomous/plan mode
	runners   map[string]*runnerEntry
	runnersMu sync.Mutex

	// Per-thread subscription channels
	streamSubs   map[string][]chan *StreamEvent
	agentSubs    map[string][]chan *AgentState
	toolSubs     map[string][]chan *ToolExecution
	subagentSubs map[string][]chan *SubagentProgress
	threadSubs   []chan *ThreadStateEvent
	mu           sync.RWMutex
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
		runners:          make(map[string]*runnerEntry),
		streamSubs:       make(map[string][]chan *StreamEvent),
		agentSubs:        make(map[string][]chan *AgentState),
		toolSubs:         make(map[string][]chan *ToolExecution),
		subagentSubs:     make(map[string][]chan *SubagentProgress),
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
	r.runnersMu.Lock()
	for tid := range r.runners {
		entry := r.runners[tid]
		if entry.cancel != nil {
			entry.cancel()
		}
		if entry.askCh != nil {
			close(entry.askCh)
		}
		delete(r.runners, tid)
	}
	r.runnersMu.Unlock()

	return nil
}

// runnerEntry tracks an active runner and its cancellation.
type runnerEntry struct {
	runner         *agent.Runner
	cancel         context.CancelFunc
	parentThreadID string              // non-empty for subagent forks — merge on stop
	askCh          chan agent.AskRequest // closed on stop to terminate the ask goroutine
}

// getOrCreateRunner returns the active runner for a thread, creating one if needed.
func (r *Resolver) getOrCreateRunner(threadID string) (*agent.Runner, error) {
	r.runnersMu.Lock()
	defer r.runnersMu.Unlock()

	if entry, ok := r.runners[threadID]; ok {
		return entry.runner, nil
	}

	thread, err := r.DB.GetThread(threadID)
	if err != nil {
		return nil, fmt.Errorf("get thread %s: %w", threadID, err)
	}
	workingDirs := thread.WorkingDirs

	// Per-thread sandbox workspace. Created lazily on first runner
	// construction; layout mirrors plans/plan-{tid}/ so the two
	// thread-local resources sit side by side in the data dir.
	// Cheap to compute even when the thread isn't sandboxed — the
	// directory just stays empty.
	workspace, err := sandbox.WorkspaceDir(r.Config.DataDir, threadID)
	if err != nil {
		return nil, fmt.Errorf("workspace dir: %w", err)
	}

	// AskUserQuestion channel — goroutine reads from it, stopRunner closes it.
	// Each request carries structured args (questions / headers / options /
	// multiSelect). The payload published to the frontend IS the
	// JSON-marshaled args so the UI has everything it needs to render
	// option lists without a second round-trip. The frontend answers via
	// answerQuestion(callId, answer) where `answer` is JSON of the
	// question→answer map (or plain text when there's only one question).
	askCh := make(chan agent.AskRequest, 1)
	go func() {
		for req := range askCh {
			callID := fmt.Sprintf("ask-%d", time.Now().UnixNano())
			argsJSON, err := json.Marshal(req.Args)
			if err != nil {
				// Shouldn't happen — Args is a plain struct. Fall back to
				// empty-object so the frontend still renders something.
				argsJSON = []byte(`{"questions":[]}`)
			}
			r.publishToolExec(threadID, &ToolExecution{
				ThreadID: threadID, CallID: callID, ToolName: "AskUserQuestion",
				Arguments: string(argsJSON), Status: "waiting_for_user",
			})
			respCh := make(chan string, 1)
			r.pendingApprovalsMu.Lock()
			r.pendingAnswers[callID] = respCh
			r.pendingApprovalsMu.Unlock()

			timer := time.NewTimer(5 * time.Minute)
			var answers map[string]string
			select {
			case <-timer.C:
				answers = map[string]string{}
			case raw := <-respCh:
				answers = parseAnswerPayload(raw, req.Args.Questions)
			}
			timer.Stop()

			req.RespCh <- answers

			r.publishToolExec(threadID, &ToolExecution{
				ThreadID: threadID, CallID: callID, ToolName: "AskUserQuestion",
				Arguments: string(argsJSON), Status: "completed",
			})
			r.pendingApprovalsMu.Lock()
			delete(r.pendingAnswers, callID)
			r.pendingApprovalsMu.Unlock()
		}
	}()

	// Convert loaded skills to agent.SkillDef
	var skillDefs []agent.SkillDef
	for _, s := range r.Skills {
		skillDefs = append(skillDefs, agent.SkillDef{Name: s.Name, Content: s.Content})
	}

	// Search provider URL from settings (SearXNG or compatible)
	searchURL := ""
	if searchCfg, ok := r.Config.Settings.Providers["search"]; ok {
		searchURL = searchCfg.BaseURL
	}

	tools, err := agent.BuildTools(agent.ToolDeps{
		Sandboxed:   thread.Sandboxed,
		Workspace:   workspace,
		WorkingDirs: workingDirs,
		Tasks:       agent.NewTaskStore(),
		ThreadID:    threadID,
		Skills:      skillDefs,
		SearchURL:   searchURL,
		IsPlanMode: func() bool {
			st, _ := r.DB.GetAgentState(threadID)
			return st != nil && st.Mode == storage.AgentModePlan
		},
		AgentState: func(mode string) error {
			var m storage.AgentMode
			switch mode {
			case "plan":
				m = storage.AgentModePlan
			case "autonomous":
				m = storage.AgentModeAutonomous
			}
			// Narrow UPDATE so an EnterPlan/ExitPlan call during an
			// autonomous run doesn't wipe StartedAt/DurationLimit/RoundCount
			// (the in-memory loop would keep running, but GetAgentState
			// readers would see stale metadata).
			if err := r.DB.SetAgentStatusAndMode(threadID, storage.AgentStatusRunning, m); err != nil {
				return err
			}
			gqlMode := AgentModeNormal
			if m == storage.AgentModeAutonomous {
				gqlMode = AgentModeAutonomous
			} else if m == storage.AgentModePlan {
				gqlMode = AgentModePlan
			}
			// Preserve current planContent across publishes. ExitPlan fires
			// OnPlanContent then AgentState("normal") in sequence — without
			// this, the second publish's nil planContent clobbers the first,
			// and the frontend PlanPanel never renders because the last
			// subscription event it sees has no plan.
			r.planContentMu.RLock()
			var planPtr *string
			if pc, ok := r.planContent[threadID]; ok && pc != "" {
				copied := pc
				planPtr = &copied
			}
			r.planContentMu.RUnlock()
			r.publishAgentState(threadID, &AgentState{
				ThreadID: threadID, Status: AgentStatusRunning, Mode: gqlMode,
				PlanContent: planPtr,
			})
			return nil
		},
		CompileAdoc: func(path string) (string, error) {
			return adoc.Compile(path)
		},
		PlanDir: filepath.Join(r.Config.DataDir, "plans"),
		SpawnAgent: func(ctx context.Context, task, forkID string) (string, error) {
			entry, ok := r.runners[threadID]
			if !ok {
				return "", fmt.Errorf("no runner for thread %s", threadID)
			}
			fork, err := entry.runner.SpawnSubagent(ctx, task, forkID)
			if err != nil {
				return "", err
			}
			r.runners[forkID] = &runnerEntry{
				runner:         fork,
				parentThreadID: threadID,
			}
			r.publishSubagent(threadID, &SubagentProgress{
				ThreadID: threadID, ForkThreadID: forkID,
				Task: task, Status: "running", RoundCount: 0,
			})
			return "Subagent started: " + forkID, nil
		},
		SendToAgent: func(ctx context.Context, agentID, message string) (string, error) {
			entry, ok := r.runners[agentID]
			if !ok {
				return "", fmt.Errorf("no runner for agent %s", agentID)
			}
			resp, err := entry.runner.SendMessage(ctx, message, pb.SelectionScope_SELECTION_SCOPE_THREAD)
			if err != nil {
				return "", err
			}
			// Incremental merge after each subagent interaction — edges and scores
			// accumulate in the parent. Merge is idempotent (scores use INSERT OR REPLACE).
			if entry.parentThreadID != "" {
				if parentEntry, pOk := r.runners[entry.parentThreadID]; pOk {
					parentEntry.runner.MergeSubagent(entry.runner)
				}
			}
			return fmt.Sprintf("Response from %s: %d blocks", agentID, len(resp.Content)), nil
		},
		ApprovalFn: func(ctx context.Context, callID, toolName, args string) (bool, error) {
			// Publish pending tool execution to frontend
			r.publishToolExec(threadID, &ToolExecution{
				ThreadID: threadID, CallID: callID, ToolName: toolName,
				Arguments: args, Status: "pending",
			})
			// Create approval channel and wait (5 minute timeout to prevent goroutine leak)
			ch := make(chan bool, 1)
			r.pendingApprovalsMu.Lock()
			r.pendingApprovals[callID] = ch
			r.pendingThreadIDs[callID] = threadID
			r.pendingApprovalsMu.Unlock()
			defer func() {
				r.pendingApprovalsMu.Lock()
				delete(r.pendingApprovals, callID)
				delete(r.pendingThreadIDs, callID)
				r.pendingApprovalsMu.Unlock()
			}()
			timer := time.NewTimer(5 * time.Minute)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				r.publishToolExec(threadID, &ToolExecution{
					ThreadID: threadID, CallID: callID, ToolName: toolName,
					Arguments: args, Status: "cancelled",
				})
				return false, ctx.Err()
			case <-timer.C:
				r.publishToolExec(threadID, &ToolExecution{
					ThreadID: threadID, CallID: callID, ToolName: toolName,
					Arguments: args, Status: "timeout",
				})
				return false, fmt.Errorf("tool approval timed out after 5 minutes")
			case approved := <-ch:
				status := "approved"
				if !approved {
					status = "denied"
				}
				r.publishToolExec(threadID, &ToolExecution{
					ThreadID: threadID, CallID: callID, ToolName: toolName,
					Arguments: args, Status: status,
				})
				return approved, nil
			}
		},
		HookFn: func(ctx context.Context, event, toolName string) error {
			if r.Hooks == nil {
				return nil
			}
			result, err := r.Hooks.Fire(ctx, event, toolName)
			if err != nil {
				return err
			}
			if result != nil && result.Blocked {
				return fmt.Errorf("hook blocked %s on %s: %s", event, toolName, result.Output)
			}
			return nil
		},
		Permissions: r.Config.Settings.Permissions,
		AskCh: askCh,
		OnPlanContent: func(content string) {
			r.planContentMu.Lock()
			r.planContent[threadID] = content
			r.planContentMu.Unlock()
			// Publish updated agent state with plan content. Mode stays
			// Plan — ExitPlan used to flip to Normal here but now it just
			// surfaces the plan. The user flips mode explicitly via
			// approvePlan (to Normal/Autonomous) or keeps iterating (stays
			// Plan). Status stays Idle since the model's turn is either
			// about to end naturally or the runner is between rounds.
			pc := content
			r.publishAgentState(threadID, &AgentState{
				ThreadID: threadID, Status: AgentStatusIdle, Mode: AgentModePlan,
				PlanContent: &pc,
			})
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build tools: %w", err)
	}

	// Append MCP tools
	tools = append(tools, r.MCPTools...)
	modelName := ""
	if mainCfg, ok := r.Config.Settings.Providers["main"]; ok {
		modelName = mainCfg.Model
	}

	// Assemble system prompt from templates
	instruction := ""
	if r.Assembler != nil {
		threadName := thread.Name
		st, _ := r.DB.GetAgentState(threadID)
		mode := "normal"
		if st != nil {
			switch st.Mode {
			case storage.AgentModeAutonomous:
				mode = "autonomous"
			case storage.AgentModePlan:
				mode = "plan"
			}
		}
		spideyMD := prompt.LoadSpideyMD(workingDirs)
		planDir, err := planDirForThread(r.Config.DataDir, threadID)
		if err != nil {
			return nil, fmt.Errorf("plan dir: %w", err)
		}
		assembled, err := r.Assembler.Assemble(prompt.TemplateData{
			UserName:    r.Config.Settings.GetUserName(),
			ThreadName:  threadName,
			Sandboxed:   thread.Sandboxed,
			WorkingDirs: workingDirs,
			SpideyMD:    spideyMD,
			PlanDir:     planDir,
			CurrentTime: time.Now().Format(time.RFC3339),
			Mode:        mode,
		})
		if err != nil {
			return nil, fmt.Errorf("assemble prompt: %w", err)
		} else {
			instruction = assembled
		}
	} else {
		return nil, fmt.Errorf("prompt assembler not initialized — templates directory missing")
	}

	// Wrap the main completer with retry. The retry wrapper closes over
	// threadID so transient failures surface as RetryStatus events on
	// THIS thread's AgentState subscription — the UI renders the
	// indicator contextually. Principled retry per the retry package:
	// bounded attempts, exponential backoff, auth/4xx errors surface
	// immediately without consuming retry budget.
	mainWithRetry := completerretry.New(r.Main, retry.DefaultPolicy(), func(ev retry.Event) {
		r.publishRetryStatus(threadID, ev)
	})
	rerankerModelID := ""
	if r.Config != nil {
		if cls, ok := r.Config.Settings.Providers["classifier"]; ok {
			rerankerModelID = cls.Model
		}
	}
	runner, err := agent.NewRunner(r.Engine, mainWithRetry, r.DB, threadID, tools, modelName, instruction, rerankerModelID)
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

	// Wire selection results for introspection + outbound citation tracking.
	// The in-memory map remains the hot-path read; persistence is the
	// durable record that survives restart so historical turns can be
	// audited. Write-through on every callback — same semantics as the
	// score-cache persister hook.
	runner.SetSelectionCallback(func(result *pb.SelectionResult) {
		r.mu.Lock()
		r.selectionResults[result.EventId] = result
		r.latestSelection[threadID] = result.EventId
		for _, sel := range result.Selected {
			r.citationCount[sel.MessageId]++
		}
		r.mu.Unlock()

		// Event IDs are synthesized as sel-<target_message_id> in the
		// engine. Strip the prefix to recover the target for the
		// selections table FK.
		targetID := result.EventId
		if len(targetID) > 4 && targetID[:4] == "sel-" {
			targetID = targetID[4:]
		}
		if err := r.DB.SaveSelection(result, targetID, threadID); err != nil {
			log.Printf("SaveSelection(event=%s target=%s): %v", result.EventId, targetID, err)
		}
	})

	// Wire round callback for autonomous mode
	runner.SetRoundCallback(func(round int, elapsed time.Duration) {
		elapsedStr := elapsed.Truncate(time.Second).String()
		durLimit := ""
		if st, _ := r.DB.GetAgentState(threadID); st != nil {
			durLimit = st.DurationLimit
		}
		r.publishAgentState(threadID, &AgentState{
			ThreadID: threadID, Status: AgentStatusRunning, Mode: AgentModeAutonomous,
			RoundCount: round, ElapsedTime: &elapsedStr, DurationLimit: &durLimit,
		})
		// Round 93 fix: narrow UPDATE so we don't wipe StartedAt/DurationLimit
		// set by StartAutonomous at the beginning of the run.
		r.DB.SetAgentRoundCount(threadID, round)
	})

	// Wire tool call/result events to GraphQL subscriptions
	runner.OnToolCall = func(callID, toolName, args string) {
		r.publishToolExec(threadID, &ToolExecution{
			ThreadID: threadID, CallID: callID, ToolName: toolName,
			Arguments: args, Status: "running",
		})
	}
	runner.OnToolResult = func(callID, toolName, result string, isError bool) {
		status := "completed"
		if isError {
			status = "failed"
		}
		r.publishToolExec(threadID, &ToolExecution{
			ThreadID: threadID, CallID: callID, ToolName: toolName,
			Arguments: "", Status: status, Result: &result, IsError: &isError,
		})
	}

	// Embed-on-arrival for search indexing. Each message's chunks get
	// embedded as soon as it's stored so RRC's cosine prefilter has
	// vectors ready by the time the next OnMessage call fires.
	// Routes through the bounded EmbedQueue — see the EmbedQueue
	// doc comment for why the raw `go searcher.EmbedMessageChunks`
	// pattern was insufficient. Reads via r.EmbedQueue() which is
	// atomic so a settings reload can swap the queue without racing
	// the hot path.
	runner.OnMessageStored = func(msgID, _text string) {
		if q := r.EmbedQueue(); q != nil {
			q.Enqueue(msgID)
		}
	}

	// Per spec (spec/web/MANIFEST.adoc:187): autonomous errors pause the
	// run rather than exit it. Handler writes the error as a system
	// message so it's visible in-thread, pauses the autoState so the
	// loop blocks at the next waitIfPaused, and publishes Paused to the
	// UI. Resume (with optional correction) picks up from there.
	runner.OnAutonomousError = func(err error) {
		// Previously this wrote the error as a SYSTEM-role pb.Message
		// into the thread corpus so the UI would see it. That was
		// double-wrong:
		//  1. UX — an error banner landing in the conversation stream
		//     as if it were a chat message conflates transient
		//     operational state with durable dialog content.
		//  2. Corpus pollution — permanent. The error message becomes
		//     a Selection candidate for future rounds; the reranker
		//     scores it similarly to other "something broke" material
		//     and surfaces it as a phantom prereq. Once in the corpus,
		//     it never leaves, and its noise compounds across future
		//     queries.
		//
		// Correct path: publish the error through the AgentState
		// subscription as a retry-final event. The UI already renders
		// RetryStatus{Final:true, Error:...} as a pause banner with
		// the error text — no corpus write needed.
		log.Printf("[Autonomous] mid-run error on %s: %v — pausing", threadID, err)
		runner.PauseAutonomous()
		_ = r.DB.SetAgentStatus(threadID, storage.AgentStatusPaused)
		errMsg := err.Error()
		r.publishAgentState(threadID, &AgentState{
			ThreadID: threadID, Status: AgentStatusPaused, Mode: AgentModeAutonomous,
			Retry: &RetryStatus{
				Attempt: 1, MaxAttempts: 1,
				Error: &errMsg,
				Final: true,
			},
		})
	}

	r.runners[threadID] = &runnerEntry{runner: runner, askCh: askCh}
	return runner, nil
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
	r.runnersMu.Lock()
	defer r.runnersMu.Unlock()
	entry, ok := r.runners[threadID]
	if !ok {
		return
	}
	// Merge subagent fork back into parent
	if entry.parentThreadID != "" {
		if parentEntry, pOk := r.runners[entry.parentThreadID]; pOk {
			if err := parentEntry.runner.MergeSubagent(entry.runner); err != nil {
				log.Printf("Subagent merge %s → %s: %v", threadID, entry.parentThreadID, err)
			}
		}
		r.publishSubagent(entry.parentThreadID, &SubagentProgress{
			ThreadID: entry.parentThreadID, ForkThreadID: threadID,
			Status: "completed",
		})
	}
	// Cancel the in-flight turn (if any) before cancelling the
	// autonomous-loop ctx. The turn's ctx is a child of whatever
	// caller ctx ADK is running under; entry.cancel is the
	// autonomous outer loop's ctx. A non-autonomous streaming send
	// has no entry.cancel — the turn-cancel is the only lever that
	// reaches it.
	if entry.runner != nil {
		entry.runner.CancelTurn()
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	if entry.askCh != nil {
		close(entry.askCh)
	}
	delete(r.runners, threadID)
}

// unsubscribeOnDone waits for ctx.Done, then calls cleanup under the lock.
func unsubscribeOnDone(ctx context.Context, cleanup func()) {
	go func() {
		<-ctx.Done()
		cleanup()
	}()
}

// removeChan removes ch from a channel slice. Does NOT lock — caller must hold the lock.
func removeChan[T comparable](subs []chan T, ch chan T) []chan T {
	for i, s := range subs {
		if s == ch {
			return append(subs[:i], subs[i+1:]...)
		}
	}
	return subs
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
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan *ToolExecution, 16)
	r.toolSubs[threadID] = append(r.toolSubs[threadID], ch)
	return ch
}

func (r *Resolver) publishToolExec(threadID string, event *ToolExecution) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, ch := range r.toolSubs[threadID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (r *Resolver) publishSubagent(threadID string, event *SubagentProgress) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, ch := range r.subagentSubs[threadID] {
		select {
		case ch <- event:
		default:
		}
	}
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

func (r *Resolver) publishThreadState(event *ThreadStateEvent) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, ch := range r.threadSubs {
		select {
		case ch <- event:
		default:
		}
	}
}
