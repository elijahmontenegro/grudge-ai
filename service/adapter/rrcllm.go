package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"math"
	"strings"
	"sync"

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/storage"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// RRCLLM implements model.LLM. ADK calls this thinking it's an LLM;
// RRC intercepts transparently per the RRC protocol:
//   - Derives Query from the current Event
//   - Runs OnMessage + Select against the persistent Store (DB)
//   - Assembles: System Prompt + Selected Messages + Current Turn
//   - Forwards to the real LLM with tool declarations
//
// Carry-forward is NOT done here — it happens once in Runner.afterModelCallback.
type StreamCallback func(delta, thinking string, done bool)

type RRCLLM struct {
	engine          *rrc.Engine
	completer       core.Completer
	db              *storage.DB
	threadID        string
	modelName       string
	rerankerModelID string // used by StoreResolver to look up per-query scores
	EngineMu        *sync.RWMutex     // shared engine lock
	Scope           pb.SelectionScope // set per-call by Runner before ADK runs

	OnStream    StreamCallback                   // publish streaming deltas
	OnSelection func(result *pb.SelectionResult) // publish selection for introspection
	OnEdge      func(edge *pb.Edge)              // persist edges to DB
}

// NewRRCLLM creates an RRC-as-LLM adapter.
//
// rerankerModelID is the id under which the reranker's chunk-pair
// scores are persisted. It anchors the protocol-rule pipeline's
// StoreResolver so candidate picks for protocol-slot fills use the
// same scoring surface that drove Selection.
func NewRRCLLM(engine *rrc.Engine, completer core.Completer, db *storage.DB, threadID, modelName, rerankerModelID string) *RRCLLM {
	return &RRCLLM{
		engine:          engine,
		completer:       completer,
		db:              db,
		threadID:        threadID,
		modelName:       modelName,
		rerankerModelID: rerankerModelID,
	}
}

func (r *RRCLLM) Name() string { return r.modelName }

// GenerateContent implements model.LLM per the RRC protocol.
//
// With IncludeContents=None on the llmagent, req.Contents contains only the
// current turn (latest user input + tool calls/results from this Run).
// RRC provides historical context via selection from the persistent Store.
//
// Flow:
//  1. Extract system instruction from ADK config
//  2. Derive Query from the current Event (last content in ADK conversation)
//  3. Load corpus from DB, run OnMessage + Select against real stored messages
//  4. Persist edges, publish selection for introspection
//  5. Assemble: System Prompt + Selected Messages + Current Turn
//  6. Forward to real LLM with tool declarations
func (r *RRCLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// === System Instruction ===
		var systemMsg *pb.LLMMessage
		if req.Config != nil && req.Config.SystemInstruction != nil {
			systemMsg = GenaiContentToProto(req.Config.SystemInstruction)
			systemMsg.Role = pb.Role_ROLE_SYSTEM
		}

		// === Derive Query from the current Event ===
		var query string
		if len(req.Contents) > 0 {
			lastContent := req.Contents[len(req.Contents)-1]
			for _, p := range lastContent.Parts {
				if p.Text != "" {
					query += p.Text
				}
				if p.FunctionResponse != nil {
					if resp := p.FunctionResponse.Response; resp != nil {
						if r, ok := resp["result"].(string); ok {
							query += r
						}
					}
				}
			}
		}

		// === Selection against the persistent Store ===
		// Corpus scope must match Selection scope — if we're going to
		// select from all threads, we have to SCORE against all threads
		// first, otherwise OnMessage never creates cross-thread edges
		// and Select just walks outward from a query-id with nothing
		// connecting it to prior conversations. Scoping corpus to the
		// current thread under ALL_THREADS scope was the cause of the
		// "agent can't remember across threads" bug.
		var corpus []*pb.Message
		var err error
		if r.Scope == pb.SelectionScope_SELECTION_SCOPE_ALL_THREADS {
			corpus, err = r.db.AllCorpus()
		} else {
			corpus, err = r.db.ThreadCorpus(r.threadID)
		}
		if err != nil {
			yield(nil, fmt.Errorf("load corpus: %w", err))
			return
		}

		if query == "" {
			for i := len(corpus) - 1; i >= 0; i-- {
				if text := ProtoToText(corpus[i].Content); text != "" {
					query = text
					break
				}
			}
		}

		// Log the derived Query (first 200 chars) so introspection logs
		// show what Selection was trying to match, not just how many
		// results came back. Protocol §I8: one Query per Selection.
		if query != "" {
			qSnip := query
			if len(qSnip) > 200 {
				qSnip = qSnip[:200] + "…"
			}
			log.Printf("RRC: Query thread=%s query=%q", r.threadID, qSnip)
		}

		// selectedOrdered holds Selected in engine-emitted order: highest
		// effective score first, lowest at the tail. The assembly loop
		// below sheds from the tail when the LLM returns a context-
		// window-exceeds-limit error — principled graceful degradation
		// when the physical token ceiling is below what RRC identified
		// as prerequisite. Lowest-score first preserves the most
		// prerequisite-like selections as long as possible.
		var selectedOrdered []*pb.SelectedMessage
		var queryMsgID string // captured for the rectification resolver below
		if query != "" && len(corpus) > 0 {
			// Use the last stored message as the query source — this is the real
			// message the Runner stored (user msg, tool call, or tool result),
			// not a synthetic phantom.
			queryMsg := corpus[len(corpus)-1]
			queryMsgID = queryMsg.Id

			if r.EngineMu != nil {
				r.EngineMu.Lock()
			}
			edges, edgeErr := r.engine.OnMessage(ctx, queryMsg, corpus)
			result, selErr := r.engine.Select(queryMsg.Id, r.Scope, r.threadID)
			if r.EngineMu != nil {
				r.EngineMu.Unlock()
			}

			// RRC is not optional (CLAUDE.md, spec/web/MANIFEST.adoc:186).
			// "Cross-encoder unreachable → system does not work. Error
			// displayed, links to settings, system waits until resolved.
			// No degraded mode, no passthrough."
			//
			// Previously these errors were logged-and-continued: the
			// round assembled with selected=0 and the LLM still fired
			// with radius + current turn only. Model produced content
			// without prerequisite context; nobody knew RRC was dark.
			// Confirmed in the 2026-04-23 autonomous run — 33 blind
			// rounds accumulated in positions 344-376 during a TEI
			// wedge, polluting the corpus.
			//
			// The fix: infrastructure failures abort the round. The
			// error bubbles to ADK's event iterator → SendMessage →
			// the caller (GraphQL mutation for chat/plan; autonomous
			// loop's OnAutonomousError handler for autonomous runs,
			// which pauses the loop and surfaces the error in-thread).
			// Same propagation path serves every mode.
			if edgeErr != nil {
				log.Printf("RRC: OnMessage failed for thread %s (query msg %s): %v",
					r.threadID, queryMsg.Id, edgeErr)
				yield(nil, fmt.Errorf("rrc classifier failed: %w", edgeErr))
				return
			}
			if selErr != nil {
				log.Printf("RRC: Select failed for thread %s (prompt %s, scope %v): %v",
					r.threadID, queryMsg.Id, r.Scope, selErr)
				yield(nil, fmt.Errorf("rrc select failed: %w", selErr))
				return
			}

			// Persist edges only when the scoring round completed cleanly.
			// Previously edges were persisted "even on partial runs" —
			// but a partial run means the classifier errored mid-scoring,
			// so the edges we have are an arbitrary subset of what would
			// have been scored. Partial data here silently biases future
			// walks (edges that happen to have completed survive; edges
			// that would have scored higher but didn't reach the
			// classifier don't exist). Either the round succeeds and its
			// edges land, or the round aborts and nothing lands.
			if r.OnEdge != nil {
				for _, edge := range edges {
					r.OnEdge(edge)
				}
			}

			if selErr == nil && result != nil {
				if r.OnSelection != nil {
					r.OnSelection(result)
				}

				// Apply hyperselection uniformly — no Current Turn
				// carve-out. The protocol's Current Turn rule (§3.3)
				// treated the model's accumulated per-turn output as
				// categorically sacred. That was reasonable for a
				// user↔model conversation but degenerate for agent
				// loops: a 69-event tool chain within one turn would
				// bloat the payload unconditionally, forcing shed-to-
				// fit to fire as steady-state traffic against real
				// prior context.
				//
				// Under the principle (hyperselection, discriminative,
				// zero-return valid) the model's intra-turn tool chain
				// is no different from any other prior — each event
				// entered the corpus, went through OnMessage, earned
				// its edges. Let Selection decide which of those
				// events is actually prerequisite to the current query,
				// whether it's from 300 turns ago or from 3 tool calls
				// ago. The immediate conversational bridge stays via
				// Radius below (last N thread messages regardless of
				// turn boundary).
				//
				// Query itself is excluded by the engine's DAG walk —
				// Select starts FROM queryMsg.Id, it doesn't emit it.
				selectedOrdered = append(selectedOrdered, result.Selected...)
				log.Printf("RRC: thread %s selected %d/%d messages for prompt %s",
					r.threadID, len(selectedOrdered), len(corpus), queryMsg.Id)
			}
		}

		// === Assemble + Send with unified-budget shed ===
		//
		// One budget: ContextBudgetTokens. One shed rule: drop the
		// lowest-scored Selected entry OR rectification insert until
		// the wire fits. Rectification and Selection compete on the
		// same rerank-max axis (rectification candidates' cache
		// score; Selected's walk score projected via MaxScoresFrom
		// against the current query). System and Radius are fixed —
		// structural, never shed; if they alone exceed the budget,
		// surface an error.
		//
		// Each shed iteration: rebuild Selected from corpus minus
		// droppedSelectedIDs, run rules to regenerate rectification
		// (different dropped sets yield different wires, so rules
		// must re-run rather than reuse), compute total, compare to
		// budget, drop the lowest-score droppable, repeat.

		droppedSelectedIDs := make(map[string]bool)
		budget := r.engine.Config().ContextBudgetTokens

		for {
			// === Build Selected (chronological, minus dropped) ===
			selectedIDs := make(map[string]bool)
			for _, s := range selectedOrdered {
				if !droppedSelectedIDs[s.MessageId] {
					selectedIDs[s.MessageId] = true
				}
			}
			var selectedMsgs []*pb.LLMMessage
			var wireIDs []string
			selectedPtrToID := make(map[*pb.LLMMessage]string)
			for _, msg := range corpus {
				if selectedIDs[msg.Id] {
					llm := MessageToLLM(msg)
					selectedMsgs = append(selectedMsgs, llm)
					selectedPtrToID[llm] = msg.Id
					wireIDs = append(wireIDs, msg.Id)
				}
			}

			// === Build Radius ===
			var radiusMsgs []*pb.LLMMessage
			radiusN := r.engine.RadiusSize()
			if radiusN > 0 {
				threadCorpus, terr := r.db.ThreadCorpus(r.threadID)
				if terr != nil {
					log.Printf("RRC: Radius load failed for thread %s: %v", r.threadID, terr)
				} else {
					start := len(threadCorpus) - radiusN
					if start < 0 {
						start = 0
					}
					for _, m := range threadCorpus[start:] {
						if selectedIDs[m.Id] {
							continue
						}
						radiusMsgs = append(radiusMsgs, MessageToLLM(m))
						wireIDs = append(wireIDs, m.Id)
					}
				}
			}

			// === Assemble pre-rectification wire ===
			var llmMsgs []*pb.LLMMessage
			if systemMsg != nil {
				llmMsgs = append(llmMsgs, systemMsg)
			}
			llmMsgs = append(llmMsgs, selectedMsgs...)
			llmMsgs = append(llmMsgs, radiusMsgs...)

			// === Rectify ===
			// Rules fill protocol slots from the Store. Each rule is
			// a pure function over (wire, resolver); the resolver
			// owns the score-ordered candidate pool (rerank-max
			// against this query, scope-respecting) + used-set.
			// The wire-id exclusion seed prevents rules from re-
			// fetching a message that's already contributing to the
			// payload under its original id.
			resolver, rerr := NewStoreResolver(r.db, r.threadID, queryMsgID, r.rerankerModelID, r.Scope)
			insertScores := map[*pb.LLMMessage]float64{}
			if rerr != nil {
				log.Printf("RRC: resolver build failed thread=%s: %v — sending unrectified", r.threadID, rerr)
			} else {
				resolver.ExcludeIDs(wireIDs)
				rectified, scores, aerr := Apply(llmMsgs, resolver,
					PairToolResultsWithCalls,
					PairToolCallsWithResults,
					EnsureUserAnchor,
				)
				if aerr != nil {
					log.Printf("RRC: rectification failed thread=%s: %v — sending pre-rectification wire", r.threadID, aerr)
				} else {
					llmMsgs = rectified
					insertScores = scores
				}
			}

			// === Unified budget: shed lowest-score droppable ===
			// Droppable = Selected entries + rectification inserts.
			// Score axis = rerank-max against query (Selected entries
			// read from resolver.Score; rectification inserts
			// carry score in insertScores). System + Radius are
			// fixed-cost; not shed. If the fixed cost alone exceeds
			// budget, we can't fit — surface an error.
			// Estimate per-message token cost using ProtoToAllText,
			// which covers text + thinking + tool_call arguments +
			// tool_result content + inlined attachment text. The
			// naive ProtoToText only sees text blocks — on a
			// tool-heavy wire (chapter bodies arrive as tool_results,
			// FileWrite arguments carry prose), that under-counts
			// every message with tool content to zero. Empirically
			// this is the difference between estimating ~15K tokens
			// and sending 280K tokens on the wire, which is why
			// budget gates never fired and 92% of requests hit the
			// provider's context-window ceiling. ProtoToAllText
			// matches the storage layer's chunk text and reflects
			// what actually goes to the adapter's canonicalizer
			// before wire serialization.
			msgTokens := make([]int, len(llmMsgs))
			total := 0
			for i, m := range llmMsgs {
				msgTokens[i] = rrc.EstimateTokens(ProtoToAllText(m.Content))
				total += msgTokens[i]
			}
			if budget > 0 && total > budget {
				// Drop iteratively from lowest-score until fit.
				dropRectByPtr := make(map[*pb.LLMMessage]bool)
				var dropSelectedID string
				for total > budget {
					// Find lowest score across Selected + rect.
					var bestPtr *pb.LLMMessage
					bestScore := math.Inf(1)
					bestIdx := -1
					for i, m := range llmMsgs {
						if dropRectByPtr[m] {
							continue
						}
						var s float64
						if rs, ok := insertScores[m]; ok {
							s = rs
						} else if id, ok := selectedPtrToID[m]; ok {
							s = resolver.Score(id)
						} else {
							continue // system or radius — never shed
						}
						if s < bestScore {
							bestScore = s
							bestPtr = m
							bestIdx = i
						}
					}
					if bestPtr == nil {
						// Nothing droppable left — System+Radius
						// alone exceed budget. Fall through; send
						// anyway and let provider surface the real
						// error.
						break
					}
					if id, ok := selectedPtrToID[bestPtr]; ok {
						dropSelectedID = id
						break // defer actual drop to outer rebuild
					}
					dropRectByPtr[bestPtr] = true
					total -= msgTokens[bestIdx]
				}
				if dropSelectedID != "" {
					log.Printf("RRC: shed selected msg=%s (score=%.3f)", dropSelectedID, resolver.Score(dropSelectedID))
					droppedSelectedIDs[dropSelectedID] = true
					continue // rebuild from scratch
				}
				if len(dropRectByPtr) > 0 {
					log.Printf("RRC: shed %d rectification insert(s) to fit budget", len(dropRectByPtr))
					filtered := make([]*pb.LLMMessage, 0, len(llmMsgs))
					for _, m := range llmMsgs {
						if dropRectByPtr[m] {
							continue
						}
						filtered = append(filtered, m)
					}
					llmMsgs = filtered
				}
			}

			inserts := len(insertScores)
			log.Printf("RRC: assembly thread=%s selected=%d(shed=%d) radius=%d rect=%d total=%d",
				r.threadID, len(selectedMsgs), len(droppedSelectedIDs), len(radiusMsgs), inserts, len(llmMsgs))

			protoReq := &pb.CompletionRequest{
				Messages: llmMsgs,
				Model:    req.Model,
				Stream:   stream,
			}
			if req.Config != nil {
				if req.Config.Temperature != nil {
					t := float32(*req.Config.Temperature)
					protoReq.Temperature = &t
				}
				if req.Config.TopP != nil {
					t := float32(*req.Config.TopP)
					protoReq.TopP = &t
				}
				if req.Config.MaxOutputTokens > 0 {
					t := req.Config.MaxOutputTokens
					protoReq.MaxTokens = &t
				}
				for _, gt := range req.Config.Tools {
					for _, fd := range gt.FunctionDeclarations {
						paramsJSON := `{"type":"object","properties":{}}`
						if fd.ParametersJsonSchema != nil {
							if b, err := json.Marshal(fd.ParametersJsonSchema); err == nil {
								paramsJSON = string(b)
							}
						} else if fd.Parameters != nil {
							if b, err := json.Marshal(fd.Parameters); err == nil {
								paramsJSON = string(b)
							}
						}
						protoReq.Tools = append(protoReq.Tools, &pb.ToolDeclaration{
							Name:           fd.Name,
							Description:    fd.Description,
							ParametersJson: paramsJSON,
						})
					}
				}
			}

			// Try the LLM. tryComplete/tryStream return the initial
			// error WITHOUT yielding it, so the shed loop can observe
			// context-window errors and retry with a smaller payload.
			// Mid-stream errors and successes commit to the iterator.
			var sendErr error
			if protoReq.Stream {
				sendErr = r.tryStream(ctx, protoReq, yield)
			} else {
				sendErr = r.tryComplete(ctx, protoReq, yield)
			}

			if sendErr == nil {
				// Either a successful send (already yielded) or a
				// mid-stream failure that yielded its own error.
				return
			}
			if !isContextOverflow(sendErr) {
				// Non-overflow initial failure — propagate as-is.
				yield(nil, sendErr)
				return
			}
			// Provider-reported overflow despite our budget math —
			// tokenizer undershoot. Force-shed one more entry (the
			// lowest-score surviving Selected) and rebuild. The
			// in-loop budget pass will typically catch this on the
			// next iteration.
			var forcedDrop string
			lowestScore := math.Inf(1)
			for _, s := range selectedOrdered {
				if droppedSelectedIDs[s.MessageId] {
					continue
				}
				sc := resolver.Score(s.MessageId)
				if sc < lowestScore {
					lowestScore = sc
					forcedDrop = s.MessageId
				}
			}
			if forcedDrop == "" {
				yield(nil, fmt.Errorf("context window exceeded after shedding all %d selections: %w", len(selectedOrdered), sendErr))
				return
			}
			log.Printf("RRC: provider overflow, forcing shed of selected msg=%s (score=%.3f)", forcedDrop, lowestScore)
			droppedSelectedIDs[forcedDrop] = true
		}
	}
}

// isContextOverflow recognizes provider errors that indicate the request
// payload exceeded the model's context-window ceiling. Distinguishing
// these from other failures lets the assembly layer shed lowest-score
// Selected entries and retry with a smaller payload, rather than
// surfacing the error terminally. Patterns are provider-specific
// strings observed in the wild; new providers get added here as they
// surface different wording.
func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	patterns := []string{
		"context window exceeds limit",     // ollama (minimax upstream)
		"context length exceeded",           // OpenAI-family
		"maximum context length",            // OpenAI-family alt wording
		"context_length_exceeded",           // OpenAI error code
		"prompt is too long",                // Anthropic
		"input is too long",                 // Anthropic alt
		"requested tokens",                  // bits of generic "exceeds" wording
		"exceeds the maximum",               // generic
		"too many tokens",                   // generic
		"context window",                    // last-resort catch
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// tryComplete sends a non-streaming request. The initial error is
// returned to the caller (the shed loop) without yielding, so a
// context-overflow failure can trigger a re-assembly. On success the
// response is yielded and nil is returned.
func (r *RRCLLM) tryComplete(ctx context.Context, protoReq *pb.CompletionRequest, yield func(*model.LLMResponse, error) bool) error {
	protoResp, err := r.completer.Complete(ctx, protoReq)
	if err != nil {
		return err
	}
	genaiContent := ProtoResponseToGenai(protoResp)
	yield(&model.LLMResponse{
		Content:      genaiContent,
		TurnComplete: true,
	}, nil)
	return nil
}

// tryStream opens a streaming request and returns the initial error
// (if any) WITHOUT yielding it — so the shed loop can observe a
// context-overflow and retry. Once the stream begins yielding chunks,
// further errors are yielded to ADK through the iterator (no retry
// possible past that point, the turn is committed).
func (r *RRCLLM) tryStream(ctx context.Context, protoReq *pb.CompletionRequest, yield func(*model.LLMResponse, error) bool) error {
	ch, err := r.completer.Stream(ctx, protoReq)
	if err != nil {
		return err
	}

	// Track function calls seen during the stream. ADK checks the LAST event
	// to decide whether to continue the tool loop (IsFinalResponse checks
	// hasFunctionCalls). If the last event is an empty Done signal, ADK exits
	// the loop and never executes the tool. The Done response must carry any
	// pending function calls so ADK sees them and continues.
	var pendingCalls []*genai.Part

	for chunk := range ch {
		resp := &model.LLMResponse{Partial: true}
		if t := chunk.GetText(); t != nil {
			resp.Content = &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: t.Text}},
			}
			if r.OnStream != nil {
				r.OnStream(t.Text, "", false)
			}
		}
		if t := chunk.GetThinking(); t != nil {
			resp.Content = &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: t.Text, Thought: true}},
			}
			if r.OnStream != nil {
				r.OnStream("", t.Text, false)
			}
		}
		if tc := chunk.GetToolCall(); tc != nil {
			part := &genai.Part{
				FunctionCall: &genai.FunctionCall{
					ID:   tc.Id,
					Name: tc.Name,
					Args: parseToolArgs(tc.Arguments),
				},
			}
			pendingCalls = append(pendingCalls, part)
			resp.Content = &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{part},
			}
		}
		if chunk.Done {
			resp.Partial = false
			resp.TurnComplete = true
			// If there were function calls in the stream, the Done response
			// must carry them so ADK continues the tool loop.
			if len(pendingCalls) > 0 {
				resp.Content = &genai.Content{
					Role:  "model",
					Parts: pendingCalls,
				}
			} else if resp.Content == nil {
				resp.Content = &genai.Content{
					Role:  "model",
					Parts: []*genai.Part{{Text: ""}},
				}
			}
			if chunk.Error != nil {
				resp.ErrorMessage = *chunk.Error
			}
			if r.OnStream != nil {
				r.OnStream("", "", true)
			}
			yield(resp, nil)
			return nil
		}
		if !yield(resp, nil) {
			return nil
		}
	}
	// Stream ended without Done chunk — force a final response
	yield(&model.LLMResponse{
		TurnComplete: true,
		Content: &genai.Content{
			Role:  "model",
			Parts: []*genai.Part{{Text: ""}},
		},
	}, nil)
	if r.OnStream != nil {
		r.OnStream("", "", true)
		if r.OnStream != nil {
			r.OnStream("", "", true)
		}
	}
	return nil
}

// parseToolArgs converts a JSON arguments string to map[string]any for genai.FunctionCall.
func parseToolArgs(argsJSON string) map[string]any {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return map[string]any{"raw": argsJSON}
	}
	return args
}

// (user-anchor, pair-tool-calls, pair-tool-results rectifications
// live in protocol.go as composable Rules + StoreResolver.)
