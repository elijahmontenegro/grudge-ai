// Package adkbridge makes Google ADK speak RRC: RRCLLM implements
// ADK's model interface, intercepts every outbound call, and assembles
// its payload through rrc.Engine.Assemble against the lossless corpus.
// The corpus comes through the consumer-defined CorpusStore interface,
// so the bridge has no storage dependency — any store returning
// []*threadv1.Message (grudge's *storage.DB satisfies it structurally) works.
package adkbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"
	"math"
	"sort"

	"github.com/elijahmontenegro/grudge/core"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/elijahmontenegro/grudge/core/genaicodec"
)

type StreamCallback func(delta, thinking string, done bool)

// TokenScales grounds the pre-send token counter in provider-reported
// usage: Scale converts the model-truth budget into counter units;
// Observe feeds back one (predicted, observed) pair per successful
// send, with the counter-unit budget the assembly ran under.
// Consumer-defined interface (rrc/tokenscale's *Bound satisfies it);
// nil means ungrounded — the budget is used unconverted and nothing
// is observed.
type TokenScales interface {
	Scale() float64
	Observe(predicted, observed, budget int) error
}

// CorpusStore is the bounded message access Assemble needs — the selected
// set's content and its protocol counterparts, never the full corpus.
// Consumer-defined interface; grudge's *storage.DB satisfies it structurally,
// and its shape matches rrc.CorpusStore so it flows straight into
// AssembleRequest.Store.
type CorpusStore interface {
	// Local Context window construction (bounded, invariant under corpus
	// growth): the current turn and its immediately preceding turn, each
	// fetched turn-complete, plus the recency gate/fallback.
	RecentMessages(threadID string, n int) ([]*threadv1.Message, error)
	TurnMessages(threadID, turnID string) ([]*threadv1.Message, error)
	PrecedingTurnID(threadID, currentTurnID string) (string, error)
	// Assemble's Store (also the rrc.CorpusStore shape).
	Messages(ids []string) (map[string]*threadv1.Message, error)
	TurnPeers(msgs []*threadv1.Message) ([]*threadv1.Message, error)
}

// mergeByPosition unions two message slices, dedups by id, and sorts ascending
// by position — the ordered Local Context window BuildActiveDiscourse expects,
// built from the bounded turn + recency fetches.
func mergeByPosition(a, b []*threadv1.Message) []*threadv1.Message {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]*threadv1.Message, 0, len(a)+len(b))
	for _, src := range [][]*threadv1.Message{a, b} {
		for _, m := range src {
			if !seen[m.Id] {
				seen[m.Id] = true
				out = append(out, m)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

// RRCLLM intercepts every outbound model call and assembles its RRC
// payload from the persistent Store.
type RRCLLM struct {
	engine    *rrc.Engine
	completer core.Completer
	db        CorpusStore
	threadID  string
	modelName string
	Scope     threadv1.SelectionScope
	// CurrentTurnID identifies the active discourse for this outbound
	// call — the triggering event plus the model/tool events it spawns
	// share it. Set by the runner before each SendMessage (like Scope).
	// Empty for an autonomous tick's first model call (no event stored
	// yet), in which case Local Context falls back to the bounded
	// recency window.
	CurrentTurnID string

	OnStream    StreamCallback
	OnSelection func(result *rrcv1.SelectionResult)
	OnEdge      func(edge *rrcv1.Edge)
	OnAssemble  func(t rrc.AssembleTelemetry)
	// OnUsage fires once per completed model call with the assembler's
	// counter-unit prediction for the sent wire and the provider's
	// reported usage — one atomic event, so a consumer can never pair
	// a prediction with another call's usage. predicted is 0 on the
	// empty-corpus forwarding path (no assembly ran).
	OnUsage func(predicted int, usage *llmv1.Usage)

	// Scales grounds budget conversion and receives observations.
	// Optional; set at composition alongside the callbacks.
	Scales TokenScales
	// CountText is the adapter's counting projection (what the codec
	// actually sends per message). Optional; nil counts every block.
	CountText func(*llmv1.LLMMessage) string
}

func NewRRCLLM(engine *rrc.Engine, completer core.Completer, db CorpusStore, threadID, modelName string) *RRCLLM {
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

		cfg := r.engine.Config()
		// Local Context is the in-flight WINDOW: the immediately preceding
		// turn ∪ the current turn record, each fetched TURN-COMPLETE from
		// bounded, indexed reads — never the full corpus and never a
		// size-truncated slice (a recency LIMIT decapitates a long
		// preceding turn, and loses it entirely once the current turn
		// outgrows the limit — which is exactly how the old spine went
		// blind mid-tool-loop). The window serves both consumers: the
		// network sees it whole (tools included — the model is never blind
		// to the exchange it is continuing), and the engine takes
		// membership, exclusion, and mass-walk seeds from it while the
		// QUERY stays the current turn's semantic projection. The recency
		// fetch survives as the empty-corpus gate and the no-turn-identity
		// fallback window (an autonomous tick with nothing stored yet).
		recent, err := r.db.RecentMessages(r.threadID, cfg.LocalContextSize)
		if err != nil {
			yield(nil, fmt.Errorf("load recent messages: %w", err))
			return
		}
		if len(recent) == 0 {
			r.forwardCurrentTurn(ctx, req, stream, systemMsg, protoTools, yield)
			return
		}
		turnMsgs, err := r.db.TurnMessages(r.threadID, r.CurrentTurnID)
		if err != nil {
			yield(nil, fmt.Errorf("load turn messages: %w", err))
			return
		}
		window := recent
		effTurnID := ""
		if r.CurrentTurnID != "" && len(turnMsgs) > 0 {
			effTurnID = r.CurrentTurnID
			precedingID, perr := r.db.PrecedingTurnID(r.threadID, r.CurrentTurnID)
			if perr != nil {
				yield(nil, fmt.Errorf("load preceding turn id: %w", perr))
				return
			}
			var preceding []*threadv1.Message
			if precedingID != "" {
				if preceding, err = r.db.TurnMessages(r.threadID, precedingID); err != nil {
					yield(nil, fmt.Errorf("load preceding turn: %w", err))
					return
				}
			}
			window = mergeByPosition(preceding, turnMsgs)
		}

		// The turn RECORD — the in-flight suffix of the window — still
		// derives the anchor (and, after generation, the provenance
		// contributors). The window-tail is delivery and graph seeding,
		// never discourse focus.
		turnRecord := rrc.BuildActiveDiscourse(window, effTurnID, cfg.LocalContextSize)
		localContext := rrc.SemanticMessages(turnRecord)
		if len(localContext) == 0 {
			yield(nil, fmt.Errorf("RRC: no semantic Local Context in the active turn (%d record messages)", len(turnRecord)))
			return
		}
		// The anchor is the last SEMANTIC message of the current turn — the
		// discourse focus (the trigger at call 1, the latest thinking
		// mid-turn) — never a trailing tool_result: edges, selections, and
		// provenance target the reasoning, not mechanical blocks.
		anchor := localContext[len(localContext)-1]

		// ADK supplies no current content for an autonomous continuation
		// without a newly stored event. Such a tick keeps native Local
		// Context but does not invent a serialization or audit event.
		// Serialized over the WINDOW: MessageIDs — membership: exclusion,
		// mass-walk seeds, audit — cover everything delivered, tools and
		// tail included, so nothing delivered is ever re-retrieved; the
		// query Chunks stay the current turn's semantic projection via the
		// discriminator.
		var serializedLocal *rrc.SerializedLocalContext
		if len(req.Contents) > 0 {
			serializedLocal = rrc.SerializeLocalContext(window, effTurnID, cfg.Chunk)
		}

		// Budget conversion: ContextBudgetTokens is model truth (the
		// window the user declared); the engine counts in the configured
		// counter's units. The learned scale — grounded in the provider's
		// own reported usage — converts between them. Ungrounded (no
		// scale yet) means the budget passes through unconverted.
		budget := cfg.ContextBudgetTokens
		scale := 0.0
		if r.Scales != nil {
			if s := r.Scales.Scale(); s > 0 {
				scale = s
				budget = int(float64(cfg.ContextBudgetTokens) / s)
			}
		}

		var excludeIDs []string
		published := false
		// priorSelection carries the first attempt's selection into any
		// context-overflow retry so the retry reuses it (A5) — one outbound
		// call owns one selector input and one selection event. Retries only
		// re-run shed-to-fit (dropping whole groups via excludeIDs); they do
		// not re-select, re-walk provenance, or re-emit edges.
		var priorSelection *rrcv1.SelectionResult
		for {
			result, err := r.engine.Assemble(ctx, rrc.AssembleRequest{
				SerializedLocalContext: serializedLocal, Anchor: anchor, Store: r.db, LocalContext: window,
				CurrentTurnID: effTurnID,
				Scope:         r.Scope, ThreadID: r.threadID, System: systemMsg,
				Budget: budget, HeadroomPct: cfg.BudgetHeadroomPct,
				PerMsgDelim:    cfg.PerMsgDelimiterTokens,
				FixedTokens:    estimateToolSchemaTokens(protoTools, cfg.Chunk),
				ExcludeIDs:     excludeIDs,
				PriorSelection: priorSelection,
				CountText:      r.CountText,
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
			log.Printf("RRC: assembly thread=%s selected=%d local=%d closure=%d shed=%d total=%d budget=%d scale=%.3f wire=%d",
				r.threadID, t.SelectedCount, t.LocalContextCount, t.ClosureCount,
				t.SheddedCount, t.TotalTokens, t.EffectiveBudget, scale, len(result.Wire))

			protoReq := completionRequest(req, stream, result.Wire, protoTools, cfg.ContextBudgetTokens)
			var usage *llmv1.Usage
			var sendErr error
			if stream {
				usage, sendErr = r.tryStream(ctx, protoReq, yield)
			} else {
				usage, sendErr = r.tryComplete(ctx, protoReq, yield)
			}
			if sendErr == nil {
				r.observeUsage(t.TotalTokens, budget, usage)
				// The whole delivered turn record (tools included) fed the
				// generation — provenance is a fact about what reached the
				// wire, not just the semantic discourse.
				r.recordProvenance(anchor, turnRecord, result.Selection)
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

// observeUsage feeds telemetry and the grounding learner after a
// successful send. usage is nil when the provider reported nothing or
// the stream ended without a clean terminal chunk — nil is not an
// observation. An admission rejection or persistence failure is
// logged loudly and never fails the turn (which already succeeded).
func (r *RRCLLM) observeUsage(predicted, budget int, usage *llmv1.Usage) {
	if usage == nil {
		return
	}
	if r.OnUsage != nil {
		r.OnUsage(predicted, usage)
	}
	if r.Scales == nil || usage.PromptTokens <= 0 {
		return
	}
	if err := r.Scales.Observe(predicted, int(usage.PromptTokens), budget); err != nil {
		log.Printf("RRC: token-scale observation failed thread=%s model=%s: %v", r.threadID, r.modelName, err)
	}
}

// recordProvenance records, after a successful generation, that this turn
// was generated from its delivered turn record (the active turn as the
// model saw it — semantic discourse and tool steps alike, definitionally
// load-bearing → weight 1.0) plus the selected prerequisites at their RAW
// dependency evidence (provenance_weight: the via-path product of raw
// cross-encoder scores). Never the effective score: that is the
// calibrated, mass-lifted, MMR-rewritten posterior, and banking it feeds
// the lift back into the next turn's mass — a measured self-reinforcing
// echo (raw sim 0.36 banked as 0.99, re-lifted every turn) with MMR's
// negative rewrites polluting the graph for free. Raw products < 1
// self-damp across hops and turns. The window-tail (the delivered
// preceding turn) is NEVER a contributor: an edge that would exist for
// every turn regardless of content encodes zero information — positional
// adjacency must not become graph weight. anchor is the turn's current
// semantic focus. Edges are recorded into the DAG and persisted via
// OnEdge (same path as cross-encoder edges; the (from,to,source) primary
// key lets them coexist). A failed send records nothing — provenance is
// a fact about what actually fed a completed turn.
func (r *RRCLLM) recordProvenance(anchor *threadv1.Message, turnRecord []*threadv1.Message, selection *rrcv1.SelectionResult) {
	if anchor == nil {
		return
	}
	var contributors []rrc.Contributor
	for _, m := range turnRecord {
		contributors = append(contributors, rrc.Contributor{
			MessageID: m.Id, ThreadID: m.ThreadId, Weight: 1.0,
		})
	}
	if selection != nil {
		for _, s := range selection.Selected {
			// Zero-weight entries carry no recorded evidence (legacy
			// blobs predating the field, or edges whose raw CE was never
			// set) — a weightless edge adds rows, not mass. Skip.
			if s.ProvenanceWeight <= 0 {
				continue
			}
			contributors = append(contributors, rrc.Contributor{
				MessageID: s.MessageId, ThreadID: s.ThreadId, Weight: float64(s.ProvenanceWeight),
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

func systemMessage(req *model.LLMRequest) *llmv1.LLMMessage {
	if req.Config == nil || req.Config.SystemInstruction == nil {
		return nil
	}
	message := genaicodec.ContentToProto(req.Config.SystemInstruction)
	message.Role = threadv1.Role_ROLE_SYSTEM
	return message
}

func toolDeclarations(req *model.LLMRequest) []*llmv1.ToolDeclaration {
	if req.Config == nil {
		return nil
	}
	var tools []*llmv1.ToolDeclaration
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
			tools = append(tools, &llmv1.ToolDeclaration{
				Name: declaration.Name, Description: declaration.Description,
				ParametersJson: parameters,
			})
		}
	}
	return tools
}

func completionRequest(req *model.LLMRequest, stream bool, messages []*llmv1.LLMMessage, tools []*llmv1.ToolDeclaration, contextWindow int) *llmv1.CompletionRequest {
	out := &llmv1.CompletionRequest{Messages: messages, Model: req.Model, Stream: stream, Tools: tools}
	if contextWindow > 0 {
		window := int32(contextWindow)
		out.ContextWindowTokens = &window
	}
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

func (r *RRCLLM) forwardCurrentTurn(ctx context.Context, req *model.LLMRequest, stream bool, system *llmv1.LLMMessage, tools []*llmv1.ToolDeclaration, yield func(*model.LLMResponse, error) bool) {
	var messages []*llmv1.LLMMessage
	if system != nil {
		messages = append(messages, system)
	}
	for _, content := range req.Contents {
		messages = append(messages, genaicodec.ContentToProto(content))
	}
	protoReq := completionRequest(req, stream, messages, tools, r.engine.Config().ContextBudgetTokens)
	var usage *llmv1.Usage
	var err error
	if stream {
		usage, err = r.tryStream(ctx, protoReq, yield)
	} else {
		usage, err = r.tryComplete(ctx, protoReq, yield)
	}
	if err != nil {
		yield(nil, err)
		return
	}
	// No assembly ran on this path — there is no prediction to pair,
	// so the learner is not fed (observeUsage skips predicted 0), but
	// telemetry still sees the reported usage.
	r.observeUsage(0, 0, usage)
}

func lowestSurvivingScore(result *rrcv1.SelectionResult, excludedIDs []string) string {
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

func estimateToolSchemaTokens(tools []*llmv1.ToolDeclaration, chunkCfg chunk.Config) int {
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
			total += chunkCfg.Estimate(string(data))
		}
	}
	return total
}

// tryComplete sends the request and returns the provider's reported
// usage alongside the transport error. Usage is non-nil only on a
// successful response that carried it.
func (r *RRCLLM) tryComplete(ctx context.Context, req *llmv1.CompletionRequest, yield func(*model.LLMResponse, error) bool) (*llmv1.Usage, error) {
	response, err := r.completer.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	yield(&model.LLMResponse{Content: genaicodec.ProtoToContent(response.Message), TurnComplete: true}, nil)
	return response.Usage, nil
}

// tryStream sends the request and returns the provider's reported
// usage alongside the transport error. The usage contract is strict:
// non-nil iff a terminal chunk with Done and no Error carried it.
// Consumer abort, a stream that ends without a Done chunk, a
// mid-stream error, and a provider error chunk all return nil usage —
// no usage means no observation.
func (r *RRCLLM) tryStream(ctx context.Context, req *llmv1.CompletionRequest, yield func(*model.LLMResponse, error) bool) (*llmv1.Usage, error) {
	next, stop := iter.Pull2(r.completer.Stream(ctx, req))
	defer stop()

	chunk, err, ok := next()
	if !ok {
		return nil, r.finishEmpty(yield)
	}
	if err != nil {
		return nil, err
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
			response.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{
				Text: thinking.Text, Thought: true, ThoughtSignature: thinking.Signature,
			}}}
			// A signature-only terminator (e.g. Anthropic's zero-text
			// chunk closing a signed thinking block) carries no new
			// text to show — skip the callback so it doesn't fire an
			// empty delta.
			if r.OnStream != nil && thinking.Text != "" {
				r.OnStream("", thinking.Text, false)
			}
		}
		if call := chunk.GetToolCall(); call != nil {
			part := &genai.Part{FunctionCall: &genai.FunctionCall{
				ID: call.Id, Name: call.Name, Args: parseToolArgs(call.Arguments),
			}, ThoughtSignature: call.Signature}
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
			// A provider error chunk is a failed generation — its usage
			// (if any) describes an aborted call and must not ground the
			// learner.
			if chunk.Error != nil {
				return nil, nil
			}
			return chunk.Usage, nil
		}
		if !yield(response, nil) {
			return nil, nil
		}

		chunk, err, ok = next()
		if !ok {
			return nil, r.finishEmpty(yield)
		}
		if err != nil {
			yield(&model.LLMResponse{TurnComplete: true, ErrorMessage: err.Error()}, nil)
			return nil, nil
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
