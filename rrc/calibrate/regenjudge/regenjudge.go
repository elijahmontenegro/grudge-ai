// Package regenjudge is the live CounterfactualJudge: it decides whether a
// candidate is a true prerequisite of a turn by regenerative counterfactual
// coherence. It lives in its own package (not calibrate) so the calibrate
// math stays free of any LLM/core dependency; the offline fit command wires
// this in, tests inject a fake.
//
// Ground-truth definition (see calibrate.CounterfactualJudge): a candidate is
// a prerequisite iff the turn's continuation is materially better with it
// present than without it. This routes through what the model does, never
// through whether selection picked the candidate — the property that breaks
// the self-referential-label circularity.
package regenjudge

import (
	"context"
	"fmt"
	"strings"

	"github.com/elijahmontenegro/grudge/core"
	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"github.com/elijahmontenegro/grudge/rrc"
)

// TurnContext supplies the material a judgment needs for one (turn,
// candidate) pair: the local discourse the turn was interpreted from and the
// candidate message under test. The offline harness builds these by replaying
// the lossless corpus.
type TurnContext struct {
	LocalContext []*pb.Message // the active discourse of the turn
	Candidate    *pb.Message   // the message whose prerequisite-ness is judged
}

// ContextProvider resolves a (turnID, candidateID) into the material a
// judgment needs. Implemented against storage by the offline command; a map
// in tests.
type ContextProvider interface {
	Resolve(turnID, candidateID string) (TurnContext, error)
}

// Judge implements calibrate.CounterfactualJudge by regeneration: it asks a
// judge model whether the continuation of the local discourse would be
// materially better if the candidate were included. Comparing the model's
// verdict on with-vs-without is what avoids scoring a historical target that
// was produced without the candidate.
type Judge struct {
	completer core.Completer
	provider  ContextProvider
}

func New(completer core.Completer, provider ContextProvider) *Judge {
	return &Judge{completer: completer, provider: provider}
}

// IsPrerequisite renders a judgment prompt (local discourse + candidate) and
// asks the judge model for a strict yes/no. It reads only turn/candidate
// material and the model's answer — never sim/mass — preserving label
// independence from the signals being calibrated.
func (j *Judge) IsPrerequisite(ctx context.Context, turnID, candidateID string) (bool, error) {
	tc, err := j.provider.Resolve(turnID, candidateID)
	if err != nil {
		return false, fmt.Errorf("regenjudge resolve turn=%s candidate=%s: %w", turnID, candidateID, err)
	}
	prompt := buildPrompt(tc)
	req := &pb.CompletionRequest{
		Messages: []*pb.LLMMessage{
			{Role: pb.Role_ROLE_SYSTEM, Content: rrc.BlocksFromText(systemInstruction)},
			{Role: pb.Role_ROLE_USER, Content: rrc.BlocksFromText(prompt)},
		},
	}
	resp, err := j.completer.Complete(ctx, req)
	if err != nil {
		return false, fmt.Errorf("regenjudge complete turn=%s: %w", turnID, err)
	}
	return parseVerdict(resp), nil
}

const systemInstruction = "You are a strict judge of conversational dependency. " +
	"You are given the recent discourse of a conversation turn and one earlier " +
	"candidate message. Answer whether the candidate is a PREREQUISITE for a " +
	"coherent continuation of the turn: would including it materially improve " +
	"the next response, versus the response the model would give without it? " +
	"A candidate that is merely topically similar but not required is NOT a " +
	"prerequisite. Answer with exactly one word: YES or NO."

func buildPrompt(tc TurnContext) string {
	var b strings.Builder
	b.WriteString("RECENT DISCOURSE (the active turn):\n")
	for _, m := range tc.LocalContext {
		b.WriteString(rrc.SerializeMessageForScoring(m))
	}
	b.WriteString("\nCANDIDATE EARLIER MESSAGE:\n")
	if tc.Candidate != nil {
		b.WriteString(rrc.SerializeMessageForScoring(tc.Candidate))
	}
	b.WriteString("\nIs the candidate a prerequisite for a coherent continuation? Answer YES or NO.")
	return b.String()
}

// parseVerdict reads the model's answer as yes/no, defaulting to NO (the
// precision-first default: when the judge is unclear, do not label a
// candidate a prerequisite).
func parseVerdict(resp *pb.CompletionResponse) bool {
	if resp == nil || resp.Message == nil {
		return false
	}
	text := strings.ToLower(rrc.TextFromBlocks(resp.Message.Content))
	text = strings.TrimSpace(text)
	// Look for a leading yes; default no.
	return strings.HasPrefix(text, "yes")
}
