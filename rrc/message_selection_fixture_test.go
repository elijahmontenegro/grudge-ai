package rrc

import (
	"context"
	"strings"

	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
)

// Graph-gate fixtures express their current context as one message.
// selectViaFixture is the test-only adapter over SelectPrerequisites —
// production consumers always build a real SerializedLocalContext. Candidates
// come from the oracle (register them via addMsg before calling), not a corpus
// argument.
func (e *Engine) selectViaFixture(ctx context.Context, message *threadv1.Message) ([]*rrcv1.Edge, PrerequisiteSelectionTelemetry, error) {
	serialized := &SerializedLocalContext{
		EventID:     "sel-" + message.Id,
		Fingerprint: "fixture-" + message.Id,
		MessageIDs:  []string{message.Id},
		Chunks:      []SerializedLocalContextChunk{{Index: 0, Text: strings.TrimSpace(pbtext.TextFromBlocks(message.Content))}},
	}
	return e.SelectPrerequisites(ctx, serialized, message, threadv1.SelectionScope_SELECTION_SCOPE_ALL_THREADS, message.ThreadId)
}
