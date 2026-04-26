package ollama

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/core/internal/httpc"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// dumpBudget caps how many distinct 5xx request bodies we dump per
// process, to keep disk use bounded while still capturing a handful
// of cases for investigation. Same payload retried by the resilience
// loop only counts once (deduped on the first 32 bytes, which is
// enough to separate structurally-different requests).
const dumpBudgetMax = 10

var (
	dumpMu       sync.Mutex
	dumpCount    int
	dumpedHashes = make(map[string]bool)
)

// dumpFailingRequestOnce writes the given request body to a
// timestamped file. Dedupes on a prefix of the body so the same
// payload retried 10 times via exponential backoff produces one
// file; structurally-different failures each get their own.
func dumpFailingRequestOnce(body []byte) {
	dumpMu.Lock()
	defer dumpMu.Unlock()
	if dumpCount >= dumpBudgetMax {
		return
	}
	// Dedup on full-content hash. The system-prompt prefix is
	// constant across requests, so prefix-keying collapsed
	// structurally-different failures to one entry.
	h := sha1.Sum(body)
	key := hex.EncodeToString(h[:])
	if dumpedHashes[key] {
		return
	}
	dumpedHashes[key] = true
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("[Ollama] dump: UserHomeDir: %v", err)
		return
	}
	dir := filepath.Join(home, ".local", "share", "spidey", "failing-requests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[Ollama] dump: MkdirAll: %v", err)
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("ollama-5xx-%s-%d.json", time.Now().Format("20060102-150405"), dumpCount))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		log.Printf("[Ollama] dump: WriteFile: %v", err)
		return
	}
	dumpCount++
	log.Printf("[Ollama] dump: failing request body written to %s (%d bytes, %d/%d budget)", path, len(body), dumpCount, dumpBudgetMax)
}

// Config for the Ollama provider. No auth — Ollama runs locally.
type Config struct {
	BaseURL string // e.g., "http://localhost:11434"
}

type provider struct {
	cfg    Config
	client *httpc.Client
}

// New creates an Ollama provider.
func New(cfg Config) core.Provider {
	return &provider{
		cfg:    cfg,
		client: httpc.New(httpc.TimeoutDefault, nil),
	}
}


func (p *provider) Completer(model string) (core.Completer, error) {
	return &completer{
		model:         model,
		baseURL:       p.cfg.BaseURL,
		client:        p.client,
		streamClient:  httpc.NewStreaming(nil),
	}, nil
}

func (p *provider) Embedder(model string) (core.Embedder, error) {
	return &embedder{
		model:   model,
		baseURL: p.cfg.BaseURL,
		client:  p.client,
	}, nil
}

func (p *provider) Classifier(_ string) (core.Classifier, error) {
	return nil, core.ErrUnsupported
}


// --- Completer ---

type completer struct {
	model        string
	baseURL      string
	client       *httpc.Client
	streamClient *httpc.Client
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Options  any           `json:"options,omitempty"`
	Tools    []ollamaTool  `json:"tools,omitempty"`
}

type ollamaTool struct {
	Type     string             `json:"type"`
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Thinking  string           `json:"thinking,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	// Tool-response correlation (OpenAI-compatible). Required by Ollama
	// when Role=="tool" so the model can match the response to the
	// original tool call. Without this, the model sees the tool's
	// output but can't correlate it to the call it made and behaves
	// as if the tool returned null/empty.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type ollamaToolCall struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Message      chatMessage `json:"message"`
	Model        string      `json:"model"`
	Done         bool        `json:"done"`
	PromptEval   int         `json:"prompt_eval_count"`
	EvalCount    int         `json:"eval_count"`
}

func (c *completer) Complete(ctx context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error) {
	cr := chatRequest{
		Model:    c.model,
		Messages: toLlamaMsgs(req.Messages),
		Stream:   false,
		Options:  providerOpts(req),
		Tools:    toOllamaTools(req.Tools),
	}
	body, err := json.Marshal(cr)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := c.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		log.Printf("[Ollama] Complete error %d: %d msgs, %d tools, body %d bytes, response: %s", status, len(cr.Messages), len(cr.Tools), len(body), string(respBody))
		return nil, fmt.Errorf("%w: ollama returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp chatResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}

	msg := &pb.LLMMessage{Role: pb.Role_ROLE_ASSISTANT}
	if resp.Message.Thinking != "" {
		msg.Content = append(msg.Content, &pb.ContentBlock{
			Block: &pb.ContentBlock_Thinking{Thinking: &pb.ThinkingContent{Text: resp.Message.Thinking}},
		})
	}
	if resp.Message.Content != "" {
		msg.Content = append(msg.Content, &pb.ContentBlock{
			Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: resp.Message.Content}},
		})
	}
	for _, tc := range resp.Message.ToolCalls {
		argsStr := "{}"
		if len(tc.Function.Arguments) > 0 {
			argsStr = string(tc.Function.Arguments)
		}
		msg.Content = append(msg.Content, &pb.ContentBlock{
			Block: &pb.ContentBlock_ToolCall{ToolCall: &pb.ToolCallContent{
				Id:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: argsStr,
			}},
		})
	}
	return &pb.CompletionResponse{
		Message: msg,
		Usage: &pb.Usage{
			PromptTokens:     int32(resp.PromptEval),
			CompletionTokens: int32(resp.EvalCount),
		},
		Model: resp.Model,
	}, nil
}

func (c *completer) Stream(ctx context.Context, req *pb.CompletionRequest) iter.Seq2[*pb.StreamChunk, error] {
	return func(yield func(*pb.StreamChunk, error) bool) {
		cr := chatRequest{
			Model:    c.model,
			Messages: toLlamaMsgs(req.Messages),
			Stream:   true,
			Options:  providerOpts(req),
			Tools:    toOllamaTools(req.Tools),
		}
		body, err := json.Marshal(cr)
		if err != nil {
			yield(nil, err)
			return
		}

		log.Printf("[Ollama] Stream request: %d msgs, %d tools, %d bytes", len(cr.Messages), len(cr.Tools), len(body))

		httpReq, err := http.NewRequest("POST", c.baseURL+"/api/chat", bytes.NewReader(body))
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
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			resp.Body.Close()
			log.Printf("[Ollama] Stream error %d: %d msgs, %d tools, body %d bytes, response: %s",
				resp.StatusCode, len(cr.Messages), len(cr.Tools), len(body), strings.TrimSpace(string(b)))
			if resp.StatusCode >= 500 {
				dumpFailingRequestOnce(body)
			}
			yield(nil, &httpc.StatusError{Provider: "ollama", StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(b))})
			return
		}
		defer resp.Body.Close()

		dec := json.NewDecoder(resp.Body)
		for {
			var chunk chatResponse
			if err := dec.Decode(&chunk); err != nil {
				if err != io.EOF {
					yield(&pb.StreamChunk{Done: true, Error: ptr(err.Error())}, nil)
				}
				return
			}
			if chunk.Message.Thinking != "" {
				if !yield(&pb.StreamChunk{
					Delta: &pb.StreamChunk_Thinking{Thinking: &pb.ThinkingContent{Text: chunk.Message.Thinking}},
				}, nil) {
					return
				}
			}
			if chunk.Message.Content != "" {
				if !yield(&pb.StreamChunk{
					Delta: &pb.StreamChunk_Text{Text: &pb.TextContent{Text: chunk.Message.Content}},
				}, nil) {
					return
				}
			}
			for _, tc := range chunk.Message.ToolCalls {
				argsStr := "{}"
				if len(tc.Function.Arguments) > 0 {
					argsStr = string(tc.Function.Arguments)
				}
				if !yield(&pb.StreamChunk{
					Delta: &pb.StreamChunk_ToolCall{ToolCall: &pb.ToolCallContent{
						Id:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: argsStr,
					}},
				}, nil) {
					return
				}
			}
			if chunk.Done {
				yield(&pb.StreamChunk{
					Done: true,
					Usage: &pb.Usage{
						PromptTokens:     int32(chunk.PromptEval),
						CompletionTokens: int32(chunk.EvalCount),
					},
				}, nil)
				return
			}
		}
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
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

func (e *embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: e.model, Input: text})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", e.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	respBody, status, err := e.client.DoJSON(ctx, httpReq)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: ollama returned %d: %s", core.ErrProviderUnavailable, status, respBody)
	}

	var resp embedResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, err
	}
	if len(resp.Embeddings) == 0 {
		return nil, fmt.Errorf("%w: ollama returned empty embeddings", core.ErrProviderUnavailable)
	}
	return resp.Embeddings[0], nil
}

func (e *embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := e.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		results[i] = vec
	}
	return results, nil
}

// --- helpers ---

// toLlamaMsgs converts a flat list of LLMMessages (derived from the
// per-block pb.Message storage schema) into a wire-valid sequence for
// OpenAI/ollama's chat protocol.
//
// The fundamental impedance mismatch: our storage is one-pb.Message-per-
// content-block (a single tool call is its own row; a single tool result
// is its own row), which preserves event ordering losslessly for the
// corpus but produces malformed OpenAI-protocol sequences when fed
// verbatim to the provider. The OpenAI protocol requires:
//
//   1. An `assistant` message carrying tool_calls is followed by `tool`
//      messages whose tool_call_id matches each call's id, all before
//      any subsequent non-tool message.
//   2. `tool` messages MUST have a preceding `assistant.tool_calls[]`
//      entry with matching id. Orphan tool messages are rejected.
//   3. Empty `assistant` messages (no content, no thinking, no
//      tool_calls) are invalid.
//
// Minimax (via ollama.com) enforces these with 503 on violation — we
// empirically confirmed a 503 at 100% reproduction against a dumped
// failing request, and 200 at 100% against the same body with the tool
// protocol canonicalized.
//
// This canonicalizer walks the input once and emits a valid sequence:
//
//   - Consecutive assistant messages whose only content is tool_calls
//     are merged into a single `assistant{tool_calls: [...]}` — ADK
//     splits parallel tool calls across separate events, our storage
//     captures each as its own pb.Message, the wire needs them grouped.
//   - After each emitted assistant-with-tool_calls, we emit exactly the
//     matching tool responses in order of appearance in the input.
//     Tool responses with no matching call in the current emitted set
//     are dropped (orphan — their call was likely outside the Selected
//     window or the Radius boundary).
//   - Text-only assistant messages, user messages, and system messages
//     pass through unchanged.
//   - Pure-empty assistant messages (no text, no thinking, no calls)
//     are dropped.
func toLlamaMsgs(msgs []*pb.LLMMessage) []chatMessage {
	// Phase 1: extract a flat sequence of "atoms" preserving order:
	//   - text/thinking content on its role-bearing message
	//   - each tool_call with its id + name + arguments
	//   - each tool_result with its tool_call_id + content
	type atom struct {
		kind       string // "text", "toolcall", "toolresult"
		role       string // only for text atoms
		text       string
		thinking   string
		toolCall   ollamaToolCall
		toolResult *pb.ToolResultContent
	}
	var atoms []atom
	for _, m := range msgs {
		role := roleStr(m.Role)
		var textParts []string
		var thinkParts []string
		var msgCalls []ollamaToolCall
		var msgResults []*pb.ToolResultContent
		for _, b := range m.Content {
			if t := b.GetText(); t != nil {
				textParts = append(textParts, t.Text)
			}
			if t := b.GetThinking(); t != nil {
				thinkParts = append(thinkParts, t.Text)
			}
			if tc := b.GetToolCall(); tc != nil {
				msgCalls = append(msgCalls, ollamaToolCall{
					ID:   tc.Id,
					Type: "function",
					Function: struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					}{
						Name:      tc.Name,
						Arguments: json.RawMessage(tc.Arguments),
					},
				})
			}
			if tr := b.GetToolResult(); tr != nil {
				msgResults = append(msgResults, tr)
			}
		}
		// Emit atoms for this source message. Text/thinking first, then
		// tool_calls, then tool_results — matches the intrinsic order
		// inside a multi-block assistant message (thinking precedes
		// tool invocation precedes response in the execution timeline).
		if len(textParts) > 0 || len(thinkParts) > 0 {
			atoms = append(atoms, atom{
				kind:     "text",
				role:     role,
				text:     strings.Join(textParts, "\n"),
				thinking: strings.Join(thinkParts, "\n"),
			})
		}
		for _, tc := range msgCalls {
			atoms = append(atoms, atom{kind: "toolcall", toolCall: tc})
		}
		for _, tr := range msgResults {
			atoms = append(atoms, atom{kind: "toolresult", toolResult: tr})
		}
	}

	// Phase 2a: build the set of tool_call IDs that have a matching
	// tool_result atom somewhere in the input. Tool calls without a
	// matching response are orphans — the model emitted the call in
	// a prior turn but the response either wasn't picked by Selection
	// or was dropped by shed. Sending orphan tool_calls to the
	// provider trips protocol validation (assistant.tool_calls must
	// be followed by tool messages matching every id). Same direction
	// as orphan tool_results: drop the side that can't be satisfied.
	resultIDs := make(map[string]bool)
	for _, a := range atoms {
		if a.kind == "toolresult" && a.toolResult != nil {
			resultIDs[a.toolResult.ToolCallId] = true
		}
	}

	// Phase 2b: walk atoms and emit canonical chatMessages.
	//
	// Rules enforced here:
	//   - Only tool_calls whose id is in resultIDs are kept — the
	//     rest are orphans, dropped.
	//   - Consecutive kept tool_calls are merged into one assistant
	//     with a tool_calls array.
	//   - Matching tool_results are emitted immediately after their
	//     paired assistant-with-tool_calls, in input order, then
	//     marked consumed.
	//   - A thinking-only atom (no text, just reasoning) does NOT
	//     emit its own chatMessage — standalone
	//     role=assistant/content=""/thinking-only messages violate
	//     the OpenAI tool-use protocol (assistant must have content
	//     or tool_calls). Instead the thinking is buffered and
	//     merged into the NEXT emitted assistant message that has
	//     text or tool_calls, preserving the reason-before-action
	//     chronology. Trailing thinking with no subsequent action
	//     is dropped.
	//   - Text atoms with non-empty text emit as regular messages,
	//     absorbing any buffered thinking.
	//   - Orphan tool_results (no matching call in the kept output)
	//     drop.
	out := make([]chatMessage, 0, len(atoms))
	consumed := make([]bool, len(atoms))
	var pendingThinking []string // accumulated from thinking-only atoms
	flushThinking := func() string {
		if len(pendingThinking) == 0 {
			return ""
		}
		t := strings.Join(pendingThinking, "\n")
		pendingThinking = pendingThinking[:0]
		return t
	}
	for i := 0; i < len(atoms); i++ {
		if consumed[i] {
			continue
		}
		a := atoms[i]
		switch a.kind {
		case "text":
			if a.text == "" && a.thinking == "" {
				// No content at all — drop silently.
				consumed[i] = true
				continue
			}
			if a.text == "" && a.thinking != "" {
				// Thinking only — buffer for merge into next actionable
				// message. Do not emit on its own.
				pendingThinking = append(pendingThinking, a.thinking)
				consumed[i] = true
				continue
			}
			// Text (with or without thinking) — emit, absorbing any
			// prior buffered thinking as the leading reasoning.
			thinking := a.thinking
			if buf := flushThinking(); buf != "" {
				if thinking != "" {
					thinking = buf + "\n" + thinking
				} else {
					thinking = buf
				}
			}
			out = append(out, chatMessage{
				Role:     a.role,
				Content:  a.text,
				Thinking: thinking,
			})
			consumed[i] = true
		case "toolcall":
			// Greedy-collect consecutive toolcalls, KEEPING ONLY
			// those with a matching tool_result somewhere in the
			// input. If no call in the group has a match, drop the
			// whole group — we won't emit an assistant with all
			// orphan tool_calls.
			var group []ollamaToolCall
			j := i
			for j < len(atoms) && atoms[j].kind == "toolcall" && !consumed[j] {
				consumed[j] = true
				if resultIDs[atoms[j].toolCall.ID] {
					group = append(group, atoms[j].toolCall)
				}
				j++
			}
			if len(group) == 0 {
				// Orphan toolcall group — pending thinking loses its
				// intended action and is dropped too.
				flushThinking()
				continue
			}
			callIDs := make(map[string]bool, len(group))
			for _, c := range group {
				callIDs[c.ID] = true
			}
			out = append(out, chatMessage{
				Role:      "assistant",
				Thinking:  flushThinking(),
				ToolCalls: group,
			})
			// Emit matching tool responses from the remaining atoms.
			for k := j; k < len(atoms); k++ {
				if consumed[k] {
					continue
				}
				if atoms[k].kind != "toolresult" {
					continue
				}
				tr := atoms[k].toolResult
				if !callIDs[tr.ToolCallId] {
					continue
				}
				out = append(out, chatMessage{
					Role:       "tool",
					ToolCallID: tr.ToolCallId,
					Content:    tr.Content,
				})
				consumed[k] = true
			}
		case "toolresult":
			// Orphan tool_result — no preceding tool_call remains to
			// bind to. Drop. Any buffered thinking here is stranded
			// too; it can't attach to a toolresult meaningfully, so
			// drop it as well on the next flush.
			consumed[i] = true
		}
	}
	// Trailing thinking with no subsequent action — drop, since
	// emitting a thinking-only assistant is exactly the protocol
	// violation this function exists to prevent.
	flushThinking()
	return out
}

func roleStr(r pb.Role) string {
	switch r {
	case pb.Role_ROLE_USER:
		return "user"
	case pb.Role_ROLE_ASSISTANT:
		return "assistant"
	case pb.Role_ROLE_SYSTEM:
		return "system"
	default:
		return "user"
	}
}

func providerOpts(req *pb.CompletionRequest) map[string]any {
	opts := make(map[string]any)
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		opts["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		opts["stop"] = req.Stop
	}
	if len(opts) == 0 {
		return nil
	}
	return opts
}

func toOllamaTools(tools []*pb.ToolDeclaration) []ollamaTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]ollamaTool, len(tools))
	for i, t := range tools {
		params := json.RawMessage(t.ParametersJson)
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		out[i] = ollamaTool{
			Type: "function",
			Function: ollamaToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		}
	}
	return out
}

func ptr(s string) *string { return &s }
