// Package tiktoken provides a cl100k_base BPE TokenEstimator backed
// by github.com/pkoukk/tiktoken-go. Importing this package brings
// tiktoken-go (and its dlclark/regexp2 transitive dep) into the
// consumer's binary; importing rrc alone does not.
//
// cl100k_base is GPT-4's BPE tokenizer and is the closest widely-
// available stand-in for the family of BPE tokenizers modern LLMs
// use (Claude's internal tokenizer, minimax, most Llama derivatives).
// It's not exact for non-GPT providers — vocabularies diverge by a
// few percent to ~20% — but it's a structurally correct
// tokenization, not a character heuristic. Budget calculations treat
// its output as approximate; the reactive shed layer is the ground
// truth for "actually fits."
package tiktoken

import (
	"fmt"

	"github.com/pkoukk/tiktoken-go"

	"github.com/emontenegr/spidey/rrc"
)

const encoding = "cl100k_base"

// estimator wraps a loaded *tiktoken.Tiktoken. Implements
// rrc.TokenEstimator.
type estimator struct {
	enc *tiktoken.Tiktoken
}

// New loads the cl100k_base BPE encoder and returns it as a
// rrc.TokenEstimator. Returns an error if the encoder cannot be
// loaded (corrupt cache, network unreachable for first-run fetch).
// Callers MUST handle the error — refusing to boot is the right
// failure mode rather than silently degrading to a char-based
// heuristic that would change the unit every downstream budget
// check is denominated in.
func New() (rrc.TokenEstimator, error) {
	enc, err := tiktoken.GetEncoding(encoding)
	if err != nil {
		return nil, fmt.Errorf("tiktoken: load %s: %w", encoding, err)
	}
	return &estimator{enc: enc}, nil
}

func (e *estimator) Estimate(s string) int {
	return len(e.enc.Encode(s, nil, nil))
}
