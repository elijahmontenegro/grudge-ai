package proxy

import (
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
)

// ExtractThinking extracts thinking blocks from a stream for carry-forward.
// Called concurrently during response streaming — collects thinking content
// while the stream passes through to the client.
func ExtractThinking(chunks <-chan *pb.StreamChunk) ([]*pb.ThinkingContent, <-chan *pb.StreamChunk) {
	var thinking []*pb.ThinkingContent
	passthrough := make(chan *pb.StreamChunk)

	go func() {
		defer close(passthrough)
		for chunk := range chunks {
			if t := chunk.GetThinking(); t != nil {
				thinking = append(thinking, t)
			}
			passthrough <- chunk
		}
	}()

	return thinking, passthrough
}
