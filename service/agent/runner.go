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

	"github.com/emontenegr/grudge/core"
	pb "github.com/emontenegr/grudge/proto/gen/go/grudge/v1"
	"github.com/emontenegr/grudge/rrc"
	adk "github.com/emontenegr/grudge/service/agent/internal/adk"
	"github.com/emontenegr/grudge/service/messages"
	"github.com/emontenegr/grudge/service/storage"

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
	rrcLLM          *adk.RRCLLM // stored to set scope per-call
	mu          sync.Mutex
	msgSeq      atomic.Int64 // monotonic message ID counter
	autoState   *AutonomousState
	// turnCancel holds the derived-context cancel for the in-flight
	// turn. Atomic pointer because r.mu is held for the entire
	// SendMessage call — CancelTurn has to read this without blocking
	// on mu. SendMessage Stores a fresh cancel at turn entry, Swaps
	// nil at exit so we don't fire a stale cancel on a subsequent
	// turn. Stop from the resolver fires whatever's currently live,
	// interrupting ADK's event iterator (which is running on the
	// derived ctx) and any in-flight HTTP call underneath.
	turnCancel  atomic.Pointer[context.CancelFunc]
	onStream    adk.StreamCallback
	onSelection func(result *pb.SelectionResult)
	onRound     func(round int, elapsed time.Duration)
	// Event handlers — service wires these to publish to GraphQL subscriptions
	OnToolCall   func(callID, toolName, args string)
	OnToolResult func(callID, toolName, result string, isError bool)
	// inserter centralizes message-store + chunk derivation + embed
	// enqueue. Both the graph layer's storeMessage paths and the
	// runner's indexMessage go through this single inserter so chunk
	// derivation lives in one place.
	inserter *messages.Inserter
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
func (r *Runner) SetStreamCallback(cb adk.StreamCallback) {
	r.onStream = cb
}

// SetSelectionCallback sets the callback for RRC selection results (for introspection).
func (r *Runner) SetSelectionCallback(cb func(result *pb.SelectionResult)) {
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
// rerankerModelID is the id under which reranker chunk-pair scores
// are persisted in the scores table. Passed through to RRCLLM so
// its protocol-rectification resolver can score Store-resident
// candidates against the current query.
func NewRunner(engine *rrc.Engine, completer core.Completer, db *storage.DB, threadID string, tools []tool.Tool, modelName, instruction, rerankerModelID string, inserter *messages.Inserter) (*Runner, error) {
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
	}

	// RRC-as-LLM: ADK calls this thinking it's an LLM. Engine owns its
	// own lock now; rrcLLM acquires it directly via engine.Lock /
	// Unlock — no shared mutex passed in.
	rrcLLM := adk.NewRRCLLM(engine, completer, db, threadID, modelName, rerankerModelID)
	rrcLLM.OnStream = func(delta, thinking string, done bool) {
		if r.onStream != nil {
			r.onStream(delta, thinking, done)
		}
	}
	rrcLLM.OnSelection = func(result *pb.SelectionResult) {
		if r.onSelection != nil {
			r.onSelection(result)
		}
	}
	rrcLLM.OnEdge = func(edge *pb.Edge) { db.InsertEdge(edge) }
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
		r.tickAssemble.OnMessage.DurationMs += t.OnMessage.DurationMs
		r.tickAssemble.OnMessage.CandidatesScored += t.OnMessage.CandidatesScored
		r.tickAssemble.OnMessage.Reranked += t.OnMessage.Reranked
		r.tickAssemble.SelectMs += t.SelectMs
		r.tickAssemble.MMRMs += t.MMRMs
		r.tickAssemble.ShedMs += t.ShedMs
		// Final-state fields: take the last attempt's values.
		r.tickAssemble.OnMessage.PriorsConsidered = t.OnMessage.PriorsConsidered
		r.tickAssemble.OnMessage.EdgesFormed = t.OnMessage.EdgesFormed
		r.tickAssemble.SelectedCount = t.SelectedCount
		r.tickAssemble.RadiusCount = t.RadiusCount
		r.tickAssemble.RectifiedCount = t.RectifiedCount
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
			adk.StripADKIdentity(agentName, ""),
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
// vec0 KNN retrieval, until the next service boot's
// BackfillEmbeddings caught them up. The fix collapses both ops into
// one helper so the bug class is structurally impossible — adding a
// future call-site can't reintroduce it.
//
// The per-tick persist accumulator includes only the synchronous
// SQLite write. The enqueue is fire-and-forget into the bounded
// worker pool; its cost is steady-state background load, not a tick
// stage.
func (r *Runner) indexMessage(msg *pb.Message) error {
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
func (r *Runner) SendMessage(ctx context.Context, content string, scope pb.SelectionScope, attachments ...*pb.AttachmentContent) (msg *pb.Message, retErr error) {
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

	// Store the user Event only when it carries real content. Empty-
	// content autonomous ticks do not enter the Store — they are a
	// loop iteration, not an Event. Protocol-adherence shaping (e.g.
	// a provider requiring a non-empty terminal user turn) is handled
	// at the adapter via repositioning of already-persisted messages,
	// never by writing synthetic Events here.
	if content != "" || len(attachments) > 0 {
		blocks := rrc.BlocksFromText(content)
		for _, a := range attachments {
			blocks = append(blocks, &pb.ContentBlock{
				Block: &pb.ContentBlock_Attachment{Attachment: a},
			})
		}
		userMsg := &pb.Message{
			Id:       r.nextMsgID(),
			Role:     pb.Role_ROLE_USER,
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
//	t_rrc_onmessage_ms = OnMessage's own duration (chunking + embed + KNN + rerank + edges)
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
		onMessageMs int64
		selectMs    int64
		assembleMs  int64
		completeMs  int64
		streamMs    int64
	)
	if r.tickAssembleSet {
		onMessageMs = r.tickAssemble.OnMessage.DurationMs
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
			assemblyInsideRun := onMessageMs + selectMs + assembleMs
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
		ThreadID:           r.threadID,
		Round:              r.tickRound,
		RRCOnMessageMs:     onMessageMs,
		SelectMs:           selectMs,
		AssembleMs:         assembleMs,
		CompleteMs:         completeMs,
		StreamMs:           streamMs,
		PersistMs:          r.tickPersistMs,
		TotalMs:            totalMs,
		CompleterModel:     r.modelName,
		CorpusSize:         r.tickCorpusSize,
		SelectedCount:      r.tickAssemble.SelectedCount,
		AssembledTokensEst: r.tickAssemble.TotalTokens,
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
func (r *Runner) processEvents(events iter.Seq2[*session.Event, error]) (*pb.Message, error) {
	var lastErr error
	var thinkingBuf strings.Builder
	var textBuf strings.Builder
	var lastAssistantMsg *pb.Message

	// ADK occasionally emits the same FunctionCall or FunctionResponse
	// Part across multiple events in a single Run (observed: every tool
	// call stored twice in the corpus). Track IDs we've already persisted
	// so repeat Parts are ignored at the storage boundary rather than
	// polluting the corpus the RRC engine sees next round.
	seenCallIDs := make(map[string]bool)
	seenResultIDs := make(map[string]bool)

	// storeThinking flushes accumulated thinking as its own message in the turn.
	storeThinking := func() {
		if thinkingBuf.Len() == 0 {
			return
		}
		msg := &pb.Message{
			Id:       r.nextMsgID(),
			Role:     pb.Role_ROLE_ASSISTANT,
			Content:  []*pb.ContentBlock{{Block: &pb.ContentBlock_Thinking{Thinking: &pb.ThinkingContent{Text: thinkingBuf.String()}}}},
			Position: r.msgSeq.Load(),
			ThreadId: r.threadID,
		}
		if err := r.indexMessage(msg); err != nil {
			log.Printf("[Runner] indexMessage(thinking) thread=%s: %v", r.threadID, err)
		}
		thinkingBuf.Reset()
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
				if fc.ID != "" && seenCallIDs[fc.ID] {
					continue
				}
				if fc.ID != "" {
					seenCallIDs[fc.ID] = true
				}
				// Flush thinking BEFORE the tool call so ordering is correct
				storeThinking()

				argsJSON := "{}"
				if fc.Args != nil {
					if s, err := adk.MarshalFunctionArgs(fc.Args); err == nil {
						argsJSON = s
					}
				}
				toolCallMsg := &pb.Message{
					Id:       r.nextMsgID(),
					Role:     pb.Role_ROLE_ASSISTANT,
					Content:  []*pb.ContentBlock{{Block: &pb.ContentBlock_ToolCall{ToolCall: &pb.ToolCallContent{Id: fc.ID, Name: fc.Name, Arguments: argsJSON}}}},
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
				toolResultMsg := &pb.Message{
					Id:       r.nextMsgID(),
					Role:     pb.Role_ROLE_ASSISTANT,
					Content:  []*pb.ContentBlock{{Block: &pb.ContentBlock_ToolResult{ToolResult: &pb.ToolResultContent{ToolCallId: fr.ID, Content: resultText}}}},
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

			if part.Text != "" && part.FunctionCall == nil {
				if part.Thought {
					thinkingBuf.WriteString(part.Text)
				} else {
					textBuf.WriteString(part.Text)
				}
			}
		}
	}

	// Store final thinking + text as the last message in the turn
	var finalContent []*pb.ContentBlock
	if thinkingBuf.Len() > 0 {
		finalContent = append(finalContent, &pb.ContentBlock{
			Block: &pb.ContentBlock_Thinking{Thinking: &pb.ThinkingContent{Text: thinkingBuf.String()}},
		})
	}
	if textBuf.Len() > 0 {
		finalContent = append(finalContent, &pb.ContentBlock{
			Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: textBuf.String()}},
		})
	}
	if len(finalContent) > 0 {
		lastAssistantMsg = &pb.Message{
			Id:       r.nextMsgID(),
			Role:     pb.Role_ROLE_ASSISTANT,
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

	// QUD carry-forward removed along with the small-fast-model extractor.
	// Thinking blocks are still stored by the runner's message loop; edge
	// discovery on thinking text is done by the scorer when the
	// synthetic message is seen by OnMessage. No separate carry-forward
	// pass is needed.
	_ = llmResponse.Content
	return llmResponse, nil
}
