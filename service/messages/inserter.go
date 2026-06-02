// Package messages owns the single message-insertion pathway. Both
// the graph layer (resolver-side SendMessage / EditMessage / agent
// denial paths) and the runtime layer (Runner.InsertMessage during
// agent rounds) route through Inserter so chunk derivation and the
// post-insert embed enqueue happen in exactly one place.
//
// Pre-Part-B both sides duplicated a chunksFor helper that converted
// (*pb.Message, chunk.Config) to []storage.Chunk via
// rrc.TextFromBlocks + chunk.Split. The duplication grew naturally
// because each layer had its own entry into the insert path. The
// helper is now a private function in this package; both layers call
// Inserter.Insert.
package messages

import (
	pb "github.com/emontenegr/grudge/proto/gen/go/grudge/v1"
	"github.com/emontenegr/grudge/rrc"
	"github.com/emontenegr/grudge/rrc/chunk"
	"github.com/emontenegr/grudge/service/storage"
)

// Inserter persists a message and its chunks, then enqueues an embed
// for the message id. ChunkConfig is fetched per-call via the
// supplied accessor so the inserter sees current engine config
// across atomic substrate swaps without holding a captured reference.
//
// EmbedEnqueue is best-effort: a nil enqueuer (or a no-op closure)
// is permitted for tests and for boot-time inserts that should fall
// back to the startup backfill goroutine.
type Inserter struct {
	db          *storage.DB
	chunkConfig func() chunk.Config
	embedEnq    func(messageID string)
}

// New constructs an Inserter. The chunkConfig closure is invoked on
// every Insert to pick up live engine config (the substrate swaps
// the underlying engine atomically). embedEnq may be nil.
func New(db *storage.DB, chunkConfig func() chunk.Config, embedEnq func(messageID string)) *Inserter {
	return &Inserter{db: db, chunkConfig: chunkConfig, embedEnq: embedEnq}
}

// Insert persists msg with its derived chunks, then enqueues an
// embed for the message id. Chunk derivation is silent on empty /
// non-text messages — they store with zero chunks, which is the
// correct shape for tool-only or empty-content turns. The embed
// enqueue is skipped when chunks are empty since there's nothing
// for the embedder to embed.
func (i *Inserter) Insert(msg *pb.Message) error {
	chunks := chunksFor(msg, i.chunkConfig())
	if err := i.db.InsertMessage(msg, chunks); err != nil {
		return err
	}
	if i.embedEnq != nil && len(chunks) > 0 {
		i.embedEnq(msg.Id)
	}
	return nil
}

// chunksFor splits a message's text into storage-shaped chunk rows.
// Returns nil when the message has no text content (system messages,
// empty-content turns) — InsertMessage tolerates a nil chunks slice.
func chunksFor(msg *pb.Message, cfg chunk.Config) []storage.Chunk {
	text := rrc.TextFromBlocks(msg.Content)
	if text == "" {
		return nil
	}
	rcs := chunk.Split(text, cfg)
	if len(rcs) == 0 {
		return nil
	}
	out := make([]storage.Chunk, len(rcs))
	for idx, c := range rcs {
		out[idx] = storage.Chunk{
			MessageID:  msg.Id,
			ChunkIndex: c.Index,
			Text:       c.Text,
			ByteStart:  c.ByteStart,
			ByteEnd:    c.ByteEnd,
			TokenEst:   c.TokenEst,
		}
	}
	return out
}
