package messages

import (
	"strings"
	"testing"

	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
)

type testEstimator struct{}

func (testEstimator) Estimate(s string) int { return len(s)/4 + 1 }

func TestChunksForIndexesEveryMessageShape(t *testing.T) {
	tests := []struct {
		name    string
		message *threadv1.Message
		want    string
	}{
		{
			name:    "empty",
			message: &threadv1.Message{Id: "empty", Role: threadv1.Role_ROLE_ASSISTANT},
			want:    "role=assistant",
		},
		{
			name: "image",
			message: &threadv1.Message{
				Id: "image", Role: threadv1.Role_ROLE_USER,
				Content: []*threadv1.ContentBlock{{
					Block: &threadv1.ContentBlock_Image{Image: &threadv1.ImageContent{
						MediaType: "image/png", Data: []byte{1, 2, 3},
					}},
				}},
			},
			want: "[image media_type=image/png bytes=3]",
		},
		{
			name: "tool result",
			message: &threadv1.Message{
				Id: "result", Role: threadv1.Role_ROLE_USER,
				Content: []*threadv1.ContentBlock{{
					Block: &threadv1.ContentBlock_ToolResult{ToolResult: &threadv1.ToolResultContent{
						ToolCallId: "call-1", Content: "done",
					}},
				}},
			},
			want: "tool_call_id=call-1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chunks := chunksFor(test.message, chunk.Config{MaxChars: 2000, OverlapChars: 200, Estimator: testEstimator{}})
			if len(chunks) == 0 {
				t.Fatal("message was not indexed")
			}
			if !strings.Contains(chunks[0].Text, test.want) {
				t.Fatalf("projection %q does not contain %q", chunks[0].Text, test.want)
			}
		})
	}
}
