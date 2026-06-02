package core

import (
	"context"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

// Codec is the bidirectional mapping between one LLM wire format and
// grudge's proto truth types. Every adapter that participates in the
// proxy path implements Codec. Adding a new protocol = new
// implementation, registered via core.RegisterCodec.
//
// Codec methods are all []byte → *pb.* or *pb.* → []byte. The proxy
// reads an incoming HTTP body, calls DecodeRequest to canonicalize
// it, runs RRC, calls EncodeResponse / EncodeChunk to send back in
// the same wire format. The inverse pair (EncodeRequest /
// DecodeResponse / DecodeChunk) supports the outbound direction
// where grudge itself talks to an upstream model server.
//
// Implementations should be stateless after construction.
type Codec interface {
	// Name returns the codec's registry key. Convention: lowercase
	// short name like "openai", "anthropic", "ollama". Used by
	// LookupCodec, log lines, and proxy route matching.
	Name() string

	// Inbound: client request → canonical proto.
	DecodeRequest(body []byte) (*pb.CompletionRequest, error)

	// Outbound: canonical proto → wire format.
	EncodeRequest(req *pb.CompletionRequest) ([]byte, error)

	// Outbound: canonical response → client wire.
	EncodeResponse(resp *pb.CompletionResponse) ([]byte, error)

	// Inbound: upstream response → canonical proto.
	DecodeResponse(body []byte) (*pb.CompletionResponse, error)

	// Streaming: outbound — encode one chunk for the SSE/JSONL
	// the client expects.
	EncodeChunk(chunk *pb.StreamChunk) ([]byte, error)

	// Streaming: inbound — parse one server-sent line into a chunk.
	// Implementations return (nil, nil) for keep-alive lines or
	// other non-payload events the caller should skip.
	DecodeChunk(line []byte) (*pb.StreamChunk, error)
}

// codecRegistry is the package-level map from Name() to Codec.
// Adapters register themselves via init() so importing
// core/adapter/X/ enables that codec without explicit wiring.
var codecRegistry = map[string]Codec{}

// RegisterCodec installs a codec under c.Name(). Last write wins —
// callers can override a default registration by registering a
// different codec under the same name. A nil codec or empty name is
// a no-op (defensive against init() ordering bugs).
func RegisterCodec(c Codec) {
	if c == nil {
		return
	}
	if name := c.Name(); name != "" {
		codecRegistry[name] = c
	}
}

// LookupCodec returns the codec registered under name. Used by the
// proxy router to dispatch by URL prefix or content-type.
func LookupCodec(name string) (Codec, bool) {
	c, ok := codecRegistry[name]
	return c, ok
}

// CodecNames returns all registered codec names. Order is
// unspecified.
func CodecNames() []string {
	out := make([]string, 0, len(codecRegistry))
	for name := range codecRegistry {
		out = append(out, name)
	}
	return out
}

// EncodeChunkContext / DecodeChunkContext are reserved for future
// streaming codecs that need request-scoped state (auth headers
// shared across chunks, etc). Today's codecs are stateless per
// chunk; the *Context variants would extend the interface.
//
// Defining now to lock the convention; consumers can adopt them
// later without breaking the existing Codec contract.
type _ = context.Context
