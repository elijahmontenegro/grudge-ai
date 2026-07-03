package messages

import (
	"strings"
	"testing"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
)

type testEstimator struct{}

func (testEstimator) Estimate(s string) int { return len(s)/4 + 1 }

func TestChunksForIndexesEveryMessageShape(t *testing.T) {
	chunk.SetDefaultEstimator(testEstimator{})
	tests := []struct {
		name    string
		message *pb.Message
		want    string
	}{
		{
			name:    "empty",
			message: &pb.Message{Id: "empty", Role: pb.Role_ROLE_ASSISTANT},
			want:    "role=assistant",
		},
		{
			name: "image",
			message: &pb.Message{
				Id: "image", Role: pb.Role_ROLE_USER,
				Content: []*pb.ContentBlock{{
					Block: &pb.ContentBlock_Image{Image: &pb.ImageContent{
						MediaType: "image/png", Data: []byte{1, 2, 3},
					}},
				}},
			},
			want: "[image media_type=image/png bytes=3]",
		},
		{
			name: "tool result",
			message: &pb.Message{
				Id: "result", Role: pb.Role_ROLE_USER,
				Content: []*pb.ContentBlock{{
					Block: &pb.ContentBlock_ToolResult{ToolResult: &pb.ToolResultContent{
						ToolCallId: "call-1", Content: "done",
					}},
				}},
			},
			want: "tool_call_id=call-1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chunks := chunksFor(test.message, chunk.DefaultConfig())
			if len(chunks) == 0 {
				t.Fatal("message was not indexed")
			}
			if !strings.Contains(chunks[0].Text, test.want) {
				t.Fatalf("projection %q does not contain %q", chunks[0].Text, test.want)
			}
		})
	}
}
