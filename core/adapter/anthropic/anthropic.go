package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/core/adapter/internal/util"
	"github.com/elijahmontenegro/grudge/core/httpc"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

const apiVersion = "2023-06-01"

// Config for the Anthropic provider.
type Config struct {
	APIKey  string
	BaseURL string // default: "https://api.anthropic.com"
}

type provider struct {
	cfg    Config
	client *httpc.Client
}

// New creates an Anthropic provider.
func New(cfg Config) any {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com"
	}
	authFn := func(req *http.Request) {
		req.Header.Set("x-api-key", cfg.APIKey)
		req.Header.Set("anthropic-version", apiVersion)
	}
	return &provider{
		cfg:    cfg,
		client: httpc.New(httpc.TimeoutDefault, authFn),
	}
}

func (p *provider) Completer(model string) (core.Completer, error) {
	authFn := func(req *http.Request) {
		req.Header.Set("x-api-key", p.cfg.APIKey)
		req.Header.Set("anthropic-version", apiVersion)
	}
	return &completer{
		model:        model,
		baseURL:      p.cfg.BaseURL,
		client:       p.client,
		streamClient: httpc.NewStreaming(authFn),
	}, nil
}

// --- Completer ---

type completer struct {
	model        string
	baseURL      string
	client       *httpc.Client
	streamClient *httpc.Client
}

type messagesRequest struct {
	Model      string         `json:"model"`
	Messages   []apiMessage   `json:"messages"`
	MaxTokens  int32          `json:"max_tokens"`
	System     string         `json:"system,omitempty"`
	Stream     bool           `json:"stream"`
	Tools      []apiTool      `json:"tools,omitempty"`
	ToolChoice *apiToolChoice `json:"tool_choice,omitempty"`
}

type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// apiToolChoice mirrors Anthropic's tool_choice union: Type selects
// "auto" | "any" | "tool" | "none"; Name is consulted only for "tool".
type apiToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type apiMessage struct {
	Role    string           `json:"role"`
	Content []apiContentPart `json:"content"`
}

type apiContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Thinking is the reasoning text on a "thinking" part — Anthropic
	// nests it here, not in Text.
	Thinking string `json:"thinking,omitempty"`
	// Signature binds a thinking block to its exact text; Anthropic
	// rejects a modified pairing on replay. Empty means unsigned.
	Signature string `json:"signature,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	// Input is tool_use's arguments. Anthropic requires a JSON OBJECT
	// here, not a string — encode always normalizes to a non-empty
	// RawMessage first (never emit "input":null: a nil RawMessage
	// marshals as null, which Anthropic rejects, and omitempty on a
	// non-nil-but-empty RawMessage would emit invalid JSON).
	Input   json.RawMessage `json:"input,omitempty"`
	ToolID  string          `json:"tool_use_id,omitempty"`
	Content string          `json:"content,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
}

type messagesResponse struct {
	ID         string           `json:"id"`
	Content    []apiContentPart `json:"content"`
	Model      string           `json:"model"`
	Usage      apiUsage         `json:"usage"`
	StopReason string           `json:"stop_reason"`
}

// apiUsage decodes Anthropic's usage object. input_tokens EXCLUDES
// the cached portions — Anthropic reports cache writes and reads as
// separate fields — so the true prompt size is the three-way sum
// (promptTotal), never input_tokens alone.
type apiUsage struct {
	InputTokens              int32 `json:"input_tokens"`
	OutputTokens             int32 `json:"output_tokens"`
	CacheCreationInputTokens int32 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int32 `json:"cache_read_input_tokens"`
}

// promptTotal is the full prompt-side token count: freshly evaluated
// plus cache-written plus cache-read — everything occupying the
// model's window on this request.
func (u apiUsage) promptTotal() int32 {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

func (c *completer) Complete(ctx context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
	apiReq, err := toAPIRequest(c.model, req, false)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(apiReq)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := c.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: anthropic returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp messagesResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}

	return &llmv1.CompletionResponse{
		Id:    resp.ID,
		Model: resp.Model,
		Message: &llmv1.LLMMessage{
			Role:    threadv1.Role_ROLE_ASSISTANT,
			Content: fromAPIContent(resp.Content),
		},
		Usage: &llmv1.Usage{
			PromptTokens:     resp.Usage.promptTotal(),
			CompletionTokens: resp.Usage.OutputTokens,
		},
		FinishReason: normalizeStopReason(resp.StopReason),
	}, nil
}

func (c *completer) Stream(ctx context.Context, req *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(yield func(*llmv1.StreamChunk, error) bool) {
		apiReq, err := toAPIRequest(c.model, req, true)
		if err != nil {
			yield(nil, err)
			return
		}
		body, err := json.Marshal(apiReq)
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq, err := http.NewRequest("POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := c.streamClient.Do(ctx, httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		if resp.StatusCode != http.StatusOK {
			err := httpc.NewStatusError("anthropic", resp)
			resp.Body.Close()
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		var promptTokens int32
		// blocks tracks per-index streaming state between
		// content_block_start and content_block_stop: a tool_use
		// block accumulates its input_json_delta fragments (the
		// object arrives piecewise, like OpenAI's argument
		// fragmentation); a thinking block accumulates its
		// signature_delta (arrives once, after the thinking text,
		// before the block closes). Neither is emitted until its
		// content_block_stop — the object isn't complete, and the
		// signature isn't attached, until then.
		blocks := make(map[int]*anthropicBlockState)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := line[6:]
			if data == "[DONE]" {
				return
			}

			var event sseEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(err.Error())}, nil)
				return
			}

			switch event.Type {
			case "message_start":
				// The prompt-side counts arrive ONLY here, nested at
				// message.usage — message_delta carries just the output
				// side. Hold them for the terminal chunk.
				promptTokens = event.Message.Usage.promptTotal()
			case "content_block_start":
				if event.ContentBlock != nil {
					blocks[event.Index] = &anthropicBlockState{
						kind: event.ContentBlock.Type,
						id:   event.ContentBlock.ID,
						name: event.ContentBlock.Name,
					}
				}
			case "content_block_delta":
				switch event.Delta.Type {
				case "text_delta":
					if !yield(&llmv1.StreamChunk{
						Delta: &llmv1.StreamChunk_Text{Text: &threadv1.TextContent{Text: event.Delta.Text}},
					}, nil) {
						return
					}
				case "thinking_delta":
					if !yield(&llmv1.StreamChunk{
						Delta: &llmv1.StreamChunk_Thinking{Thinking: &threadv1.ThinkingContent{Text: event.Delta.Thinking}},
					}, nil) {
						return
					}
				case "input_json_delta":
					if b := blocks[event.Index]; b != nil {
						b.args.WriteString(event.Delta.PartialJSON)
					}
				case "signature_delta":
					if b := blocks[event.Index]; b != nil {
						b.signature = event.Delta.Signature
					}
				}
			case "content_block_stop":
				b := blocks[event.Index]
				delete(blocks, event.Index)
				if b == nil {
					continue
				}
				switch b.kind {
				case "tool_use":
					args := b.args.String()
					if args == "" {
						args = "{}"
					}
					if !yield(&llmv1.StreamChunk{
						Delta: &llmv1.StreamChunk_ToolCall{ToolCall: &threadv1.ToolCallContent{
							Id: b.id, Name: b.name, Arguments: args,
						}},
					}, nil) {
						return
					}
				case "thinking":
					// Only a SIGNED thinking block is worth surfacing —
					// an unsigned one can never be replayed (Anthropic
					// rejects it), so emitting it would just be text the
					// runner stores and later drops. The signature
					// arrives after all the thinking text, so this is a
					// dedicated zero-text chunk: the runner reads it as
					// "attach this signature to the block just
					// accumulated," not as more text to append.
					if b.signature != "" {
						if !yield(&llmv1.StreamChunk{
							Delta: &llmv1.StreamChunk_Thinking{Thinking: &threadv1.ThinkingContent{
								Signature: []byte(b.signature),
							}},
						}, nil) {
							return
						}
					}
				}
			case "message_delta":
				// Some API versions repeat cumulative input-side usage
				// on the delta — trust whichever report is larger (both
				// are lower bounds on the same truth).
				if t := event.Usage.promptTotal(); t > promptTokens {
					promptTokens = t
				}
				yield(&llmv1.StreamChunk{
					Done: true,
					Usage: &llmv1.Usage{
						PromptTokens:     promptTokens,
						CompletionTokens: event.Usage.OutputTokens,
					},
					FinishReason: normalizeStopReason(event.Delta.StopReason),
				}, nil)
				return
			case "error":
				yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(event.Error.Message)}, nil)
				return
			}
		}
		if err := scanner.Err(); err != nil {
			yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(err.Error())}, nil)
		}
	}
}

// anthropicBlockState is per-content-block-index streaming state,
// live between content_block_start and content_block_stop.
type anthropicBlockState struct {
	kind      string // "text" | "thinking" | "tool_use"
	id, name  string
	args      strings.Builder
	signature string
}

type sseEvent struct {
	Type         string           `json:"type"`
	Index        int              `json:"index"`
	ContentBlock *sseContentBlock `json:"content_block,omitempty"`
	Delta        sseDelta         `json:"delta,omitempty"`
	Usage        apiUsage         `json:"usage,omitempty"`
	Error        sseError         `json:"error,omitempty"`
	// Message is populated on message_start events — the prompt-side
	// usage nests inside it, not at the event's top level.
	Message sseMessage `json:"message"`
}

// sseContentBlock is content_block_start's payload — announces a new
// block's type and (for tool_use) its id/name before any deltas.
type sseContentBlock struct {
	Type string `json:"type"` // "text" | "thinking" | "tool_use"
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type sseMessage struct {
	Usage apiUsage `json:"usage"`
}

type sseDelta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Thinking, PartialJSON, and Signature are each populated on a
	// different delta.type: thinking_delta, input_json_delta, and
	// signature_delta respectively.
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	Signature   string `json:"signature,omitempty"`
	// StopReason is populated on message_delta — Anthropic's
	// terminal-stop vocabulary, mapped by normalizeStopReason.
	StopReason string `json:"stop_reason,omitempty"`
}

type sseError struct {
	Message string `json:"message"`
}

// --- helpers ---

func toAPIRequest(model string, req *llmv1.CompletionRequest, stream bool) (messagesRequest, error) {
	var system string
	var msgs []apiMessage
	for _, m := range req.Messages {
		if m.Role == threadv1.Role_ROLE_SYSTEM {
			system = textFromBlocks(m.Content)
			continue
		}
		msgs = append(msgs, apiMessage{
			Role:    roleStr(m.Role),
			Content: toAPIContent(m.Content),
		})
	}

	maxTokens := int32(4096)
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	toolChoice, err := toAPIToolChoice(req.ToolChoice)
	if err != nil {
		return messagesRequest{}, err
	}

	return messagesRequest{
		Model:      model,
		Messages:   msgs,
		MaxTokens:  maxTokens,
		System:     system,
		Stream:     stream,
		Tools:      toAPITools(req.Tools),
		ToolChoice: toolChoice,
	}, nil
}

func toAPITools(tools []*llmv1.ToolDeclaration) []apiTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]apiTool, len(tools))
	for i, t := range tools {
		schema := json.RawMessage(t.ParametersJson)
		if len(schema) == 0 {
			// Anthropic requires an object schema, not an absent one.
			schema = json.RawMessage(`{"type":"object"}`)
		}
		out[i] = apiTool{Name: t.Name, Description: t.Description, InputSchema: schema}
	}
	return out
}

// toAPIToolChoice maps the proto ToolChoice to Anthropic's wire
// shape. Unset or UNSPECIFIED omits the field — the model decides,
// Anthropic's own default. NAMED with an empty tool name can't be
// encoded and is refused rather than silently sent as AUTO.
func toAPIToolChoice(tc *llmv1.ToolChoice) (*apiToolChoice, error) {
	if tc == nil || tc.Mode == llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_UNSPECIFIED {
		return nil, nil
	}
	switch tc.Mode {
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_AUTO:
		return &apiToolChoice{Type: "auto"}, nil
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NONE:
		return &apiToolChoice{Type: "none"}, nil
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED:
		return &apiToolChoice{Type: "any"}, nil
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NAMED:
		if tc.NamedTool == "" {
			return nil, fmt.Errorf("anthropic: tool_choice NAMED requires a non-empty named_tool")
		}
		return &apiToolChoice{Type: "tool", Name: tc.NamedTool}, nil
	default:
		return nil, fmt.Errorf("anthropic: unknown tool_choice mode %v", tc.Mode)
	}
}

func toAPIContent(blocks []*threadv1.ContentBlock) []apiContentPart {
	parts := make([]apiContentPart, 0, len(blocks))
	for _, b := range blocks {
		switch v := b.Block.(type) {
		case *threadv1.ContentBlock_Text:
			parts = append(parts, apiContentPart{Type: "text", Text: v.Text.Text})
		case *threadv1.ContentBlock_Thinking:
			// Anthropic REJECTS a thinking block on replay unless it
			// carries the exact signature the model issued for that
			// exact text — never fabricate one for an unsigned block.
			// A block is replayable only when the model actually
			// signed it (requires extended thinking enabled on the
			// request, which this adapter doesn't do yet — dormant
			// until it does, fixture-tested regardless).
			//
			// Pairing: only the CURRENT turn's thinking block must
			// survive to the next request — it lives in Local Context
			// via turn_id, which RRC never sheds mid-turn (see
			// BuildActiveDiscourse in rrc/local_context.go). A signed
			// thinking block that has aged into deep history and gets
			// replayed without its neighbors is contract-acceptable:
			// Anthropic only requires the signed block on the turn
			// immediately preceding a tool_result, not on every
			// historical tool loop.
			if len(v.Thinking.Signature) == 0 {
				continue
			}
			parts = append(parts, apiContentPart{
				Type:      "thinking",
				Thinking:  v.Thinking.Text,
				Signature: string(v.Thinking.Signature),
			})
		case *threadv1.ContentBlock_ToolCall:
			args := v.ToolCall.Arguments
			if args == "" {
				args = "{}"
			}
			parts = append(parts, apiContentPart{Type: "tool_use", ID: v.ToolCall.Id, Name: v.ToolCall.Name, Input: json.RawMessage(args)})
		case *threadv1.ContentBlock_ToolResult:
			parts = append(parts, apiContentPart{
				Type: "tool_result", ToolID: v.ToolResult.ToolCallId,
				Content: v.ToolResult.Content, IsError: v.ToolResult.IsError,
			})
		}
	}
	return parts
}

func fromAPIContent(parts []apiContentPart) []*threadv1.ContentBlock {
	blocks := make([]*threadv1.ContentBlock, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "text":
			blocks = append(blocks, &threadv1.ContentBlock{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: p.Text}}})
		case "thinking":
			// Anthropic nests thinking text at "thinking", not "text".
			blocks = append(blocks, &threadv1.ContentBlock{Block: &threadv1.ContentBlock_Thinking{Thinking: &threadv1.ThinkingContent{
				Text:      p.Thinking,
				Signature: []byte(p.Signature),
			}}})
		case "tool_use":
			args := string(p.Input)
			if args == "" {
				args = "{}"
			}
			blocks = append(blocks, &threadv1.ContentBlock{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{
				Id: p.ID, Name: p.Name, Arguments: args,
			}}})
		}
	}
	return blocks
}

// normalizeStopReason maps Anthropic's stop_reason vocabulary to the
// grudge-normalized set (llm.proto's CompletionResponse.finish_reason
// doc: "stop", "length", "tool_calls", "content_filter"). Unrecognized
// or empty values pass through raw rather than being silently
// discarded.
func normalizeStopReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	default:
		return reason
	}
}

func roleStr(r threadv1.Role) string {
	switch r {
	case threadv1.Role_ROLE_USER:
		return "user"
	case threadv1.Role_ROLE_ASSISTANT:
		return "assistant"
	default:
		return "user"
	}
}

func textFromBlocks(blocks []*threadv1.ContentBlock) string {
	var s string
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			s += t.Text
		}
	}
	return s
}
