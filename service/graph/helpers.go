package graph

import (
	"context"
	"fmt"
	"strings"

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/service/adapter"
)

func completeWithBackoff(ctx context.Context, completer core.Completer, msgs []*pb.LLMMessage, selected []*pb.SelectedMessage) (*pb.CompletionResponse, error) {
	for {
		resp, err := completer.Complete(ctx, &pb.CompletionRequest{Messages: msgs})
		if err == nil {
			return resp, nil
		}

		// Check if it's a context-length error
		errMsg := err.Error()
		isContextLength := strings.Contains(errMsg, "context_length") ||
			strings.Contains(errMsg, "maximum context") ||
			strings.Contains(errMsg, "too many tokens") ||
			strings.Contains(errMsg, "max_tokens")

		if !isContextLength || len(msgs) <= 1 {
			return nil, err
		}

		// Drop the lowest-scored message (first in payload, which is lowest after prompt)
		msgs = msgs[1:]
	}
}

// streamCompletion streams a completion, publishing deltas to the subscription
// channel. Returns the accumulated content blocks for storage.
func streamCompletion(ctx context.Context, completer core.Completer, msgs []*pb.LLMMessage, msgID, threadID string, publish func(string, *StreamEvent)) ([]*pb.ContentBlock, error) {
	ch, err := completer.Stream(ctx, &pb.CompletionRequest{Messages: msgs})
	if err != nil {
		return nil, err
	}

	var blocks []*pb.ContentBlock
	var textBuf strings.Builder

	for chunk := range ch {
		if chunk.Error != nil {
			publish(threadID, &StreamEvent{
				MessageID: msgID,
				Error:     chunk.Error,
				Done:      true,
			})
			return nil, fmt.Errorf("%s", *chunk.Error)
		}

		if chunk.Done {
			break
		}

		switch d := chunk.Delta.(type) {
		case *pb.StreamChunk_Text:
			text := d.Text.Text
			textBuf.WriteString(text)
			publish(threadID, &StreamEvent{
				MessageID: msgID,
				Delta:     &text,
			})
		case *pb.StreamChunk_Thinking:
			text := d.Thinking.Text
			publish(threadID, &StreamEvent{
				MessageID: msgID,
				Thinking:  &text,
			})
			blocks = append(blocks, &pb.ContentBlock{
				Block: &pb.ContentBlock_Thinking{Thinking: &pb.ThinkingContent{Text: text}},
			})
		}
	}

	// Flush accumulated text as a content block
	if textBuf.Len() > 0 {
		blocks = append(blocks, &pb.ContentBlock{
			Block: &pb.ContentBlock_Text{Text: &pb.TextContent{Text: textBuf.String()}},
		})
	}

	// Signal done
	publish(threadID, &StreamEvent{
		MessageID: msgID,
		Done:      true,
	})

	return blocks, nil
}

func protoThreadToGQL(t *pb.Thread) *Thread {
	gql := &Thread{
		ID:          t.Id,
		Name:        t.Name,
		WorkingDirs: t.WorkingDirs,
		Sandboxed:   t.Sandboxed,
		CreatedAt:   t.CreatedAt.AsTime(),
	}
	if t.ParentThreadId != nil {
		gql.ParentThreadID = t.ParentThreadId
	}
	if t.BranchPointPosition != nil {
		pos := int(*t.BranchPointPosition)
		gql.BranchPointPosition = &pos
	}
	if t.ArchivedAt != nil {
		at := t.ArchivedAt.AsTime()
		gql.ArchivedAt = &at
	}
	return gql
}

func protoMessageToGQL(m *pb.Message) *Message {
	return &Message{
		ID:        m.Id,
		Role:      m.Role.String(),
		Content:   adapter.ProtoToText(m.Content),
		Position:  int(m.Position),
		ThreadID:  m.ThreadId,
		CreatedAt: m.CreatedAt.AsTime(),
	}
}

func protoQUDGraphToGQL(g *pb.QUDGraph) *QUDGraph {
	if g == nil {
		return &QUDGraph{}
	}
	quds := make([]*QUD, len(g.Quds))
	for i, q := range g.Quds {
		quds[i] = &QUD{
			ID:            q.Id,
			Question:      q.Question,
			EstablishedBy: q.EstablishedBy,
			Status:        q.Status.String(),
			AddressedBy:   q.AddressedBy,
		}
		if q.ParentQudId != "" {
			quds[i].ParentQudID = &q.ParentQudId
		}
	}
	return &QUDGraph{
		Quds:        quds,
		ActiveStack: g.ActiveStack,
	}
}
