package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/elijahmontenegro/grudge/adkbridge"
	"github.com/elijahmontenegro/grudge/core"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/service/messages"
	"github.com/elijahmontenegro/grudge/service/storage"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// Runner orchestrates the agent loop for a thread via ADK.
// RRC integrates as the model.LLM implementation — ADK doesn't know.
type Runner struct {
	engine          *rrc.Engine
	completer       core.Completer
	db              *storage.DB
	threadID        string
	tools           []tool.Tool
	modelName       string
	instruction     string
	rerankerModelID string // for subagent forks to inherit
	adkRunner       *runner.Runner
	rrcLLM          *adkbridge.RRCLLM // stored to set scope per-call
	mu              sync.Mutex
	msgSeq          atomic.Int64 // monotonic message ID counter
	// currentTurnID is the active-discourse identity stamped on every
	// message stored during the in-flight SendMessage (the triggering
	// event plus the model/tool events it spawns). Minted at SendMessage
	// entry, read by indexMessage. Guarded by mu (held for the whole
	// SendMessage call); indexMessage only runs under that lock.
	currentTurnID string
	autoState     *AutonomousState
	// turnCancel holds the derived-context cancel for the in-flight
	// turn. Atomic pointer because r.mu is held for the entire
	// SendMessage call — CancelTurn has to read this without blocking
	// on mu. SendMessage Stores a fresh cancel at turn entry, Swaps
	// nil at exit so we don't fire a stale cancel on a subsequent
	// turn. Stop from the resolver fires whatever's currently live,
	// interrupting ADK's event iterator (which is running on the
	// derived ctx) and any in-flight HTTP call underneath.
	turnCancel  atomic.Pointer[context.CancelFunc]
	onStream    adkbridge.StreamCallback
	onSelection func(result *rrcv1.SelectionResult)
	onRound     func(round int, elapsed time.Duration)
	// Event handlers — service wires these to publish to GraphQL subscriptions
	OnToolCall   func(callID, toolName, args string)
	OnToolResult func(callID, toolName, result string, isError bool)
	// inserter centralizes message-store + chunk derivation + embed
	// enqueue. Both the graph layer's storeMessage paths and the
	// runner's indexMessage go through this single inserter so chunk
	// derivation lives in one place.
	inserter *messages.Inserter
	// scales grounds the token budget in provider-reported usage;
	// countText is the completer adapter's counting projection. Both
	// immutable after NewRunner and inherited by subagent forks (same
	// model, same store).
	scales    adkbridge.TokenScales
	countText func(*llmv1.LLMMessage) string
	// OnAutonomousError fires when a mid-run SendMessage fails during an
	// autonomous loop. Per spec the loop pauses rather than exits — the
	// handler is expected to pause autoState, publish Paused agent state
	// to the UI, and write the error as a system message in the thread
	// so the user can see what broke before deciding to resume or stop.
	OnAutonomousError func(err error)

	// Per-SendMessage trace scratch. Reset at SendMessage entry,
	// populated as the call progresses by OnAssemble (assembly stage
	// timings + counts) and by processEvents (first-event arrival +
	// persist accumulator). Read on SendMessage exit to build the
	// final TickTrace row. r.mu is held for the entire SendMessage
	// call, so no separate mutex is needed.
	tickAssemble       rrc.AssembleTelemetry
	tickAssembleSet    bool
	tickAssembleCount  int       // number of Assemble fires (>1 = context-overflow retry)
	tickRunStart       time.Time // adkRunner.Run entered (before any ADK work)
	tickFirstEvent     time.Time // first non-nil ADK event arrived
	tickFirstEventSeen bool
	tickPersistMs      int64 // accumulator: sum of InsertMessage durations inside processEvents
	tickCorpusSize     int   // size of the corpus RRC saw on this tick
	// The last usage-bearing model call's (prediction, reported)
	// triple, latched as one atomic event from OnUsage — so the
	// trace row's predicted/reported ratio always describes a single
	// call, even when the turn's final call exited without usage.
	tickUsagePredicted  int
	tickUsagePrompt     int
	tickUsageCompletion int
	// SetTickRound (called by the autonomous loop just before
	// SendMessage) records the round THIS tick belongs to. Reset to
	// 0 at SendMessage entry so non-autonomous sends record round=0.
	tickRound int
}

// SetTickRound is called by the autonomous loop immediately before
// SendMessage to tell the trace layer which round this tick will
// fill. Without this, persistTickTrace would have to read
// agent_state.round_count, which the autonomous loop only writes
// AFTER SendMessage returns — producing an off-by-one in every
// autonomous trace. The explicit setter sidesteps that ordering
// problem and keeps non-autonomous sends (which never call this) at
// round=0.
func (r *Runner) SetTickRound(round int) {
	r.tickRound = round
}

// CancelTurn aborts the turn currently being served by SendMessage, if
// any. The ctx passed down to ADK and the HTTP client is a child of
// the turn's derived ctx, so cancellation propagates through the
// adkRunner.Run iterator and into the in-flight completer call.
// Safe to call when no turn is active — noop.
func (r *Runner) CancelTurn() {
	if p := r.turnCancel.Load(); p != nil {
		(*p)()
	}
}

// SetStreamCallback sets the callback for streaming deltas (for subscription publishing).
func (r *Runner) SetStreamCallback(cb adkbridge.StreamCallback) {
	r.onStream = cb
}

// SetSelectionCallback sets the callback for RRC selection results (for introspection).
func (r *Runner) SetSelectionCallback(cb func(result *rrcv1.SelectionResult)) {
	r.onSelection = cb
}

// SetRoundCallback sets the callback for autonomous round progress.
func (r *Runner) SetRoundCallback(cb func(round int, elapsed time.Duration)) {
	r.onRound = cb
}

// NewRunner creates an agent runner for a thread.
//
// inserter centralizes message-store + chunk derivation + embed
// enqueue. The graph layer's storeMessage path uses the same
// inserter, so chunk derivation is identical across both insert
// pathways (no per-layer chunksFor duplicate).
//
// rerankerModelID identifies the score model inherited by subagent
// runners. Local-Context score persistence is wired into the shared engine.
//
// scales and countText ground token accounting for the completer
// model: scales converts the budget via the learned usage-grounded
// scale and receives per-call observations; countText is the
// adapter's counting projection. Either may be nil (ungrounded /
// count-everything).
func NewRunner(engine *rrc.Engine, completer core.Completer, db *storage.DB, threadID string, tools []tool.Tool, modelName, instruction, rerankerModelID string, inserter *messages.Inserter, scales adkbridge.TokenScales, countText func(*llmv1.LLMMessage) string) (*Runner, error) {
	r := &Runner{
		engine:          engine,
		completer:       completer,
		db:              db,
		threadID:        threadID,
		tools:           tools,
		modelName:       modelName,
		instruction:     instruction,
		rerankerModelID: rerankerModelID,
		inserter:        inserter,
		scales:          scales,
		countText:       countText,
	}

	// RRC-as-LLM: ADK calls this thinking it's an LLM. Engine owns its
	// own lock now; rrcLLM acquires it directly via engine.Lock /
	// Unlock — no shared mutex passed in.
	rrcLLM := adkbridge.NewRRCLLM(engine, completer, db, threadID, modelName)
	rrcLLM.Scales = scales
	rrcLLM.CountText = countText
	rrcLLM.OnUsage = func(predicted int, usage *llmv1.Usage) {
		// Latch the pair for the tick trace. Same concurrency contract
		// as tickAssemble: SendMessage holds r.mu for its duration and
		// resets these at entry.
		r.tickUsagePredicted = predicted
		r.tickUsagePrompt = int(usage.PromptTokens)
		r.tickUsageCompletion = int(usage.CompletionTokens)
	}
	rrcLLM.OnStream = func(delta, thinking string, done bool) {
		if r.onStream != nil {
			r.onStream(delta, thinking, done)
		}
	}
	rrcLLM.OnSelection = func(result *rrcv1.SelectionResult) {
		if r.onSelection != nil {
			r.onSelection(result)
		}
	}
	rrcLLM.OnEdge = func(edge *rrcv1.Edge) { db.InsertEdge(edge) }
	rrcLLM.OnAssemble = func(t rrc.AssembleTelemetry) {
		// Capture into the per-SendMessage trace scratch. SendMessage
		// holds r.mu for its entire duration and resets these fields at
		// entry, so the write here is concurrency-safe and per-call.
		//
		// Retry-aware accumulation: rrcllm's context-overflow loop can
		// invoke Assemble multiple times for a single tick. Stage
		// timings sum across attempts (the trace reflects total work
		// spent in Assemble for this tick). Final-state counts come
		// from the LAST attempt — that's the assembly whose wire
		// actually went to the model, so SelectedCount / TotalTokens
		// describe what got sent, not what got discarded mid-retry.
		r.tickAssembleCount++
		if !r.tickAssembleSet {
			r.tickAssemble = t
			r.tickAssembleSet = true
			return
		}
		r.tickAssemble.PrerequisiteSelection.DurationMs += t.PrerequisiteSelection.DurationMs
		r.tickAssemble.PrerequisiteSelection.CandidatesScored += t.PrerequisiteSelection.CandidatesScored
		r.tickAssemble.PrerequisiteSelection.Reranked += t.PrerequisiteSelection.Reranked
		r.tickAssemble.SelectMs += t.SelectMs
		r.tickAssemble.MMRMs += t.MMRMs
		r.tickAssemble.ShedMs += t.ShedMs
		// Final-state fields: take the last attempt's values.
		r.tickAssemble.PrerequisiteSelection.PriorsConsidered = t.PrerequisiteSelection.PriorsConsidered
		r.tickAssemble.PrerequisiteSelection.EdgesFormed = t.PrerequisiteSelection.EdgesFormed
		r.tickAssemble.SelectedCount = t.SelectedCount
		r.tickAssemble.LocalContextCount = t.LocalContextCount
		r.tickAssemble.ClosureCount = t.ClosureCount
		r.tickAssemble.SheddedCount = t.SheddedCount
		r.tickAssemble.TotalTokens = t.TotalTokens
		r.tickAssemble.EffectiveBudget = t.EffectiveBudget
	}
	r.rrcLLM = rrcLLM

	// Create the ADK agent with RRC as its model.
	// IncludeContentsNone: ADK sends only the current turn to the model.
	// RRC provides historical context via selection from the persistent Store.
	// Without this, ADK sends the full session history and RRC is redundant.
	const agentName = "grudge"
	rootAgent, err := llmagent.New(llmagent.Config{
		Name:            agentName,
		Description:     "",
		Instruction:     instruction,
		Model:           rrcLLM,
		Tools:           tools,
		IncludeContents: llmagent.IncludeContentsNone,
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{
			adkbridge.StripADKIdentity(agentName, ""),
		},
		AfterModelCallbacks: []llmagent.AfterModelCallback{
			r.afterModelCallback,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	// Create the ADK runner
	adkRunner, err := runner.New(runner.Config{
		AppName:           "grudge",
		Agent:             rootAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create runner: %w", err)
	}

	r.adkRunner = adkRunner
	return r, nil
}

// indexMessage stores a message with its derived chunks AND enqueues
// it for embedding. Single insert-side helper for the runner so the
// "chunk row exists in storage iff a vector row will be written to
// chunk_vectors" invariant holds for every stored message.
//
// Background: a prior bug split InsertMessage and the embed-enqueue
// across each call-site (userMsg / thinking flush / tool_call /
// tool_result / final assistant). The thinking-flush site forgot the
// enqueue — for the entire 14h novel run, every pre-tool-call thought
// landed in `chunks` but never got a vector in `chunk_vectors`. The
// chunks were visible to BM25 / count queries but invisible to RRC's
// vec0 KNN retrieval. The fix collapses both operations into one
// helper so every stored message follows the same indexing path.
//
// The per-tick persist accumulator includes only the synchronous
// SQLite write. The enqueue is fire-and-forget into the bounded
// worker pool; its cost is steady-state background load, not a tick
// stage.
func (r *Runner) indexMessage(msg *threadv1.Message) error {
	// Stamp the active-discourse identity. Every message stored during a
	// SendMessage — triggering event, thinking, tool calls, tool results,
	// final assistant text — shares the turn's id, so BuildActiveDiscourse
	// can recover the in-flight local discourse without fixed-N recency.
	if msg.TurnId == "" {
		msg.TurnId = r.currentTurnID
	}
	if r.inserter == nil {
		// Tests construct a Runner with no inserter wired so they can
		// exercise processEvents without a full runtime. Insert the
		// message without chunks (matches the pre-Inserter behavior
		// when the runner's engine was nil) and skip the embed enqueue.
		pStart := time.Now()
		err := r.db.InsertMessage(msg, nil)
		r.tickPersistMs += time.Since(pStart).Milliseconds()
		return err
	}
	pStart := time.Now()
	err := r.inserter.Insert(msg)
	r.tickPersistMs += time.Since(pStart).Milliseconds()
	return err
}

// nextMsgID returns a unique monotonic message ID for this runner's thread.
//
// Uses a nanosecond timestamp suffix rather than a sequential counter.
// Sequential counters collide on reopen-after-failure: len(corpus) can be
// lower than the highest surviving message ID (any INSERT that committed
// but whose enclosing round failed leaves a gap), so `counter = len+1`
// hits an existing row and UNIQUE-constraint-fails on the next insert.
// Nanoseconds sidestep the problem entirely — no state to resync.
func (r *Runner) nextMsgID() string {
	// atomic.Int64.Add(1) keeps ordering within a single process even if
	// two calls land inside the same nanosecond tick, which is rare but
	// observed in tight tool-call loops.
	return fmt.Sprintf("msg-%s-%d-%d", r.threadID, time.Now().UnixNano(), r.msgSeq.Add(1))
}

// SendMessage processes a user message through the ADK agent loop.
// ADK is the orchestrator — we iterate its events and surface tool calls,
// results, thinking, and text to the frontend via callbacks.
func (r *Runner) SendMessage(ctx context.Context, content string, scope threadv1.SelectionScope, attachments ...*threadv1.AttachmentContent) (msg *threadv1.Message, retErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tickStart := time.Now()
	r.tickAssemble = rrc.AssembleTelemetry{}
	r.tickAssembleSet = false
	r.tickAssembleCount = 0
	r.tickRunStart = time.Time{}
	r.tickFirstEvent = time.Time{}
	r.tickFirstEventSeen = false
	r.tickPersistMs = 0
	r.tickCorpusSize = 0
	r.tickUsagePredicted = 0
	r.tickUsagePrompt = 0
	r.tickUsageCompletion = 0
	// tickRound is NOT reset here — the autonomous loop sets it via
	// SetTickRound immediately before this call. Non-autonomous sends
	// never invoke SetTickRound, so the field is already 0 from the
	// last reset (or from struct zero-value).

	// Derive a cancellable ctx so the resolver's StopAgent mutation
	// can interrupt this turn mid-flight via Runner.CancelTurn(). The
	// derived ctx flows into adkRunner.Run below and down into the
	// HTTP client, so cancellation propagates end-to-end. The Store
	// publishes the cancel for CancelTurn; the defer Swaps nil on
	// return so a subsequent turn's CancelTurn can't fire a stale
	// cancel.
	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()
	r.turnCancel.Store(&turnCancel)
	defer r.turnCancel.Store(nil)
	ctx = turnCtx

	// Insert a trace row on every exit (success or error). Telemetry
	// must never block the agent — log+discard on storage errors. The
	// closure captures tickStart so the total wall clock is accurate
	// even on early returns.
	defer func() {
		r.persistTickTrace(tickStart, retErr)
	}()

	corpus, err := r.db.ThreadCorpus(r.threadID)
	if err != nil {
		return nil, fmt.Errorf("load corpus: %w", err)
	}
	r.tickCorpusSize = len(corpus)

	// Sync message counter to corpus length (idempotent on repeated calls)
	if cur := int64(len(corpus)); cur > r.msgSeq.Load() {
		r.msgSeq.Store(cur)
	}

	// Mint the active-discourse identity for this turn before the
	// triggering event is stored, so the trigger and every model/tool
	// event it spawns share it (indexMessage stamps it). Set it on the
	// RRCLLM so BuildActiveDiscourse recovers exactly this turn's
	// in-flight discourse as Local Context.
	r.currentTurnID = fmt.Sprintf("turn-%s-%d", r.threadID, time.Now().UnixNano())
	r.rrcLLM.CurrentTurnID = r.currentTurnID

	// Store the user Event only when it carries real content. Empty-
	// content autonomous ticks do not enter the Store — they are a
	// loop iteration, not an Event. Protocol-adherence shaping (e.g.
	// a provider requiring a non-empty terminal user turn) is handled
	// at the adapter via repositioning of already-persisted messages,
	// never by writing synthetic Events here.
	if content != "" || len(attachments) > 0 {
		blocks := pbtext.BlocksFromText(content)
		for _, a := range attachments {
			blocks = append(blocks, &threadv1.ContentBlock{
				Block: &threadv1.ContentBlock_Attachment{Attachment: a},
			})
		}
		userMsg := &threadv1.Message{
			Id:       r.nextMsgID(),
			Role:     threadv1.Role_ROLE_USER,
			Content:  blocks,
			Position: int64(len(corpus)),
			ThreadId: r.threadID,
		}
		if err := r.indexMessage(userMsg); err != nil {
			return nil, err
		}
		corpus = append(corpus, userMsg)
	}

	// Set scope on RRCLLM before ADK runs — the scope toggle reaches the engine here
	r.rrcLLM.Scope = scope

	// Empty-content calls (autonomous ticks) pass nil to ADK: its Run
	// API supports nil msg explicitly — appendMessageToSession returns
	// early on nil, findAgentToRun walks session history to continue
	// from the last agent event. No synthetic content injected.
	var genaiMsg *genai.Content
	if content != "" {
		genaiMsg = &genai.Content{
			Role:  "user",
			Parts: []*genai.Part{{Text: content}},
		}
	}

	r.tickRunStart = time.Now()
	events := r.adkRunner.Run(ctx, "user", r.threadID, genaiMsg, adkagent.RunConfig{
		StreamingMode: adkagent.StreamingModeSSE,
	})
	return r.processEvents(events)
}

// persistTickTrace builds a tick_traces row from the per-call scratch
// fields and inserts it. Telemetry never blocks the agent path —
// storage errors are logged and discarded. Called from SendMessage's
// deferred exit path so success and error both produce a trace row.
//
// Stage attribution:
//
//	t_rrc_prerequisite_selection_ms = Local Context serialization, KNN, rerank, gates, and edges
//	t_select_ms        = Engine.Select (graph walk + transitive reduction)
//	t_assemble_ms      = the rest of Assemble (MMR + budget shed + token estimate)
//	t_complete_ms      = wall clock from adkRunner.Run entry to first event,
//	                    minus the in-process Assemble stages — what's left is
//	                    "time spent waiting for the model's first byte"
//	t_stream_ms        = first event to processEvents return (includes interleaved persist)
//	t_persist_ms       = sum of per-InsertMessage durations (strict subset of stream)
func (r *Runner) persistTickTrace(tickStart time.Time, callErr error) {
	now := time.Now()
	totalMs := now.Sub(tickStart).Milliseconds()

	// Stage decomposition. Some fields may be zero if SendMessage
	// exited early (e.g., corpus load failed → no Assemble fired).
	var (
		onQueryMs  int64
		selectMs   int64
		assembleMs int64
		completeMs int64
		streamMs   int64
	)
	if r.tickAssembleSet {
		onQueryMs = r.tickAssemble.PrerequisiteSelection.DurationMs
		selectMs = r.tickAssemble.SelectMs
		// "Assemble" stage in the trace folds MMR + budget shed since
		// they're both post-Select assembly work and aren't worth
		// breaking out further for diagnostics.
		assembleMs = r.tickAssemble.MMRMs + r.tickAssemble.ShedMs
	}
	if !r.tickRunStart.IsZero() {
		if r.tickFirstEventSeen {
			// Time-to-first-event includes Assemble (which ran inside
			// adkRunner.Run via RRCLLM) plus the upstream network
			// round-trip. Subtract the Assemble stages so what's left
			// approximates pure network/model wait.
			runToFirst := r.tickFirstEvent.Sub(r.tickRunStart).Milliseconds()
			assemblyInsideRun := onQueryMs + selectMs + assembleMs
			completeMs = runToFirst - assemblyInsideRun
			if completeMs < 0 {
				// Clamp to zero — sub-millisecond stage timings can
				// arithmetic-underflow this subtraction without meaning
				// anything is wrong.
				completeMs = 0
			}
			streamMs = now.Sub(r.tickFirstEvent).Milliseconds()
		} else {
			// No events arrived (early error, ErrNoResponse) — record
			// the wait time as t_complete with stream=0.
			completeMs = now.Sub(r.tickRunStart).Milliseconds()
		}
	}

	trace := &storage.TickTrace{
		ThreadID:                   r.threadID,
		Round:                      r.tickRound,
		RRCPrerequisiteSelectionMs: onQueryMs,
		SelectMs:                   selectMs,
		AssembleMs:                 assembleMs,
		CompleteMs:                 completeMs,
		StreamMs:                   streamMs,
		PersistMs:                  r.tickPersistMs,
		TotalMs:                    totalMs,
		CompleterModel:             r.modelName,
		CorpusSize:                 r.tickCorpusSize,
		SelectedCount:              r.tickAssemble.SelectedCount,
		AssembledTokensEst:         r.tickAssemble.TotalTokens,
		UsagePredictedTokens:       r.tickUsagePredicted,
		UsagePromptTokens:          r.tickUsagePrompt,
		UsageCompletionTokens:      r.tickUsageCompletion,
	}
	if callErr != nil {
		trace.Errored = true
		trace.ErrorMsg = callErr.Error()
	}
	if r.tickAssembleCount > 1 {
		// Surface retries in the error_msg column even on success
		// paths — a tick that retried 3× has summed-time numbers that
		// would otherwise look anomalous without explanation.
		retryNote := fmt.Sprintf("assemble retries=%d (context-overflow re-Assemble loop)", r.tickAssembleCount-1)
		if trace.ErrorMsg == "" {
			trace.ErrorMsg = retryNote
		} else {
			trace.ErrorMsg = trace.ErrorMsg + "; " + retryNote
		}
	}
	if err := r.db.InsertTickTrace(trace); err != nil {
		log.Printf("[TickTrace] InsertTickTrace thread=%s: %v", r.threadID, err)
	}
}

// processEvents drains an ADK event iterator, persisting messages to
// the Store along the way and synthesizing the final assistant message
// if text/thinking accumulated. Extracted from SendMessage so the
// event-handling logic is test-drivable with a synthetic iterator —
// the dedup of ADK re-emission, the thinking-flush ordering around
// tool calls, and the error/content precedence at turn-end are all
// observable here.
func (r *Runner) processEvents(events iter.Seq2[*session.Event, error]) (*threadv1.Message, error) {
	var lastErr error
	var thinkingBuf strings.Builder
	// thinkingSig is the signature attached to the thinking text
	// currently in thinkingBuf, if any. A provider signature binds to
	// the EXACT text it was issued for — set alongside a
	// signature-bearing part, then storeThinking flushes immediately
	// (see below) so a later, unsigned continuation never accumulates
	// into an already-signed buffer under a stale signature.
	var thinkingSig []byte
	var textBuf strings.Builder
	var lastAssistantMsg *threadv1.Message

	// ADK occasionally emits the same FunctionCall or FunctionResponse
	// Part across multiple events in a single Run (observed: every tool
	// call stored twice in the corpus). Track IDs we've already persisted
	// so repeat Parts are ignored at the storage boundary rather than
	// polluting the corpus the RRC engine sees next round.
	seenCallIDs := make(map[string]string)
	seenResultIDs := make(map[string]bool)

	// storeThinking flushes accumulated thinking as its own message in
	// the turn, carrying whatever signature (if any) is bound to it.
	storeThinking := func() {
		if thinkingBuf.Len() == 0 {
			return
		}
		msg := &threadv1.Message{
			Id:       r.nextMsgID(),
			Role:     threadv1.Role_ROLE_ASSISTANT,
			Content:  []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: thinkingBuf.String(), Signature: thinkingSig}}}},
			Position: r.msgSeq.Load(),
			ThreadId: r.threadID,
		}
		if err := r.indexMessage(msg); err != nil {
			log.Printf("[Runner] indexMessage(thinking) thread=%s: %v", r.threadID, err)
		}
		thinkingBuf.Reset()
		thinkingSig = nil
	}

	for event, err := range events {
		if err != nil {
			log.Printf("[Runner] ADK event error: %v", err)
			lastErr = err
			continue
		}

		eventContent := event.LLMResponse.Content
		if eventContent == nil {
			continue
		}

		// Record the first event for the tick trace's t_complete_ms /
		// t_stream_ms split. "First event" means the first non-nil
		// content yielded by ADK — that's the earliest point we know
		// the upstream model started responding. Only set once per
		// SendMessage; subsequent events are still part of the same
		// stream and don't restart the clock.
		if !r.tickFirstEventSeen {
			r.tickFirstEvent = time.Now()
			r.tickFirstEventSeen = true
		}

		for _, part := range eventContent.Parts {
			if part.FunctionCall != nil {
				fc := part.FunctionCall
				// Guard against ADK re-emitting the same call Part.
				if fc.ID != "" {
					if _, seen := seenCallIDs[fc.ID]; seen {
						continue
					}
				}
				if fc.ID != "" {
					seenCallIDs[fc.ID] = fc.Name
				}
				// Flush thinking BEFORE the tool call so ordering is correct
				storeThinking()

				argsJSON := "{}"
				if fc.Args != nil {
					if b, err := json.Marshal(fc.Args); err == nil {
						argsJSON = string(b)
					}
				}
				toolCallMsg := &threadv1.Message{
					Id:       r.nextMsgID(),
					Role:     threadv1.Role_ROLE_ASSISTANT,
					Content:  []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{Id: fc.ID, Name: fc.Name, Arguments: argsJSON, Signature: part.ThoughtSignature}}}},
					Position: r.msgSeq.Load(),
					ThreadId: r.threadID,
				}
				if err := r.indexMessage(toolCallMsg); err != nil {
					log.Printf("[Runner] indexMessage(tool_call %s) thread=%s: %v", fc.Name, r.threadID, err)
				}
				if r.OnToolCall != nil {
					r.OnToolCall(fc.ID, fc.Name, argsJSON)
				}
			}

			if part.FunctionResponse != nil {
				fr := part.FunctionResponse
				if fr.ID != "" && seenResultIDs[fr.ID] {
					continue
				}
				if fr.ID != "" {
					seenResultIDs[fr.ID] = true
				}
				resultText := ""
				if fr.Response != nil {
					if s, ok := fr.Response["output"].(string); ok {
						resultText = s
					} else if b, err := json.Marshal(fr.Response); err == nil {
						resultText = string(b)
					}
				}
				toolResultMsg := &threadv1.Message{
					Id:       r.nextMsgID(),
					Role:     threadv1.Role_ROLE_ASSISTANT,
					Content:  []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{ToolCallId: fr.ID, Content: resultText}}}},
					Position: r.msgSeq.Load(),
					ThreadId: r.threadID,
				}
				if err := r.indexMessage(toolResultMsg); err != nil {
					log.Printf("[Runner] indexMessage(tool_result %s) thread=%s: %v", fr.Name, r.threadID, err)
				}
				if r.OnToolResult != nil {
					r.OnToolResult(fr.ID, fr.Name, resultText, false)
				}
			}

			// A thought part with no text can still carry a signature —
			// e.g. the bridge's zero-text terminator that closes a
			// signed Anthropic thinking block. Widen past the
			// text != "" gate so that signature is never silently
			// dropped.
			if part.FunctionCall == nil && part.Thought && (part.Text != "" || len(part.ThoughtSignature) > 0) {
				thinkingBuf.WriteString(part.Text)
				if len(part.ThoughtSignature) > 0 {
					// The signature binds to exactly the text
					// accumulated up to this point — flush now rather
					// than let further unsigned thinking extend the
					// buffer under a signature that no longer matches
					// its full contents.
					thinkingSig = part.ThoughtSignature
					storeThinking()
				}
			} else if part.Text != "" && part.FunctionCall == nil {
				textBuf.WriteString(part.Text)
			}
		}
	}

	// A cancelled or interrupted tool execution can end the event
	// stream after its call was persisted but before ADK emits a
	// FunctionResponse. Preserve the exact protocol relation by
	// recording an explicit error result under the original call ID.
	for callID, toolName := range seenCallIDs {
		if callID == "" || seenResultIDs[callID] {
			continue
		}
		content := "Tool execution ended without a result."
		if lastErr != nil {
			content = "Tool execution failed: " + lastErr.Error()
		}
		toolResultMsg := &threadv1.Message{
			Id:   r.nextMsgID(),
			Role: threadv1.Role_ROLE_ASSISTANT,
			Content: []*threadv1.ContentBlock{{Block: &threadv1.ContentBlock_ToolResult{
				ToolResult: &threadv1.ToolResultContent{
					ToolCallId: callID,
					Content:    content,
					IsError:    true,
				},
			}}},
			Position: r.msgSeq.Load(),
			ThreadId: r.threadID,
		}
		if err := r.indexMessage(toolResultMsg); err != nil {
			return nil, err
		}
		if r.OnToolResult != nil {
			r.OnToolResult(callID, toolName, content, true)
		}
	}

	// Store final thinking + text as the last message in the turn
	var finalContent []*threadv1.ContentBlock
	if thinkingBuf.Len() > 0 {
		finalContent = append(finalContent, &threadv1.ContentBlock{
			Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{Text: thinkingBuf.String(), Signature: thinkingSig}},
		})
	}
	if textBuf.Len() > 0 {
		finalContent = append(finalContent, &threadv1.ContentBlock{
			Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: textBuf.String()}},
		})
	}
	if len(finalContent) > 0 {
		lastAssistantMsg = &threadv1.Message{
			Id:       r.nextMsgID(),
			Role:     threadv1.Role_ROLE_ASSISTANT,
			Content:  finalContent,
			Position: r.msgSeq.Load(),
			ThreadId: r.threadID,
		}
		if err := r.indexMessage(lastAssistantMsg); err != nil {
			return nil, err
		}
		return lastAssistantMsg, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("agent error: %w", lastErr)
	}
	return nil, ErrNoResponse
}

// ErrNoResponse is returned when ADK's Run yielded zero events and
// no error. In chat/plan modes this is a genuine problem and
// surfaces to the user. In autonomous mode it's expected behavior
// — the model having nothing to add on a given tick is not a
// failure, and autonomous treats it as a no-op continuation rather
// than pausing with an error banner. The sentinel lets the
// autonomous loop distinguish this specifically from real errors.
var ErrNoResponse = errors.New("no response from agent")

// afterModelCallback extracts thinking blocks for carry-forward after every LLM call.
func (r *Runner) afterModelCallback(
	ctx adkagent.CallbackContext,
	llmResponse *model.LLMResponse,
	llmResponseError error,
) (*model.LLMResponse, error) {
	if llmResponseError != nil || llmResponse == nil || llmResponse.Content == nil {
		return llmResponse, llmResponseError
	}

	// Model and tool events are stored by the runner's message loop.
	// A later outbound call sees those events through Local Context and
	// builds a fresh serialized Local Context, so no separate carry-forward pass
	// is needed here.
	_ = llmResponse.Content
	return llmResponse, nil
}
