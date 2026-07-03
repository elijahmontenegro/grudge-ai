package regenjudge

import (
	"context"
	"iter"
	"testing"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
)

// Compile-time proof the live judge satisfies the calibrate interface.
var _ calibrate.CounterfactualJudge = (*Judge)(nil)

type fakeCompleter struct{ reply string }

func (f fakeCompleter) Complete(_ context.Context, _ *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error) {
	return &llmv1.CompletionResponse{Message: &llmv1.LLMMessage{
		Role:    threadv1.Role_ROLE_ASSISTANT,
		Content: pbtext.BlocksFromText(f.reply),
	}}, nil
}
func (f fakeCompleter) Stream(_ context.Context, _ *llmv1.CompletionRequest) iter.Seq2[*llmv1.StreamChunk, error] {
	return func(yield func(*llmv1.StreamChunk, error) bool) {}
}

var _ Completer = fakeCompleter{}

type mapProvider map[string]TurnContext

func (m mapProvider) Resolve(turnID, candidateID string) (TurnContext, error) {
	return m[turnID+"|"+candidateID], nil
}

func tc() TurnContext {
	return TurnContext{
		LocalContext: []*threadv1.Message{{Role: threadv1.Role_ROLE_USER, Content: pbtext.BlocksFromText("current discourse")}},
		Candidate:    &threadv1.Message{Role: threadv1.Role_ROLE_USER, Content: pbtext.BlocksFromText("earlier message")},
	}
}

func TestJudge_YesNoParsing(t *testing.T) {
	prov := mapProvider{"t|c": tc()}

	yes := New(fakeCompleter{reply: "YES"}, prov)
	if got, err := yes.IsPrerequisite(context.Background(), "t", "c"); err != nil || !got {
		t.Fatalf("YES reply should judge prerequisite; got %v err %v", got, err)
	}

	no := New(fakeCompleter{reply: "NO, it is only topically similar."}, prov)
	if got, err := no.IsPrerequisite(context.Background(), "t", "c"); err != nil || got {
		t.Fatalf("NO reply should judge non-prerequisite; got %v err %v", got, err)
	}

	// Unclear reply defaults NO (precision-first).
	unclear := New(fakeCompleter{reply: "hmm, hard to say"}, prov)
	if got, _ := unclear.IsPrerequisite(context.Background(), "t", "c"); got {
		t.Fatal("unclear reply must default to NO")
	}
}
