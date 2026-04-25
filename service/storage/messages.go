package storage

import (
	"time"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// InsertMessage stores a message with proto-encoded content and,
// if a chunker is wired, derives and persists its chunks in the same
// logical operation. Chunks are the scoring substrate for RRC — a
// message without chunks is invisible to the classifier and the
// embedder, so we treat chunk insertion as part of a successful
// InsertMessage: if chunking fails, the whole operation fails and
// the message row is rolled back.
func (d *DB) InsertMessage(msg *pb.Message) error {
	content, err := marshalContentBlocks(msg.Content)
	if err != nil {
		return err
	}

	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO messages (id, thread_id, role, content, position, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		msg.Id, msg.ThreadId, int(msg.Role), content, msg.Position, msg.CreatedAt.AsTime(),
	); err != nil {
		return err
	}

	text := textFromBlocks(msg.Content)
	if text != "" {
		chunks := rrc.ChunkText(text, d.chunkCfg)
		if len(chunks) > 0 {
			stmt, err := tx.Prepare(`INSERT INTO chunks (message_id, chunk_index, text, byte_start, byte_end, token_est) VALUES (?, ?, ?, ?, ?, ?)`)
			if err != nil {
				return err
			}
			for _, c := range chunks {
				if _, err := stmt.Exec(msg.Id, c.Index, c.Text, c.ByteStart, c.ByteEnd, c.TokenEst); err != nil {
					stmt.Close()
					return err
				}
			}
			stmt.Close()
		}
	}

	return tx.Commit()
}

// GetMessage retrieves a single message by ID.
func (d *DB) GetMessage(id string) (*pb.Message, error) {
	msg := &pb.Message{}
	var roleInt int
	var content []byte
	var createdAt time.Time

	err := d.QueryRow(
		`SELECT id, thread_id, role, content, position, created_at FROM messages WHERE id = ?`, id,
	).Scan(&msg.Id, &msg.ThreadId, &roleInt, &content, &msg.Position, &createdAt)
	if err != nil {
		return nil, err
	}

	msg.Role = pb.Role(roleInt)
	msg.CreatedAt = timestamppb.New(createdAt)
	msg.Content, err = unmarshalContentBlocks(content)
	return msg, err
}

// ListMessages returns messages for a thread, ordered by position.
func (d *DB) ListMessages(threadID string, limit, offset int) ([]*pb.Message, error) {
	query := `SELECT id, thread_id, role, content, position, created_at
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

	var messages []*pb.Message
	for rows.Next() {
		msg := &pb.Message{}
		var roleInt int
		var content []byte
		var createdAt time.Time

		if err := rows.Scan(&msg.Id, &msg.ThreadId, &roleInt, &content, &msg.Position, &createdAt); err != nil {
			return nil, err
		}
		msg.Role = pb.Role(roleInt)
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
func (d *DB) ThreadCorpus(threadID string) ([]*pb.Message, error) {
	// Check if this is a branch
	var parentID *string
	var branchPos *int64
	d.QueryRow(
		`SELECT parent_thread_id, branch_point_position FROM threads WHERE id = ?`, threadID,
	).Scan(&parentID, &branchPos)

	var corpus []*pb.Message

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

// AllCorpus returns every stored message across every thread — the
// candidate set for cross-thread RRC selection. Sorted by (thread_id,
// position) so in-thread ordering is preserved. Required when scope ==
// SELECTION_SCOPE_ALL_THREADS; otherwise RRC can only ever score the
// query against messages from its own thread and cross-thread edges
// never form.
func (d *DB) AllCorpus() ([]*pb.Message, error) {
	rows, err := d.Query(
		`SELECT id, thread_id, role, content, position, created_at
		 FROM messages ORDER BY thread_id, position`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var corpus []*pb.Message
	for rows.Next() {
		msg := &pb.Message{}
		var roleInt int
		var content []byte
		var createdAt time.Time
		if err := rows.Scan(&msg.Id, &msg.ThreadId, &roleInt, &content, &msg.Position, &createdAt); err != nil {
			return nil, err
		}
		msg.Role = pb.Role(roleInt)
		msg.CreatedAt = timestamppb.New(createdAt)
		msg.Content, _ = unmarshalContentBlocks(content)
		corpus = append(corpus, msg)
	}
	return corpus, rows.Err()
}

// LatestMessage returns the most recent message in a thread, or nil if empty.
func (d *DB) LatestMessage(threadID string) *pb.Message {
	msg := &pb.Message{}
	var roleInt int
	var content []byte
	var createdAt time.Time
	err := d.QueryRow(
		`SELECT id, thread_id, role, content, position, created_at
		 FROM messages WHERE thread_id = ? ORDER BY position DESC LIMIT 1`, threadID,
	).Scan(&msg.Id, &msg.ThreadId, &roleInt, &content, &msg.Position, &createdAt)
	if err != nil {
		return nil
	}
	msg.Role = pb.Role(roleInt)
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

// marshalContentBlocks encodes repeated ContentBlock as a proto wrapper.
func marshalContentBlocks(blocks []*pb.ContentBlock) ([]byte, error) {
	// Use LLMMessage as a wrapper since it has repeated ContentBlock
	wrapper := &pb.LLMMessage{Content: blocks}
	return proto.Marshal(wrapper)
}

func unmarshalContentBlocks(data []byte) ([]*pb.ContentBlock, error) {
	wrapper := &pb.LLMMessage{}
	if err := proto.Unmarshal(data, wrapper); err != nil {
		return nil, err
	}
	return wrapper.Content, nil
}

// textFromBlocks delegates to rrc.TextFromBlocks. Single source of
// truth lives in rrc/text.go — embeddings and classifier inputs
// (driven from rrc) and storage chunking key off the exact same
// string, which a divergent local copy would break.
func textFromBlocks(blocks []*pb.ContentBlock) string {
	return rrc.TextFromBlocks(blocks)
}
