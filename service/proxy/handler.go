package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
)

// Handler proxies LLM requests through stateless RRC selection.
// Per-protocol wire-format conversion lives in core.Codec
// implementations (one per adapter, registered globally); this
// file is just a router that picks the codec by route, runs RRC
// selection, and forwards via the configured completer.
//
// Adding a new wire protocol is one new core/adapter/X/codec.go +
// one new RegisterRoutes line — no changes to this file beyond
// the route binding.
type Handler struct {
	classifier core.Classifier
	completer  core.Completer
	rrcCfg     rrc.EngineConfig
}

// NewHandler creates a proxy handler.
func NewHandler(classifier core.Classifier, completer core.Completer, cfg rrc.EngineConfig) *Handler {
	return &Handler{
		classifier: classifier,
		completer:  completer,
		rrcCfg:     cfg,
	}
}

// RegisterRoutes binds the OpenAI and Anthropic routes to their
// respective codecs. Add a new line per new protocol; no other
// dispatcher state to update.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", h.handleByCodec("openai"))
	mux.HandleFunc("POST /v1/messages", h.handleByCodec("anthropic"))
}

// handleByCodec produces a handler that decodes via the named
// codec, runs RRC, then forwards the request to the configured
// completer and encodes the response/stream back through the same
// codec. The codec carries the wire-format knowledge; the
// completer carries the upstream call.
func (h *Handler) handleByCodec(name string) http.HandlerFunc {
	codec, ok := core.LookupCodec(name)
	if !ok {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "codec not registered: "+name, http.StatusInternalServerError)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		req, err := codec.DecodeRequest(body)
		if err != nil {
			http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Run stateless RRC selection over the inbound message list.
		// The codec already gave us LLMMessages directly; we lift
		// them into Messages with synthetic ids for OnMessage / Select
		// to operate on, then drop back to LLMMessages for the wire.
		selected, err := h.runStatelessRRC(r.Context(), req.Messages)
		if err != nil {
			writeProxyError(w, err)
			return
		}
		req.Messages = selected

		if req.Stream {
			h.streamThrough(r.Context(), w, codec, req)
			return
		}
		h.completeThrough(r.Context(), w, codec, req)
	}
}

func (h *Handler) completeThrough(ctx context.Context, w http.ResponseWriter, codec core.Codec, req *pb.CompletionRequest) {
	resp, err := h.completeWithBackoff(ctx, req)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	body, err := codec.EncodeResponse(resp)
	if err != nil {
		http.Error(w, "encode response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

func (h *Handler) streamThrough(ctx context.Context, w http.ResponseWriter, codec core.Codec, req *pb.CompletionRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	headersSent := false
	for chunk, err := range h.completer.Stream(ctx, req) {
		if err != nil {
			if !headersSent {
				writeProxyError(w, err)
				return
			}
			fmt.Fprintf(w, "data: {\"error\":%q}\n\n", err.Error())
			return
		}
		headersSent = true
		bytes, encErr := codec.EncodeChunk(chunk)
		if encErr != nil {
			fmt.Fprintf(w, "data: {\"error\":%q}\n\n", encErr.Error())
			return
		}
		w.Write(bytes)
		if flusher != nil {
			flusher.Flush()
		}
		if chunk.Done {
			return
		}
	}
}

// runStatelessRRC creates an ephemeral engine, scores all messages
// against their predecessors, selects for the last message (the
// query), and returns the surviving LLMMessages including the
// query itself.
func (h *Handler) runStatelessRRC(ctx context.Context, llmMsgs []*pb.LLMMessage) ([]*pb.LLMMessage, error) {
	if len(llmMsgs) <= 1 {
		return llmMsgs, nil
	}
	corpus := make([]*pb.Message, len(llmMsgs))
	for i, m := range llmMsgs {
		corpus[i] = &pb.Message{
			Id:       fmt.Sprintf("proxy-%d", i),
			Role:     m.Role,
			Content:  m.Content,
			Position: int64(i),
			ThreadId: "proxy",
		}
	}

	engine := rrc.NewEngine(h.rrcCfg, h.classifier)
	for i := 1; i < len(corpus); i++ {
		if _, err := engine.OnMessage(ctx, corpus[i], corpus[:i]); err != nil {
			return nil, fmt.Errorf("scoring message %d: %w", i, err)
		}
	}
	prompt := corpus[len(corpus)-1]
	result, err := engine.Select(prompt.Id, pb.SelectionScope_SELECTION_SCOPE_THREAD, "proxy")
	if err != nil {
		return nil, fmt.Errorf("selection: %w", err)
	}

	selectedIDs := make(map[string]bool, len(result.Selected))
	for _, s := range result.Selected {
		selectedIDs[s.MessageId] = true
	}
	selectedIDs[prompt.Id] = true

	out := make([]*pb.LLMMessage, 0, len(corpus))
	for i, msg := range corpus {
		if !selectedIDs[msg.Id] {
			continue
		}
		out = append(out, llmMsgs[i])
	}
	if len(out) == 0 {
		out = []*pb.LLMMessage{llmMsgs[len(llmMsgs)-1]}
	}
	return out, nil
}

// completeWithBackoff retries completion with fewer messages on
// context-length errors. Drops the head message each iteration —
// the prompt is at the tail and is preserved.
func (h *Handler) completeWithBackoff(ctx context.Context, req *pb.CompletionRequest) (*pb.CompletionResponse, error) {
	for {
		resp, err := h.completer.Complete(ctx, req)
		if err == nil {
			return resp, nil
		}
		if !core.IsContextOverflow(err) || len(req.Messages) <= 1 {
			return nil, err
		}
		req.Messages = req.Messages[1:]
	}
}

// writeProxyError maps provider errors to appropriate HTTP status
// codes. 502: provider unreachable. 504: timeout. 422: context-
// length after backoff. 429: rate-limited.
func writeProxyError(w http.ResponseWriter, err error) {
	switch {
	case core.IsContextOverflow(err):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, core.ErrRateLimited):
		http.Error(w, err.Error(), http.StatusTooManyRequests)
	case errors.Is(err, core.ErrAuth):
		http.Error(w, err.Error(), http.StatusBadGateway)
	case errors.Is(err, core.ErrProviderUnavailable):
		http.Error(w, err.Error(), http.StatusBadGateway)
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, err.Error(), http.StatusGatewayTimeout)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}
