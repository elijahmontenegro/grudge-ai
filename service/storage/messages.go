package storage

import (
	"database/sql"
	"strings"
	"time"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// scanMessages reads full messages from rows in the canonical column order
// (id, thread_id, role, content, position, created_at, turn_id).
func scanMessages(rows *sql.Rows) ([]*threadv1.Message, error) {
	var out []*threadv1.Message
	for rows.Next() {
		msg := &threadv1.Message{}
		var roleInt int
		var content []byte
		var createdAt time.Time
		if err := rows.Scan(&msg.Id, &msg.ThreadId, &roleInt, &content, &msg.Position, &createdAt, &msg.TurnId); err != nil {
			return nil, err
		}
		msg.Role = threadv1.Role(roleInt)
		msg.CreatedAt = timestamppb.New(createdAt)
		var err error
		if msg.Content, err = unmarshalContentBlocks(content); err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

// Messages returns the given messages by id — the bounded content fetch
// Assemble uses for its selected set, never the full corpus. Satisfies
// rrc.CorpusStore.
func (d *DB) Messages(ids []string) (map[string]*threadv1.Message, error) {
	if len(ids) == 0 {
		return map[string]*threadv1.Message{}, nil
	}
	ph := strings.Repeat("?,", len(ids))
	ph = ph[:len(ph)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := d.Query(`SELECT id, thread_id, role, content, position, created_at, turn_id
	                      FROM messages WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*threadv1.Message, len(msgs))
	for _, m := range msgs {
		out[m.Id] = m
	}
	return out, nil
}

// TurnPeers returns every message sharing a (thread_id, turn_id) with any input
// message — the bounded set where tool-call/result counterparts live (a call
// and its result share a turn), via the idx_messages_turn index. Empty turn ids
// (legacy rows with no turn identity) are skipped. Satisfies rrc.CorpusStore.
func (d *DB) TurnPeers(msgs []*threadv1.Message) ([]*threadv1.Message, error) {
	seen := make(map[string]bool)
	var clauses []string
	var args []any
	for _, m := range msgs {
		if m.TurnId == "" {
			continue
		}
		key := m.ThreadId + "\x00" + m.TurnId
		if seen[key] {
			continue
		}
		seen[key] = true
		clauses = append(clauses, "(thread_id = ? AND turn_id = ?)")
		args = append(args, m.ThreadId, m.TurnId)
	}
	if len(clauses) == 0 {
		return nil, nil
	}
	rows, err := d.Query(`SELECT id, thread_id, role, content, position, created_at, turn_id
	                      FROM messages WHERE `+strings.Join(clauses, " OR "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// MessageThreads returns every message's thread id — the boot-time source
// for the ANN retrieval path's RAM-resident thread map, so thread-scope
// predicates never hit the database per search. O(messages), read once at
// startup; live inserts keep the map current through the embedding observer.
func (d *DB) MessageThreads() (map[string]string, error) {
	rows, err := d.Query(`SELECT id, thread_id FROM messages`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var id, threadID string
		if err := rows.Scan(&id, &threadID); err != nil {
			return nil, err
		}
		out[id] = threadID
	}
	return out, rows.Err()
}

// InsertMessage stores a message with proto-encoded content and its
// caller-supplied chunks in one transaction. Chunks are the scoring
// substrate for RRC — a message without chunks is invisible to
// retrieval, so most callers pre-split via chunk.Split before
// calling. Chunkless messages (empty text, system messages) pass nil.
// If the chunk-insert fails, the message row rolls back too.
//
// CreatedAt policy: the storage boundary owns row-write time. If the
// caller leaves CreatedAt nil or zero, we fill `now()` here. Callers
// who set a deliberate timestamp (branch construction, future imports)
// keep their value. This centralizes the policy so every caller —
// agent runner, graph resolvers, anything future — gets a correct
// timestamp without having to remember a field. Without this, the
// runner's five message-construction sites all stored epoch-zero
// rows because they didn't set the field.
func (d *DB) InsertMessage(msg *threadv1.Message, chunks []Chunk) error {
	fillCreatedAt(msg)
	content, err := marshalContentBlocks(msg.Content)
	if err != nil {
		return err
	}

	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := insertMessageTx(tx, msg, content, chunks); err != nil {
		return err
	}
	return tx.Commit()
}

// InsertToolCallPair stores a tool_call message and its tool_result
// message — each with its chunks — in ONE transaction. This is the
// write-side guarantee behind rrc/protocol.go CloseGroup: a tool_call
// must never reach the corpus without its result. The per-message
// InsertMessage path commits each row in its own transaction, leaving a
// crash/cancel/insert-error window where a lone call would durably brick
// the thread (CloseGroup fatals on it on every later assembly). Here
// both rows commit or neither does — the invariant is held by the
// transaction boundary, not by an after-the-fact backfill convention.
func (d *DB) InsertToolCallPair(call *threadv1.Message, callChunks []Chunk, result *threadv1.Message, resultChunks []Chunk) error {
	fillCreatedAt(call)
	fillCreatedAt(result)
	callContent, err := marshalContentBlocks(call.Content)
	if err != nil {
		return err
	}
	resultContent, err := marshalContentBlocks(result.Content)
	if err != nil {
		return err
	}

	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := insertMessageTx(tx, call, callContent, callChunks); err != nil {
		return err
	}
	if err := insertMessageTx(tx, result, resultContent, resultChunks); err != nil {
		return err
	}
	return tx.Commit()
}

// insertMessageTx writes one message row and its chunk rows inside tx.
// Shared by InsertMessage (single row) and InsertToolCallPair (atomic
// call+result); the caller owns the transaction, CreatedAt filling, and
// content marshaling.
func insertMessageTx(tx *sql.Tx, msg *threadv1.Message, content []byte, chunks []Chunk) error {
	if _, err := tx.Exec(
		`INSERT INTO messages (id, thread_id, role, content, position, created_at, turn_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.Id, msg.ThreadId, int(msg.Role), content, msg.Position, msg.CreatedAt.AsTime(), msg.TurnId,
	); err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(`INSERT INTO chunks (message_id, chunk_index, text, byte_start, byte_end, token_est) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, c := range chunks {
		if _, err := stmt.Exec(msg.Id, c.ChunkIndex, c.Text, c.ByteStart, c.ByteEnd, c.TokenEst); err != nil {
			return err
		}
	}
	return nil
}

// fillCreatedAt applies the storage-boundary CreatedAt policy: if the
// caller left it nil or zero, stamp now(). Callers with a deliberate
// timestamp (branch construction, future imports) keep their value.
// Centralizing this keeps every insert path — runner, resolvers, the
// atomic pair write — from re-stamping epoch-zero rows.
func fillCreatedAt(msg *threadv1.Message) {
	if msg.CreatedAt == nil || (msg.CreatedAt.Seconds == 0 && msg.CreatedAt.Nanos == 0) {
		msg.CreatedAt = timestamppb.Now()
	}
}

// GetMessage retrieves a single message by ID.
func (d *DB) GetMessage(id string) (*threadv1.Message, error) {
	msg := &threadv1.Message{}
	var roleInt int
	var content []byte
	var createdAt time.Time

	err := d.QueryRow(
		`SELECT id, thread_id, role, content, position, created_at, turn_id FROM messages WHERE id = ?`, id,
	).Scan(&msg.Id, &msg.ThreadId, &roleInt, &content, &msg.Position, &createdAt, &msg.TurnId)
	if err != nil {
		return nil, err
	}

	msg.Role = threadv1.Role(roleInt)
	msg.CreatedAt = timestamppb.New(createdAt)
	msg.Content, err = unmarshalContentBlocks(content)
	return msg, err
}

// RecentMessages returns a thread's most recent n messages in ascending
// position order — a bounded recency window for Local Context construction,
// via idx_messages_thread_pos. Invariant under corpus growth.
func (d *DB) RecentMessages(threadID string, n int) ([]*threadv1.Message, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := d.Query(`SELECT id, thread_id, role, content, position, created_at, turn_id
	                      FROM messages WHERE thread_id = ? ORDER BY position DESC LIMIT ?`, threadID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 { // DESC -> ascending
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// TurnMessages returns a thread's messages for one turn via idx_messages_turn —
// the in-flight turn's discourse, bounded by the turn's size (independent of
// corpus size), so invariant under corpus growth. No ORDER BY: an
// `ORDER BY position` biases the planner toward idx_messages_thread_pos (for
// the ordering) and away from the turn index (for the filter), turning a
// bounded lookup into an O(N) scan. Callers order the merged window themselves.
func (d *DB) TurnMessages(threadID, turnID string) ([]*threadv1.Message, error) {
	if turnID == "" {
		return nil, nil
	}
	rows, err := d.Query(`SELECT id, thread_id, role, content, position, created_at, turn_id
	                      FROM messages WHERE thread_id = ? AND turn_id = ?`, threadID, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// ListMessages returns messages for a thread, ordered by position.
func (d *DB) ListMessages(threadID string, limit, offset int) ([]*threadv1.Message, error) {
	query := `SELECT id, thread_id, role, content, position, created_at, turn_id
	          FROM messages WHERE thread_id = ? ORDER BY position ASC`
	args := []any{threadID}
	if limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}

	rows, err := d.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*threadv1.Message
	for rows.Next() {
		msg := &threadv1.Message{}
		var roleInt int
		var content []byte
		var createdAt time.Time

		if err := rows.Scan(&msg.Id, &msg.ThreadId, &roleInt, &content, &msg.Position, &createdAt, &msg.TurnId); err != nil {
			return nil, err
		}
		msg.Role = threadv1.Role(roleInt)
		msg.CreatedAt = timestamppb.New(createdAt)
		msg.Content, err = unmarshalContentBlocks(content)
		if err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

// ThreadCorpus returns all messages for a thread (full corpus, no pagination).
// For branched threads, includes the parent's messages up to the branch point
// (referenced, not copied — per spec: "messages 1..N-1 are immutable, referenced not copied").
func (d *DB) ThreadCorpus(threadID string) ([]*threadv1.Message, error) {
	// Check if this is a branch
	var parentID *string
	var branchPos *int64
	d.QueryRow(
		`SELECT parent_thread_id, branch_point_position FROM threads WHERE id = ?`, threadID,
	).Scan(&parentID, &branchPos)

	var corpus []*threadv1.Message

	// Include parent prefix if this is a branch
	if parentID != nil && *parentID != "" && branchPos != nil {
		parentMsgs, err := d.ListMessages(*parentID, 0, 0)
		if err != nil {
			return nil, err
		}
		for _, msg := range parentMsgs {
			if msg.Position < *branchPos {
				corpus = append(corpus, msg)
			}
		}
	}

	// Add this thread's own messages
	ownMsgs, err := d.ListMessages(threadID, 0, 0)
	if err != nil {
		return nil, err
	}
	corpus = append(corpus, ownMsgs...)
	return corpus, nil
}

// TurnStartPosition returns the position of the FIRST message of the turn
// that contains the message at (threadID, position). A branch prefix is
// "messages with position < branchPos"; snapping branchPos to a turn
// boundary keeps whole turns intact, so a branch can never include a
// tool_call while excluding its tool_result (which would brick the
// branch's protocol closure). If the target message has no turn id
// (legacy rows) or is not found, position is returned unchanged.
func (d *DB) TurnStartPosition(threadID string, position int64) (int64, error) {
	var turnID string
	err := d.QueryRow(
		`SELECT turn_id FROM messages WHERE thread_id = ? AND position = ?`,
		threadID, position,
	).Scan(&turnID)
	if err == sql.ErrNoRows {
		return position, nil
	}
	if err != nil {
		return 0, err
	}
	if turnID == "" {
		return position, nil
	}
	var minPos int64
	if err := d.QueryRow(
		`SELECT MIN(position) FROM messages WHERE thread_id = ? AND turn_id = ?`,
		threadID, turnID,
	).Scan(&minPos); err != nil {
		return 0, err
	}
	return minPos, nil
}

// AllCorpus returns every stored message across every thread — the
// candidate set for cross-thread RRC selection. Sorted by (thread_id,
// position) so in-thread ordering is preserved. Required when scope ==
// SELECTION_SCOPE_ALL_THREADS; otherwise RRC can only ever score the
// query against messages from its own thread and cross-thread edges
// never form.
func (d *DB) AllCorpus() ([]*threadv1.Message, error) {
	rows, err := d.Query(
		`SELECT id, thread_id, role, content, position, created_at, turn_id
		 FROM messages ORDER BY thread_id, position`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var corpus []*threadv1.Message
	for rows.Next() {
		msg := &threadv1.Message{}
		var roleInt int
		var content []byte
		var createdAt time.Time
		if err := rows.Scan(&msg.Id, &msg.ThreadId, &roleInt, &content, &msg.Position, &createdAt, &msg.TurnId); err != nil {
			return nil, err
		}
		msg.Role = threadv1.Role(roleInt)
		msg.CreatedAt = timestamppb.New(createdAt)
		msg.Content, _ = unmarshalContentBlocks(content)
		corpus = append(corpus, msg)
	}
	return corpus, rows.Err()
}

// LatestMessage returns the most recent message in a thread, or nil if empty.
func (d *DB) LatestMessage(threadID string) *threadv1.Message {
	msg := &threadv1.Message{}
	var roleInt int
	var content []byte
	var createdAt time.Time
	err := d.QueryRow(
		`SELECT id, thread_id, role, content, position, created_at, turn_id
		 FROM messages WHERE thread_id = ? ORDER BY position DESC LIMIT 1`, threadID,
	).Scan(&msg.Id, &msg.ThreadId, &roleInt, &content, &msg.Position, &createdAt, &msg.TurnId)
	if err != nil {
		return nil
	}
	msg.Role = threadv1.Role(roleInt)
	msg.CreatedAt = timestamppb.New(createdAt)
	msg.Content, _ = unmarshalContentBlocks(content)
	return msg
}

// MessageCount returns the number of messages in a thread.
func (d *DB) MessageCount(threadID string) int {
	var count int
	d.QueryRow(`SELECT COUNT(*) FROM messages WHERE thread_id = ?`, threadID).Scan(&count)
	return count
}

// MaxPosition returns the highest message position in a thread, -1 when
// the thread has no messages. The position authority (the runner's
// msgSeq) seeds from this — never from row counts: COUNT(*) drifts from
// the true order high-water mark whenever historical rows carry gaps or
// duplicated positions, and a count-seeded counter then mints colliding
// positions. Single seek on idx_messages_thread_pos.
func (d *DB) MaxPosition(threadID string) int64 {
	pos := int64(-1)
	d.QueryRow(`SELECT COALESCE(MAX(position), -1) FROM messages WHERE thread_id = ?`, threadID).Scan(&pos)
	return pos
}

// marshalContentBlocks encodes repeated ContentBlock as a proto wrapper.
func marshalContentBlocks(blocks []*threadv1.ContentBlock) ([]byte, error) {
	// Use LLMMessage as a wrapper since it has repeated ContentBlock
	wrapper := &llmv1.LLMMessage{Content: blocks}
	return proto.Marshal(wrapper)
}

func unmarshalContentBlocks(data []byte) ([]*threadv1.ContentBlock, error) {
	wrapper := &llmv1.LLMMessage{}
	if err := proto.Unmarshal(data, wrapper); err != nil {
		return nil, err
	}
	return wrapper.Content, nil
}
