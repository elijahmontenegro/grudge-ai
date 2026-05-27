package kernel

// EmbedEnqueuer satisfaction. The runner factory wires Kernel.Enqueue
// as the post-insert hook; chunks-per-message go through the bounded
// embed queue so the hot insert path doesn't block on a slow TEI.

// Enqueue routes a message id into the bounded embed queue.
// Tolerates a nil queue (embedder not configured / settings reload
// cleared it) — silent no-op falls back to the startup backfill
// goroutine and the live-embed path in ChunkOracle.EnsureVector,
// so a slow or down embedder doesn't block message inserts.
func (k *Kernel) Enqueue(messageID string) {
	if q := k.EmbedQueue(); q != nil {
		q.Enqueue(messageID)
	}
}
