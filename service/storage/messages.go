package storage

import (
	"time"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// InsertMessage stores a message with proto-encoded content.
func (d *DB) InsertMessage(msg *pb.Message) error {
	content, err := marshalContentBlocks(msg.Content)
	if err != nil {
		return err
	}

	_, err = d.Exec(
		`INSERT INTO messages (id, thread_id, role, content, position, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		msg.Id, msg.ThreadId, int(msg.Role), content, msg.Position, msg.CreatedAt.AsTime(),
	)
	return err
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
func (d *DB) ThreadCorpus(threadID string) ([]*pb.Message, error) {
	return d.ListMessages(threadID, 0, 0)
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
