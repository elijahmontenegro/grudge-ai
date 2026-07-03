package regenjudge

import (
	"context"
	"iter"
	"testing"

	"github.com/elijahmontenegro/grudge/core"
	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
)

// Compile-time proof the live judge satisfies the calibrate interface.
var _ calibrate.CounterfactualJudge = (*Judge)(nil)

type fakeCompleter struct{ reply string }

func (f fakeCompleter) Complete(_ context.Context, _ *pb.CompletionRequest) (*pb.CompletionResponse, error) {
	return &pb.CompletionResponse{Message: &pb.LLMMessage{
		Role:    pb.Role_ROLE_ASSISTANT,
		Content: rrc.BlocksFromText(f.reply),
	}}, nil
}
func (f fakeCompleter) Stream(_ context.Context, _ *pb.CompletionRequest) iter.Seq2[*pb.StreamChunk, error] {
	return func(yield func(*pb.StreamChunk, error) bool) {}
}

var _ core.Completer = fakeCompleter{}

type mapProvider map[string]TurnContext

func (m mapProvider) Resolve(turnID, candidateID string) (TurnContext, error) {
	return m[turnID+"|"+candidateID], nil
}

func tc() TurnContext {
	return TurnContext{
		LocalContext: []*pb.Message{{Role: pb.Role_ROLE_USER, Content: rrc.BlocksFromText("current discourse")}},
		Candidate:    &pb.Message{Role: pb.Role_ROLE_USER, Content: rrc.BlocksFromText("earlier message")},
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
