package openai

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
	"github.com/elijahmontenegro/grudge/core/adapter/internal/chatwire"
	"github.com/elijahmontenegro/grudge/core/adapter/internal/util"
	"github.com/elijahmontenegro/grudge/core/httpc"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

// Config for the OpenAI provider. Configurable BaseURL supports any
// OpenAI-compatible endpoint.
type Config struct {
	APIKey  string
	BaseURL string // default: "https://api.openai.com"
}

type provider struct {
	cfg    Config
	authFn func(*http.Request)
	client *httpc.Client
}

// New creates an OpenAI provider (or any OpenAI-compatible endpoint).
func New(cfg Config) any {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	p := &provider{cfg: cfg}
	// Skip the Authorization header entirely when no key is set. An empty
	// key means "no auth" (local vLLM / Ollama-compatible servers, which
	// reject a literal `Bearer ` with an empty token) — not `Bearer `.
	p.authFn = func(req *http.Request) {
		if cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}
	}
	p.client = httpc.New(httpc.TimeoutDefault, p.authFn)
	return p
}

func (p *provider) Completer(model string) (core.Completer, error) {
	return &completer{
		model:        model,
		baseURL:      p.cfg.BaseURL,
		client:       p.client,
		streamClient: httpc.NewStreaming(p.authFn),
	}, nil
}

func (p *provider) Embedder(model string) (core.Embedder, error) {
	return &embedder{
		model:   model,
		baseURL: p.cfg.BaseURL,
		client:  p.client,
	}, nil
}

// --- Completer ---

type completer struct {
	model        string
	baseURL      string
	client       *httpc.Client
	streamClient *httpc.Client
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   *int32        `json:"max_tokens,omitempty"`
	Temperature *float32      `json:"temperature,omitempty"`
	TopP        *float32      `json:"top_p,omitempty"`
	Stop        []string      `json:"stop,omitempty"`
	Stream      bool          `json:"stream"`
	// StreamOptions requests the final usage frame on streamed
	// completions — without it OpenAI omits usage from streams
	// entirely. Standard since mid-2024; a compat endpoint that
	// rejects it fails loudly rather than silently losing the
	// ground-truth token counts.
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	Tools         []chatTool     `json:"tools,omitempty"`
	// ToolChoice is either the bare string "auto"/"none"/"required" or
	// {"type":"function","function":{"name":...}} — the shape varies
	// by mode, so it's built directly as `any` rather than a fixed
	// struct with omitted fields.
	ToolChoice any `json:"tool_choice,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatTool struct {
	Type     string           `json:"type"` // "function"
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type namedToolChoice struct {
	Type     string              `json:"type"` // "function"
	Function namedToolChoiceFunc `json:"function"`
}

type namedToolChoiceFunc struct {
	Name string `json:"name"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	// Index identifies which parallel call a streamed argument
	// fragment belongs to. Present on stream deltas; absent (and
	// ignored) on a complete non-stream response or on replay.
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type chatResponse struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   apiUsage     `json:"usage"`
}

type chatChoice struct {
	Message      chatMessage `json:"message"`
	Delta        chatMessage `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

type apiUsage struct {
	PromptTokens     int32 `json:"prompt_tokens"`
	CompletionTokens int32 `json:"completion_tokens"`
}

func (c *completer) Complete(ctx context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
	creq, err := toChatRequest(c.model, req, false)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(creq)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := c.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: openai returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp chatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("%w: openai returned no choices", core.ErrProviderUnavailable)
	}

	return &llmv1.CompletionResponse{
		Id:    resp.ID,
		Model: resp.Model,
		Message: &llmv1.LLMMessage{
			Role:    threadv1.Role_ROLE_ASSISTANT,
			Content: fromChatMessage(resp.Choices[0].Message),
		},
		Usage: &llmv1.Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
		},
		FinishReason: normalizeFinishReason(resp.Choices[0].FinishReason),
	}, nil
}

func (c *completer) Stream(ctx context.Context, req *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(yield func(*llmv1.StreamChunk, error) bool) {
		creq, err := toChatRequest(c.model, req, true)
		if err != nil {
			yield(nil, err)
			return
		}
		body, err := json.Marshal(creq)
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq, err := http.NewRequest("POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
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
			err := httpc.NewStatusError("openai", resp)
			resp.Body.Close()
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		var totalUsage *llmv1.Usage
		var finishReason string
		calls := newToolCallAccumulator()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := line[6:]
			if data == "[DONE]" {
				// Tool calls are fragmented across many deltas keyed by
				// index; only [DONE] guarantees every fragment has
				// arrived, so reassembly emits here rather than per-delta.
				if !calls.emit(yield) {
					return
				}
				yield(&llmv1.StreamChunk{Done: true, Usage: totalUsage, FinishReason: finishReason}, nil)
				return
			}

			var chunk chatResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(err.Error())}, nil)
				return
			}

			if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
				totalUsage = &llmv1.Usage{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
				}
			}

			if len(chunk.Choices) == 0 {
				continue
			}

			choice := chunk.Choices[0]
			if choice.FinishReason != "" {
				finishReason = normalizeFinishReason(choice.FinishReason)
			}
			content, _ := choice.Delta.Content.(string)
			if content != "" {
				if !yield(&llmv1.StreamChunk{
					Delta: &llmv1.StreamChunk_Text{Text: &threadv1.TextContent{Text: content}},
				}, nil) {
					return
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				if err := calls.add(tc); err != nil {
					yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(err.Error())}, nil)
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			yield(&llmv1.StreamChunk{Done: true, Error: util.Ptr(err.Error())}, nil)
		}
	}
}

// toolCallAccumulator reassembles OpenAI's fragmented streaming tool
// calls: each delta carries one fragment of one call's arguments,
// keyed by an index shared across the whole stream (id/name arrive
// only on the fragment that starts a call). Real OpenAI traffic
// always introduces indices in ascending order, so first-seen order
// already matches index order — no explicit sort is needed.
type toolCallAccumulator struct {
	order []*toolCallBuild
	byIdx map[int]*toolCallBuild
}

type toolCallBuild struct {
	id, name string
	args     strings.Builder
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIdx: make(map[int]*toolCallBuild)}
}

// add ingests one streamed tool-call fragment. Compat endpoints that
// omit the index field are handled leniently: a fragment carrying an
// id starts a new call, a fragment with no id continues the most
// recently started one. A continuation before any call has started is
// a malformed stream — fail fast rather than guess.
func (a *toolCallAccumulator) add(tc toolCall) error {
	var b *toolCallBuild
	switch {
	case tc.Index != nil:
		b = a.byIdx[*tc.Index]
		if b == nil {
			b = &toolCallBuild{}
			a.byIdx[*tc.Index] = b
			a.order = append(a.order, b)
		}
	case tc.ID != "":
		b = &toolCallBuild{}
		a.order = append(a.order, b)
	case len(a.order) > 0:
		b = a.order[len(a.order)-1]
	default:
		return fmt.Errorf("openai stream: tool_call fragment has no index and no id, and no call is open to continue")
	}
	if tc.ID != "" {
		b.id = tc.ID
	}
	if tc.Function.Name != "" {
		b.name = tc.Function.Name
	}
	b.args.WriteString(tc.Function.Arguments)
	return nil
}

// emit yields one StreamChunk_ToolCall per accumulated call, in
// first-seen order. Returns false if the consumer stopped iterating.
func (a *toolCallAccumulator) emit(yield func(*llmv1.StreamChunk, error) bool) bool {
	for _, b := range a.order {
		args := b.args.String()
		if args == "" {
			args = "{}"
		}
		if !yield(&llmv1.StreamChunk{
			Delta: &llmv1.StreamChunk_ToolCall{ToolCall: &threadv1.ToolCallContent{
				Id: b.id, Name: b.name, Arguments: args,
			}},
		}, nil) {
			return false
		}
	}
	return true
}

// normalizeFinishReason maps OpenAI's finish_reason vocabulary to the
// grudge-normalized set (llm.proto's CompletionResponse.finish_reason
// doc: "stop", "length", "tool_calls", "content_filter"). OpenAI's
// names already match except the legacy "function_call" alias;
// anything unrecognized passes through raw rather than being silently
// discarded.
func normalizeFinishReason(reason string) string {
	switch reason {
	case "function_call":
		return "tool_calls"
	default:
		return reason
	}
}

// --- Embedder ---

type embedder struct {
	model   string
	baseURL string
	client  *httpc.Client
}

type embedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"`
}

type embedResponse struct {
	Data []embedData `json:"data"`
}

type embedData struct {
	Embedding []float32 `json:"embedding"`
}

// Embed routes both roles to the same /v1/embeddings endpoint —
// OpenAI's embedding API is symmetric at the request level. The
// model itself may apply different prompts internally based on the
// model variant; nothing to encode at this adapter layer.
func (e *embedder) Embed(ctx context.Context, _ core.EmbedRole, texts []string) ([][]float32, error) {
	return e.embed(ctx, texts)
}

func (e *embedder) embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := e.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: openai returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp embedResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("%w: openai returned %d embeddings for %d inputs", core.ErrProviderUnavailable, len(resp.Data), len(texts))
	}

	results := make([][]float32, len(resp.Data))
	for i, d := range resp.Data {
		results[i] = d.Embedding
	}
	return results, nil
}

// --- helpers ---

func toChatRequest(model string, req *llmv1.CompletionRequest, stream bool) (chatRequest, error) {
	canon := chatwire.Canonicalize(req.Messages)
	msgs := make([]chatMessage, len(canon))
	for i, m := range canon {
		msgs[i] = fromCanonicalMessage(m)
	}
	toolChoice, err := toChatToolChoice(req.ToolChoice)
	if err != nil {
		return chatRequest{}, err
	}
	out := chatRequest{
		Model:       model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Stream:      stream,
		Tools:       toChatTools(req.Tools),
		ToolChoice:  toolChoice,
	}
	if stream {
		out.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	return out, nil
}

// fromCanonicalMessage converts one chatwire.Message to OpenAI's wire
// shape. OpenAI never receives thinking — chatwire.Message.Thinking is
// discarded, matching what this codec actually sends. A tool_call-only
// assistant or a role="tool" carrier sends Content as a plain string
// (possibly empty), which OpenAI accepts.
func fromCanonicalMessage(m chatwire.Message) chatMessage {
	cm := chatMessage{
		Role:       m.Role,
		Content:    m.Text,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		cm.ToolCalls = make([]toolCall, len(m.ToolCalls))
		for i, c := range m.ToolCalls {
			cm.ToolCalls[i] = toolCall{ID: c.ID, Type: "function"}
			cm.ToolCalls[i].Function.Name = c.Name
			cm.ToolCalls[i].Function.Arguments = c.Arguments
		}
	}
	return cm
}

func toChatTools(tools []*llmv1.ToolDeclaration) []chatTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]chatTool, len(tools))
	for i, t := range tools {
		params := json.RawMessage(t.ParametersJson)
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		out[i] = chatTool{
			Type: "function",
			Function: chatToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		}
	}
	return out
}

// toChatToolChoice maps the proto ToolChoice to OpenAI's wire shape.
// Unset or UNSPECIFIED omits the field — the model decides, OpenAI's
// own default. NAMED with an empty tool name can't be encoded and is
// refused rather than silently sent as AUTO.
func toChatToolChoice(tc *llmv1.ToolChoice) (any, error) {
	if tc == nil || tc.Mode == llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_UNSPECIFIED {
		return nil, nil
	}
	switch tc.Mode {
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_AUTO:
		return "auto", nil
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NONE:
		return "none", nil
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED:
		return "required", nil
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NAMED:
		if tc.NamedTool == "" {
			return nil, fmt.Errorf("openai: tool_choice NAMED requires a non-empty named_tool")
		}
		return namedToolChoice{Type: "function", Function: namedToolChoiceFunc{Name: tc.NamedTool}}, nil
	default:
		return nil, fmt.Errorf("openai: unknown tool_choice mode %v", tc.Mode)
	}
}

func fromChatMessage(m chatMessage) []*threadv1.ContentBlock {
	var blocks []*threadv1.ContentBlock
	if s, ok := m.Content.(string); ok && s != "" {
		blocks = append(blocks, &threadv1.ContentBlock{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: s}}})
	}
	for _, tc := range m.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		blocks = append(blocks, &threadv1.ContentBlock{Block: &threadv1.ContentBlock_ToolCall{ToolCall: &threadv1.ToolCallContent{
			Id:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		}}})
	}
	return blocks
}
