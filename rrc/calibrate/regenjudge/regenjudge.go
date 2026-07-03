// Package regenjudge is the live CounterfactualJudge: it decides whether a
// candidate is a true prerequisite of a turn by regenerative counterfactual
// coherence. It lives in its own package (not calibrate) so the calibrate
// math stays free of any LLM dependency. It is wired by the substrate
// Holder's mass refit (judge = main completer, provider = the replayed
// in-memory corpus).
//
// HONEST SCOPE: this implementation is the one-call ECONOMY form of the
// counterfactual — it asks the judge model to compare with-vs-without in
// a single judgment rather than paying two fresh regenerations plus a
// comparison per label. Same ground-truth definition, cheaper estimator
// of it, and correspondingly more exposed to the judge model's topical-
// similarity bias. If labels prove noisy, the two-regeneration form is
// the upgrade path behind this same interface.
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

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
	"github.com/elijahmontenegro/grudge/rrc"
)

// Completer is the one-method completion contract the judge needs.
// Declared locally (rather than importing core.Completer, which also
// carries Stream) so the rrc stratum never imports core — any
// core.Completer satisfies it structurally.
type Completer interface {
	Complete(ctx context.Context, req *llmv1.CompletionRequest) (*llmv1.CompletionResponse, error)
}

// TurnContext supplies the material a judgment needs for one (turn,
// candidate) pair: the local discourse the turn was interpreted from and the
// candidate message under test. The offline harness builds these by replaying
// the lossless corpus.
type TurnContext struct {
	LocalContext []*threadv1.Message // the active discourse of the turn
	Candidate    *threadv1.Message   // the message whose prerequisite-ness is judged
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
	completer Completer
	provider  ContextProvider
}

func New(completer Completer, provider ContextProvider) *Judge {
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
	req := &llmv1.CompletionRequest{
		Messages: []*llmv1.LLMMessage{
			{Role: threadv1.Role_ROLE_SYSTEM, Content: pbtext.BlocksFromText(systemInstruction)},
			{Role: threadv1.Role_ROLE_USER, Content: pbtext.BlocksFromText(prompt)},
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

// Prompt bounds: one long tool-loop turn must not overflow the judge
// model's context — a single oversized prompt would error, abort the
// whole replay, and (until the attempt watermark advances) re-abort on
// every retry. Discourse keeps the most recent messages; every message
// serialization is capped. Bounds are generous for judgment quality and
// exist to prevent wedging, not to trim routinely.
const (
	promptMaxMessages = 12
	promptMaxMsgChars = 4000
)

func capped(s string) string {
	if len(s) <= promptMaxMsgChars {
		return s
	}
	return s[:promptMaxMsgChars] + "\n[...truncated for judgment...]\n"
}

func buildPrompt(tc TurnContext) string {
	var b strings.Builder
	b.WriteString("RECENT DISCOURSE (the active turn):\n")
	local := tc.LocalContext
	if len(local) > promptMaxMessages {
		local = local[len(local)-promptMaxMessages:]
	}
	for _, m := range local {
		b.WriteString(capped(rrc.SerializeMessageForScoring(m)))
	}
	b.WriteString("\nCANDIDATE EARLIER MESSAGE:\n")
	if tc.Candidate != nil {
		b.WriteString(capped(rrc.SerializeMessageForScoring(tc.Candidate)))
	}
	b.WriteString("\nIs the candidate a prerequisite for a coherent continuation? Answer YES or NO.")
	return b.String()
}

// parseVerdict reads the model's answer as yes/no, defaulting to NO (the
// precision-first default: when the judge is unclear, do not label a
// candidate a prerequisite).
func parseVerdict(resp *llmv1.CompletionResponse) bool {
	if resp == nil || resp.Message == nil {
		return false
	}
	text := strings.ToLower(pbtext.TextFromBlocks(resp.Message.Content))
	text = strings.TrimSpace(text)
	// Look for a leading yes; default no.
	return strings.HasPrefix(text, "yes")
}
