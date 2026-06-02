package adk

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"math"

	"github.com/emontenegr/grudge/core"
	pb "github.com/emontenegr/grudge/proto/gen/go/grudge/v1"
	"github.com/emontenegr/grudge/rrc"
	"github.com/emontenegr/grudge/rrc/chunk"
	"github.com/emontenegr/grudge/service/search"
	"github.com/emontenegr/grudge/service/storage"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// RRCLLM implements model.LLM. ADK calls this thinking it's an LLM;
// RRC intercepts transparently per the RRC protocol:
//   - Derives Query from the current Event
//   - Calls rrc.Engine.Assemble against the persistent Store (DB)
//   - Forwards the assembled wire to the real LLM with tool declarations
//
// Carry-forward is NOT done here — it happens once in Runner.afterModelCallback.
type StreamCallback func(delta, thinking string, done bool)

type RRCLLM struct {
	engine          *rrc.Engine
	completer       core.Completer
	db              *storage.DB
	threadID        string
	modelName       string
	rerankerModelID string            // used by StoreResolver to look up per-query scores
	Scope           pb.SelectionScope // set per-call by Runner before ADK runs

	OnStream    StreamCallback                   // publish streaming deltas
	OnSelection func(result *pb.SelectionResult) // publish selection for introspection
	OnEdge      func(edge *pb.Edge)              // persist edges to DB
	OnAssemble  func(t rrc.AssembleTelemetry)    // emit per-call assembly telemetry (stage timings + counts)
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
// rrc.Engine.Assemble provides historical context via selection from the
// persistent Store.
//
// Flow:
//  1. Extract system instruction from ADK config
//  2. Derive Query from the current Event (last content in ADK conversation)
//  3. Load scope-appropriate corpus + thread corpus from DB
//  4. Compute codec-specific tool-schema overhead for the budget
//  5. Loop: build resolver, call rrc.Engine.Assemble, send to provider;
//     on context-overflow, append lowest-score Selected to ExcludeIDs and retry.
func (r *RRCLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var systemMsg *pb.LLMMessage
		if req.Config != nil && req.Config.SystemInstruction != nil {
			systemMsg = GenaiContentToProto(req.Config.SystemInstruction)
			systemMsg.Role = pb.Role_ROLE_SYSTEM
		}

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

		// Corpus scope must match Selection scope — if we're going to
		// select from all threads, we have to SCORE against all threads
		// first, otherwise OnMessage never creates cross-thread edges
		// and Select just walks outward from a query-id with nothing
		// connecting it to prior conversations.
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
				if text := rrc.TextFromBlocks(corpus[i].Content); text != "" {
					query = text
					break
				}
			}
		}
		if query != "" {
			qSnip := query
			if len(qSnip) > 200 {
				qSnip = qSnip[:200] + "…"
			}
			log.Printf("RRC: Query thread=%s query=%q", r.threadID, qSnip)
		}

		if query == "" || len(corpus) == 0 {
			// Cold start: nothing to assemble. Forward an empty wire
			// (just system + the current turn translated by ADK).
			r.forwardCurrentTurn(ctx, req, stream, systemMsg, yield)
			return
		}

		queryMsg := corpus[len(corpus)-1]

		// Thread corpus is needed for the Radius slice. When scope is
		// THREAD it equals corpus; when ALL_THREADS we load it again.
		var threadCorpus []*pb.Message
		if r.Scope == pb.SelectionScope_SELECTION_SCOPE_ALL_THREADS {
			threadCorpus, err = r.db.ThreadCorpus(r.threadID)
			if err != nil {
				log.Printf("RRC: Radius load failed thread=%s: %v", r.threadID, err)
			}
		} else {
			threadCorpus = corpus
		}

		// Tool declarations are static across retry iterations — same
		// set of tools for the whole turn. Hoist conversion + token
		// estimation out of the loop so the JSON only marshals once
		// and the estimate participates in the budget math without
		// being re-derived. The tool block (4-9KB / round of raw
		// JSON the adapter adds unconditionally) is invisible to
		// budget math otherwise.
		var protoTools []*pb.ToolDeclaration
		if req.Config != nil {
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
					protoTools = append(protoTools, &pb.ToolDeclaration{
						Name:           fd.Name,
						Description:    fd.Description,
						ParametersJson: paramsJSON,
					})
				}
			}
		}
		toolSchemaTokens := estimateToolSchemaTokens(protoTools)

		cfg := r.engine.Config()
		var excludeIDs []string

		for {
			resolver, rerr := search.NewStoreResolver(r.db, r.threadID, queryMsg.Id, r.rerankerModelID, r.Scope)
			if rerr != nil {
				log.Printf("RRC: resolver build failed thread=%s: %v — assembling without rectification", r.threadID, rerr)
			}

			assembleReq := rrc.AssembleRequest{
				Query:        queryMsg,
				Corpus:       corpus,
				ThreadCorpus: threadCorpus,
				Scope:        r.Scope,
				ThreadID:     r.threadID,
				System:       systemMsg,
				Budget:       cfg.ContextBudgetTokens,
				HeadroomPct:  cfg.BudgetHeadroomPct,
				PerMsgDelim:  cfg.PerMsgDelimiterTokens,
				FixedTokens:  toolSchemaTokens,
				ExcludeIDs:   excludeIDs,
			}
			if resolver != nil {
				assembleReq.Resolver = resolver
				assembleReq.Rules = []rrc.Rule{
					rrc.PairToolResultsWithCalls,
					rrc.PairToolCallsWithResults,
					rrc.EnsureUserAnchor,
				}
			}

			result, aerr := r.engine.Assemble(ctx, assembleReq)
			if aerr != nil {
				log.Printf("RRC: Assemble failed thread=%s: %v", r.threadID, aerr)
				yield(nil, aerr)
				return
			}

			if r.OnEdge != nil {
				for _, edge := range result.Edges {
					r.OnEdge(edge)
				}
			}
			if r.OnSelection != nil && result.Selection != nil {
				r.OnSelection(result.Selection)
			}

			t := result.Telemetry
			if r.OnAssemble != nil {
				r.OnAssemble(t)
			}
			log.Printf("RRC: assembly thread=%s selected=%d radius=%d rect=%d shed=%d total=%d budget=%d wire=%d",
				r.threadID, t.SelectedCount, t.RadiusCount, t.RectifiedCount, t.SheddedCount,
				t.TotalTokens, t.EffectiveBudget, len(result.Wire))

			protoReq := &pb.CompletionRequest{
				Messages: result.Wire,
				Model:    req.Model,
				Stream:   stream,
				Tools:    protoTools,
			}
			if req.Config != nil {
				if req.Config.Temperature != nil {
					tt := float32(*req.Config.Temperature)
					protoReq.Temperature = &tt
				}
				if req.Config.TopP != nil {
					tt := float32(*req.Config.TopP)
					protoReq.TopP = &tt
				}
				if req.Config.MaxOutputTokens > 0 {
					tt := req.Config.MaxOutputTokens
					protoReq.MaxTokens = &tt
				}
			}

			var sendErr error
			if protoReq.Stream {
				sendErr = r.tryStream(ctx, protoReq, yield)
			} else {
				sendErr = r.tryComplete(ctx, protoReq, yield)
			}

			if sendErr == nil {
				return
			}
			if !core.IsContextOverflow(sendErr) {
				yield(nil, sendErr)
				return
			}

			// Provider rejected with context-window-exceeded despite our
			// budget math — tokenizer undershoot. Force-shed one more
			// surviving Selected entry (lowest score) and re-Assemble.
			forcedDrop := lowestSurvivingScore(result.Selection, excludeIDs)
			if forcedDrop == "" {
				yield(nil, fmt.Errorf("context window exceeded after shedding all selections: %w", sendErr))
				return
			}
			log.Printf("RRC: provider overflow, forcing shed of selected msg=%s", forcedDrop)
			excludeIDs = append(excludeIDs, forcedDrop)
		}
	}
}

// forwardCurrentTurn handles the cold-start path where there's no
// prior corpus to assemble against — emit System (if any) and let
// ADK's current-turn content flow through unchanged.
func (r *RRCLLM) forwardCurrentTurn(ctx context.Context, req *model.LLMRequest, stream bool, systemMsg *pb.LLMMessage, yield func(*model.LLMResponse, error) bool) {
	var llmMsgs []*pb.LLMMessage
	if systemMsg != nil {
		llmMsgs = append(llmMsgs, systemMsg)
	}
	for _, c := range req.Contents {
		llmMsgs = append(llmMsgs, GenaiContentToProto(c))
	}

	var protoTools []*pb.ToolDeclaration
	if req.Config != nil {
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
				protoTools = append(protoTools, &pb.ToolDeclaration{
					Name:           fd.Name,
					Description:    fd.Description,
					ParametersJson: paramsJSON,
				})
			}
		}
	}

	protoReq := &pb.CompletionRequest{
		Messages: llmMsgs,
		Model:    req.Model,
		Stream:   stream,
		Tools:    protoTools,
	}
	if req.Config != nil {
		if req.Config.Temperature != nil {
			tt := float32(*req.Config.Temperature)
			protoReq.Temperature = &tt
		}
		if req.Config.TopP != nil {
			tt := float32(*req.Config.TopP)
			protoReq.TopP = &tt
		}
		if req.Config.MaxOutputTokens > 0 {
			tt := req.Config.MaxOutputTokens
			protoReq.MaxTokens = &tt
		}
	}

	if stream {
		if err := r.tryStream(ctx, protoReq, yield); err != nil {
			yield(nil, err)
		}
		return
	}
	if err := r.tryComplete(ctx, protoReq, yield); err != nil {
		yield(nil, err)
	}
}

// lowestSurvivingScore returns the message id of the lowest-scoring
// Selected entry not already in the exclude set. Empty string when
// every entry has already been shed.
func lowestSurvivingScore(result *pb.SelectionResult, excludeIDs []string) string {
	if result == nil {
		return ""
	}
	excluded := make(map[string]bool, len(excludeIDs))
	for _, id := range excludeIDs {
		excluded[id] = true
	}
	var pick string
	lowest := math.Inf(1)
	for _, s := range result.Selected {
		if excluded[s.MessageId] {
			continue
		}
		score := float64(s.EffectiveScore)
		if score < lowest {
			lowest = score
			pick = s.MessageId
		}
	}
	return pick
}

// estimateToolSchemaTokens returns a tiktoken estimate for the JSON
// shape the Ollama adapter emits for each ToolDeclaration. Mirrors
// the serialization in core/adapter/ollama/ollama.go — one JSON
// object per function with name, description, and a parameters
// sub-object inlined from ParametersJson.
//
// The estimate is intentionally upstream of the adapter: if the
// adapter changes its wire format, the budget math diverges. The
// tradeoff is simplicity — reaching through adapter internals just
// for a budget estimate would entangle layers that are otherwise
// independent.
func estimateToolSchemaTokens(tools []*pb.ToolDeclaration) int {
	if len(tools) == 0 {
		return 0
	}
	type toolJSON struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	var total int
	for _, t := range tools {
		var tj toolJSON
		tj.Type = "function"
		tj.Function.Name = t.Name
		tj.Function.Description = t.Description
		if t.ParametersJson != "" {
			tj.Function.Parameters = json.RawMessage(t.ParametersJson)
		} else {
			tj.Function.Parameters = json.RawMessage(`{}`)
		}
		b, err := json.Marshal(tj)
		if err != nil {
			continue
		}
		total += chunk.EstimateTokens(string(b))
	}
	return total
}


// tryComplete sends a non-streaming request. The initial error is
// returned to the caller without yielding, so a context-overflow
// failure can trigger a re-assembly. On success the response is
// yielded and nil is returned.
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

// tryStream opens a streaming request and returns the initial
// error (if any) without yielding it — so the shed loop can
// observe a context-overflow and retry. Once the stream begins
// yielding chunks, further errors are yielded to ADK through the
// iterator (no retry possible past that point, the turn is
// committed).
//
// iter.Pull2 lets us inspect the first emission before committing
// to the ADK iterator: a handshake error appears as (nil, err) on
// pull #1, and we return it for the outer shed loop. Anything
// else means the stream has begun and we're past the retry
// window.
func (r *RRCLLM) tryStream(ctx context.Context, protoReq *pb.CompletionRequest, yield func(*model.LLMResponse, error) bool) error {
	next, stop := iter.Pull2(r.completer.Stream(ctx, protoReq))
	defer stop()

	chunk, err, ok := next()
	if !ok {
		yield(&model.LLMResponse{
			TurnComplete: true,
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: ""}},
			},
		}, nil)
		if r.OnStream != nil {
			r.OnStream("", "", true)
		}
		return nil
	}
	if err != nil {
		return err
	}

	// Track function calls seen during the stream. ADK checks the
	// LAST event to decide whether to continue the tool loop
	// (IsFinalResponse checks hasFunctionCalls). If the last event
	// is an empty Done signal, ADK exits the loop and never
	// executes the tool. The Done response must carry any pending
	// function calls so ADK sees them and continues.
	var pendingCalls []*genai.Part

	for {
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

		chunk, err, ok = next()
		if !ok {
			yield(&model.LLMResponse{
				TurnComplete: true,
				Content: &genai.Content{
					Role:  "model",
					Parts: []*genai.Part{{Text: ""}},
				},
			}, nil)
			if r.OnStream != nil {
				r.OnStream("", "", true)
			}
			return nil
		}
		if err != nil {
			// Mid-stream error after first chunk delivered — yield
			// to ADK, can't retry.
			yield(&model.LLMResponse{
				TurnComplete: true,
				ErrorMessage: err.Error(),
			}, nil)
			return nil
		}
	}
}

// parseToolArgs converts a JSON arguments string to map[string]any for genai.FunctionCall.
func parseToolArgs(argsJSON string) map[string]any {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return map[string]any{"raw": argsJSON}
	}
	return args
}
