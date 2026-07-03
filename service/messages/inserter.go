// Package messages owns the single message-insertion pathway. Both
// the graph layer (resolver-side SendMessage / EditMessage / agent
// denial paths) and the runtime layer (Runner.InsertMessage during
// agent rounds) route through Inserter so chunk derivation and the
// post-insert embed enqueue happen in exactly one place.
//
// Pre-Part-B both sides duplicated a chunksFor helper that converted
// (*threadv1.Message, chunk.Config) to []storage.Chunk via
// rrc.TextFromBlocks + chunk.Split. The duplication grew naturally
// because each layer had its own entry into the insert path. The
// helper is now a private function in this package; both layers call
// Inserter.Insert.
package messages

import (
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
	"github.com/elijahmontenegro/grudge/service/storage"
)

// Inserter persists a message and its chunks, then enqueues an embed
// for the message id. ChunkConfig is fetched per-call via the
// supplied accessor so the inserter sees current engine config
// across atomic substrate swaps without holding a captured reference.
//
// EmbedEnqueue may be nil in isolated tests. A configured runtime
// supplies it before accepting message inserts.
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
// embed for the message id. Every message receives a role-aware,
// block-aware scoring serialization, including tool, image, and
// empty-content messages.
func (i *Inserter) Insert(msg *threadv1.Message) error {
	chunks := chunksFor(msg, i.chunkConfig())
	if err := i.db.InsertMessage(msg, chunks); err != nil {
		return err
	}
	if i.embedEnq != nil && len(chunks) > 0 {
		i.embedEnq(msg.Id)
	}
	return nil
}

// chunksFor splits the message's scoring serialization into
// storage-shaped chunk rows. Raw message content is stored unchanged.
func chunksFor(msg *threadv1.Message, cfg chunk.Config) []storage.Chunk {
	text := rrc.SerializeMessageForScoring(msg)
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
