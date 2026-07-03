package rrc

import (
	"context"
	"strings"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

// Existing graph-gate fixtures express their current context as one message. Keep
// that fixture adapter test-only while production uses SerializedLocalContext.
type OnMessageTelemetry = PrerequisiteSelectionTelemetry

func (e *Engine) OnMessage(ctx context.Context, message *pb.Message, corpus []*pb.Message) ([]*pb.Edge, OnMessageTelemetry, error) {
	serialized := &SerializedLocalContext{
		EventID:     "sel-" + message.Id,
		Fingerprint: "fixture-" + message.Id,
		MessageIDs:  []string{message.Id},
		Chunks:      []SerializedLocalContextChunk{{Index: 0, Text: strings.TrimSpace(TextFromBlocks(message.Content))}},
	}
	return e.SelectPrerequisites(ctx, serialized, message, corpus, pb.SelectionScope_SELECTION_SCOPE_ALL_THREADS, message.ThreadId)
}
