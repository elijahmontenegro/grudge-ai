package adk

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"math"

	"github.com/elijahmontenegro/grudge/core"
	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
	"github.com/elijahmontenegro/grudge/service/storage"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type StreamCallback func(delta, thinking string, done bool)

// RRCLLM intercepts every outbound model call and assembles its RRC
// payload from the persistent Store.
type RRCLLM struct {
	engine    *rrc.Engine
	completer core.Completer
	db        *storage.DB
	threadID  string
	modelName string
	Scope     pb.SelectionScope
	// CurrentTurnID identifies the active discourse for this outbound
	// call — the triggering event plus the model/tool events it spawns
	// share it. Set by the runner before each SendMessage (like Scope).
	// Empty for an autonomous tick's first model call (no event stored
	// yet) or legacy rows, in which case Local Context falls back to the
	// bounded recency window.
	CurrentTurnID string

	OnStream    StreamCallback
	OnSelection func(result *pb.SelectionResult)
	OnEdge      func(edge *pb.Edge)
	OnAssemble  func(t rrc.AssembleTelemetry)
}

func NewRRCLLM(engine *rrc.Engine, completer core.Completer, db *storage.DB, threadID, modelName string) *RRCLLM {
	return &RRCLLM{
		engine: engine, completer: completer, db: db,
		threadID: threadID, modelName: modelName,
	}
}

func (r *RRCLLM) Name() string { return r.modelName }

func (r *RRCLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		systemMsg := systemMessage(req)
		protoTools := toolDeclarations(req)

		threadCorpus, err := r.db.ThreadCorpus(r.threadID)
		if err != nil {
			yield(nil, fmt.Errorf("load thread corpus: %w", err))
			return
		}
		if len(threadCorpus) == 0 {
			r.forwardCurrentTurn(ctx, req, stream, systemMsg, protoTools, yield)
			return
		}

		anchor := threadCorpus[len(threadCorpus)-1]
		corpus := threadCorpus
		if r.Scope == pb.SelectionScope_SELECTION_SCOPE_ALL_THREADS {
			corpus, err = r.db.AllCorpus()
			if err != nil {
				yield(nil, fmt.Errorf("load candidate corpus: %w", err))
				return
			}
		}

		cfg := r.engine.Config()
		// Active-discourse Local Context: the in-flight turn's messages
		// (triggering event + model/tool events it spawned), not a fixed
		// last-N recency window. Falls back to bounded recency when no
		// turn identity is available (autonomous first call / legacy rows).
		localContext := rrc.BuildActiveDiscourse(threadCorpus, r.CurrentTurnID, cfg.LocalContextSize)
		if len(localContext) == 0 {
			yield(nil, fmt.Errorf("RRC: no Local Context for stored anchor %s", anchor.Id))
			return
		}

		// ADK supplies no current content for an autonomous continuation
		// without a newly stored event. Such a tick keeps native Local
		// Context but does not invent a serialization or audit event.
		var serializedLocal *rrc.SerializedLocalContext
		if len(req.Contents) > 0 {
			serializedLocal = rrc.SerializeLocalContext(localContext, cfg.Chunk)
		}

		var excludeIDs []string
		published := false
		// priorSelection carries the first attempt's selection into any
		// context-overflow retry so the retry reuses it (A5) — one outbound
		// call owns one selector input and one selection event. Retries only
		// re-run shed-to-fit (dropping whole groups via excludeIDs); they do
		// not re-select, re-walk provenance, or re-emit edges.
		var priorSelection *pb.SelectionResult
		for {
			result, err := r.engine.Assemble(ctx, rrc.AssembleRequest{
				SerializedLocalContext: serializedLocal, Anchor: anchor, Corpus: corpus, LocalContext: localContext,
				Scope: r.Scope, ThreadID: r.threadID, System: systemMsg,
				Budget: cfg.ContextBudgetTokens, HeadroomPct: cfg.BudgetHeadroomPct,
				PerMsgDelim:    cfg.PerMsgDelimiterTokens,
				FixedTokens:    estimateToolSchemaTokens(protoTools),
				ExcludeIDs:     excludeIDs,
				PriorSelection: priorSelection,
			})
			if err != nil {
				yield(nil, err)
				return
			}

			if !published {
				if r.OnEdge != nil {
					for _, edge := range result.Edges {
						r.OnEdge(edge)
					}
				}
				if result.SerializedLocalContext != nil && r.OnSelection != nil {
					r.OnSelection(result.Selection)
				}
				published = true
			}
			// Reuse this selection on any subsequent overflow retry.
			priorSelection = result.Selection
			if r.OnAssemble != nil {
				r.OnAssemble(result.Telemetry)
			}
			t := result.Telemetry
			log.Printf("RRC: assembly thread=%s selected=%d local=%d closure=%d shed=%d total=%d budget=%d wire=%d",
				r.threadID, t.SelectedCount, t.LocalContextCount, t.ClosureCount,
				t.SheddedCount, t.TotalTokens, t.EffectiveBudget, len(result.Wire))

			protoReq := completionRequest(req, stream, result.Wire, protoTools)
			var sendErr error
			if stream {
				sendErr = r.tryStream(ctx, protoReq, yield)
			} else {
				sendErr = r.tryComplete(ctx, protoReq, yield)
			}
			if sendErr == nil {
				r.recordProvenance(anchor, localContext, result.Selection)
				return
			}
			if !core.IsContextOverflow(sendErr) {
				yield(nil, sendErr)
				return
			}
			drop := lowestSurvivingScore(result.Selection, excludeIDs)
			if drop == "" {
				yield(nil, fmt.Errorf("context window exceeded after shedding all selected groups: %w", sendErr))
				return
			}
			excludeIDs = append(excludeIDs, drop)
		}
	}
}

// recordProvenance records, after a successful generation, that this turn
// was generated from its Local Context (the active discourse, definitionally
// load-bearing → weight 1.0) plus the selected prerequisites (weight = their
// effective selection score). anchor is the turn's triggering event. Edges
// are recorded into the DAG and persisted via OnEdge (same path as
// cross-encoder edges; the (from,to,source) primary key lets them coexist).
// A failed send records nothing — provenance is a fact about what actually
// fed a completed turn.
func (r *RRCLLM) recordProvenance(anchor *pb.Message, localContext []*pb.Message, selection *pb.SelectionResult) {
	if anchor == nil {
		return
	}
	var contributors []rrc.Contributor
	for _, m := range localContext {
		contributors = append(contributors, rrc.Contributor{
			MessageID: m.Id, ThreadID: m.ThreadId, Weight: 1.0,
		})
	}
	if selection != nil {
		for _, s := range selection.Selected {
			contributors = append(contributors, rrc.Contributor{
				MessageID: s.MessageId, ThreadID: s.ThreadId, Weight: float64(s.EffectiveScore),
			})
		}
	}
	edges := r.engine.RecordProvenance(anchor, contributors)
	if r.OnEdge != nil {
		for _, edge := range edges {
			r.OnEdge(edge)
		}
	}
}

func systemMessage(req *model.LLMRequest) *pb.LLMMessage {
	if req.Config == nil || req.Config.SystemInstruction == nil {
		return nil
	}
	message := GenaiContentToProto(req.Config.SystemInstruction)
	message.Role = pb.Role_ROLE_SYSTEM
	return message
}

func toolDeclarations(req *model.LLMRequest) []*pb.ToolDeclaration {
	if req.Config == nil {
		return nil
	}
	var tools []*pb.ToolDeclaration
	for _, group := range req.Config.Tools {
		for _, declaration := range group.FunctionDeclarations {
			parameters := `{"type":"object","properties":{}}`
			if declaration.ParametersJsonSchema != nil {
				if data, err := json.Marshal(declaration.ParametersJsonSchema); err == nil {
					parameters = string(data)
				}
			} else if declaration.Parameters != nil {
				if data, err := json.Marshal(declaration.Parameters); err == nil {
					parameters = string(data)
				}
			}
			tools = append(tools, &pb.ToolDeclaration{
				Name: declaration.Name, Description: declaration.Description,
				ParametersJson: parameters,
			})
		}
	}
	return tools
}

func completionRequest(req *model.LLMRequest, stream bool, messages []*pb.LLMMessage, tools []*pb.ToolDeclaration) *pb.CompletionRequest {
	out := &pb.CompletionRequest{Messages: messages, Model: req.Model, Stream: stream, Tools: tools}
	if req.Config == nil {
		return out
	}
	if req.Config.Temperature != nil {
		value := float32(*req.Config.Temperature)
		out.Temperature = &value
	}
	if req.Config.TopP != nil {
		value := float32(*req.Config.TopP)
		out.TopP = &value
	}
	if req.Config.MaxOutputTokens > 0 {
		out.MaxTokens = &req.Config.MaxOutputTokens
	}
	return out
}

func (r *RRCLLM) forwardCurrentTurn(ctx context.Context, req *model.LLMRequest, stream bool, system *pb.LLMMessage, tools []*pb.ToolDeclaration, yield func(*model.LLMResponse, error) bool) {
	var messages []*pb.LLMMessage
	if system != nil {
		messages = append(messages, system)
	}
	for _, content := range req.Contents {
		messages = append(messages, GenaiContentToProto(content))
	}
	protoReq := completionRequest(req, stream, messages, tools)
	var err error
	if stream {
		err = r.tryStream(ctx, protoReq, yield)
	} else {
		err = r.tryComplete(ctx, protoReq, yield)
	}
	if err != nil {
		yield(nil, err)
	}
}

func lowestSurvivingScore(result *pb.SelectionResult, excludedIDs []string) string {
	if result == nil {
		return ""
	}
	excluded := make(map[string]bool, len(excludedIDs))
	for _, id := range excludedIDs {
		excluded[id] = true
	}
	var pick string
	lowest := math.Inf(1)
	for _, selected := range result.Selected {
		if !excluded[selected.MessageId] && float64(selected.EffectiveScore) < lowest {
			pick, lowest = selected.MessageId, float64(selected.EffectiveScore)
		}
	}
	return pick
}

func estimateToolSchemaTokens(tools []*pb.ToolDeclaration) int {
	type toolJSON struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	var total int
	for _, tool := range tools {
		var value toolJSON
		value.Type = "function"
		value.Function.Name = tool.Name
		value.Function.Description = tool.Description
		value.Function.Parameters = json.RawMessage(tool.ParametersJson)
		if len(value.Function.Parameters) == 0 {
			value.Function.Parameters = json.RawMessage(`{}`)
		}
		if data, err := json.Marshal(value); err == nil {
			total += chunk.EstimateTokens(string(data))
		}
	}
	return total
}

func (r *RRCLLM) tryComplete(ctx context.Context, req *pb.CompletionRequest, yield func(*model.LLMResponse, error) bool) error {
	response, err := r.completer.Complete(ctx, req)
	if err != nil {
		return err
	}
	yield(&model.LLMResponse{Content: ProtoResponseToGenai(response), TurnComplete: true}, nil)
	return nil
}

func (r *RRCLLM) tryStream(ctx context.Context, req *pb.CompletionRequest, yield func(*model.LLMResponse, error) bool) error {
	next, stop := iter.Pull2(r.completer.Stream(ctx, req))
	defer stop()

	chunk, err, ok := next()
	if !ok {
		return r.finishEmpty(yield)
	}
	if err != nil {
		return err
	}
	var pendingCalls []*genai.Part
	for {
		response := &model.LLMResponse{Partial: true}
		if text := chunk.GetText(); text != nil {
			response.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{Text: text.Text}}}
			if r.OnStream != nil {
				r.OnStream(text.Text, "", false)
			}
		}
		if thinking := chunk.GetThinking(); thinking != nil {
			response.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{Text: thinking.Text, Thought: true}}}
			if r.OnStream != nil {
				r.OnStream("", thinking.Text, false)
			}
		}
		if call := chunk.GetToolCall(); call != nil {
			part := &genai.Part{FunctionCall: &genai.FunctionCall{
				ID: call.Id, Name: call.Name, Args: parseToolArgs(call.Arguments),
			}}
			pendingCalls = append(pendingCalls, part)
			response.Content = &genai.Content{Role: "model", Parts: []*genai.Part{part}}
		}
		if chunk.Done {
			response.Partial = false
			response.TurnComplete = true
			if len(pendingCalls) > 0 {
				response.Content = &genai.Content{Role: "model", Parts: pendingCalls}
			} else if response.Content == nil {
				response.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{Text: ""}}}
			}
			if chunk.Error != nil {
				response.ErrorMessage = *chunk.Error
			}
			if r.OnStream != nil {
				r.OnStream("", "", true)
			}
			yield(response, nil)
			return nil
		}
		if !yield(response, nil) {
			return nil
		}

		chunk, err, ok = next()
		if !ok {
			return r.finishEmpty(yield)
		}
		if err != nil {
			yield(&model.LLMResponse{TurnComplete: true, ErrorMessage: err.Error()}, nil)
			return nil
		}
	}
}

func (r *RRCLLM) finishEmpty(yield func(*model.LLMResponse, error) bool) error {
	yield(&model.LLMResponse{
		TurnComplete: true,
		Content:      &genai.Content{Role: "model", Parts: []*genai.Part{{Text: ""}}},
	}, nil)
	if r.OnStream != nil {
		r.OnStream("", "", true)
	}
	return nil
}

func parseToolArgs(raw string) map[string]any {
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return map[string]any{"raw": raw}
	}
	return args
}
