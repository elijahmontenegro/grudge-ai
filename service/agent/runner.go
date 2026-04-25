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

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/adapter"
	"github.com/emontenegr/spidey/service/storage"

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
	rrcLLM          *adapter.RRCLLM // stored to set scope per-call
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
	onStream    adapter.StreamCallback
	onSelection func(result *pb.SelectionResult)
	onRound     func(round int, elapsed time.Duration)
	// Event handlers — service wires these to publish to GraphQL subscriptions
	OnToolCall   func(callID, toolName, args string)
	OnToolResult func(callID, toolName, result string, isError bool)
	// Embed-on-arrival — called after storing a message so search indexes it
	OnMessageStored func(msgID, text string)
	// OnAutonomousError fires when a mid-run SendMessage fails during an
	// autonomous loop. Per spec the loop pauses rather than exits — the
	// handler is expected to pause autoState, publish Paused agent state
	// to the UI, and write the error as a system message in the thread
	// so the user can see what broke before deciding to resume or stop.
	OnAutonomousError func(err error)
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
func (r *Runner) SetStreamCallback(cb adapter.StreamCallback) {
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
// rerankerModelID is the id under which reranker chunk-pair scores
// are persisted in the scores table. Passed through to RRCLLM so
// its protocol-rectification resolver can score Store-resident
// candidates against the current query.
func NewRunner(engine *rrc.Engine, completer core.Completer, db *storage.DB, threadID string, tools []tool.Tool, modelName, instruction, rerankerModelID string) (*Runner, error) {
	r := &Runner{
		engine:          engine,
		completer:       completer,
		db:              db,
		threadID:        threadID,
		tools:           tools,
		modelName:       modelName,
		instruction:     instruction,
		rerankerModelID: rerankerModelID,
	}

	// RRC-as-LLM: ADK calls this thinking it's an LLM. Engine owns its
	// own lock now; rrcLLM acquires it directly via engine.Lock /
	// Unlock — no shared mutex passed in.
	rrcLLM := adapter.NewRRCLLM(engine, completer, db, threadID, modelName, rerankerModelID)
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
	r.rrcLLM = rrcLLM

	// Create the ADK agent with RRC as its model.
	// IncludeContentsNone: ADK sends only the current turn to the model.
	// RRC provides historical context via selection from the persistent Store.
	// Without this, ADK sends the full session history and RRC is redundant.
	rootAgent, err := llmagent.New(llmagent.Config{
		Name:            "spidey",
		Description:     "Spidey agentic assistant with RRC-powered selective memory",
		Instruction:     instruction,
		Model:           rrcLLM,
		Tools:           tools,
		IncludeContents: llmagent.IncludeContentsNone,
		AfterModelCallbacks: []llmagent.AfterModelCallback{
			r.afterModelCallback,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	// Create the ADK runner
	adkRunner, err := runner.New(runner.Config{
		AppName:           "spidey",
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

// embedIfWired calls the embed-on-arrival callback for search indexing.
func (r *Runner) embedIfWired(msgID, text string) {
	if r.OnMessageStored != nil && text != "" {
		go r.OnMessageStored(msgID, text)
	}
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
func (r *Runner) SendMessage(ctx context.Context, content string, scope pb.SelectionScope, attachments ...*pb.AttachmentContent) (*pb.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

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

	corpus, err := r.db.ThreadCorpus(r.threadID)
	if err != nil {
		return nil, fmt.Errorf("load corpus: %w", err)
	}

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
		blocks := adapter.TextToProto(content)
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
		if err := r.db.InsertMessage(userMsg); err != nil {
			return nil, err
		}
		r.embedIfWired(userMsg.Id, content)
		corpus = append(corpus, userMsg)
	}

	// Set scope on RRCLLM before ADK runs — the scope toggle reaches the engine here
	r.rrcLLM.Scope = scope

	// Empty-content calls (autonomous ticks) pass nil to ADK: its Run
	// API supports nil msg explicitly — appendMessageToSession returns
	// early on nil, findAgentToRun walks session history to continue
	// from the last agent event. No synthetic content injected.
	var msg *genai.Content
	if content != "" {
		msg = &genai.Content{
			Role:  "user",
			Parts: []*genai.Part{{Text: content}},
		}
	}

	events := r.adkRunner.Run(ctx, "user", r.threadID, msg, adkagent.RunConfig{
		StreamingMode: adkagent.StreamingModeSSE,
	})
	return r.processEvents(events)
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
		r.db.InsertMessage(msg)
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
					if s, err := adapter.MarshalFunctionArgs(fc.Args); err == nil {
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
				r.db.InsertMessage(toolCallMsg)
				r.embedIfWired(toolCallMsg.Id, fc.Name+": "+argsJSON)
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
				r.db.InsertMessage(toolResultMsg)
				r.embedIfWired(toolResultMsg.Id, resultText)
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
		if err := r.db.InsertMessage(lastAssistantMsg); err != nil {
			return nil, err
		}
		r.embedIfWired(lastAssistantMsg.Id, adapter.ProtoToText(lastAssistantMsg.Content))
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
	// discovery on thinking text is done by the classifier when the
	// synthetic message is seen by OnMessage. No separate carry-forward
	// pass is needed.
	_ = llmResponse.Content
	return llmResponse, nil
}
