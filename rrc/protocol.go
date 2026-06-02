package rrc

// Protocol-adherence rule pipeline.
//
// RRC's Selection + Radius produces a wire payload that represents
// prerequisite context for the current query. Providers expect that
// wire to meet specific structural constraints (paired tool_call/
// tool_result, a user-role anchor somewhere in the conversation,
// etc.). When Selection's output doesn't satisfy a constraint, the
// missing piece is filled from the Store by rule, not fabricated.
//
// Design invariant — single source of truth:
//
//	Every message's content in the output wire is Store-resident
//	(present in the input wire or fetched from the persisted
//	corpus). Protocol-plumbing ids may be rebound to satisfy wire
//	shape. No content is fabricated; no roles are altered at the
//	block level.
//
// The candidate a rule fetches is a specific BLOCK (a tool_call
// block, a tool_result block, a user-text block) from a scored
// pb.Message in the Store. Messages are the storage unit; blocks
// are the protocol-slot unit. The rule extracts exactly the
// required block, wraps it in a fresh pb.LLMMessage with the role
// that block type requires on the wire, and rebinds protocol-
// plumbing ids as needed. Other blocks on the candidate message
// are not dragged along.
//
// Candidate consumption is score-ordered. Within a single pipeline
// pass, each picked candidate message is marked used and not
// returned again — so two orphan tool_results don't both pair with
// the same top-scored tool_call. Messages already represented in
// the input wire are seeded into the used set before any rule
// fires, preventing a message that's already contributing to the
// wire (via Selection or Radius) from being re-fetched under a
// different id.

import (
	"fmt"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

// Rule is a pure transform: given the current wire and a resolver
// capable of fetching Store-resident candidates, produce the next
// wire. Rules are composable; each runs against the output of the
// previous.
//
// Rules MUST NOT apply size/count caps internally. The caller
// (assembly layer) budgets rectification inserts together with
// Selected entries under the single ContextBudget, shedding
// lowest-score across the combined pool. Rules just propose
// candidates; budget governs.
type Rule func(wire []*pb.LLMMessage, r Resolver) ([]*pb.LLMMessage, error)

// Resolver is the single interface through which rules reach into
// the Store for protocol-slot fills.
//
// BestCandidate returns the highest-scored message in the Store
// scope that passes the filter and hasn't been consumed earlier in
// this pipeline pass. On pick, the resolver records the candidate
// as used (subsequent calls skip it) and appends the pick to the
// log readable via Picks(). When no candidate passes, returns nil.
type Resolver interface {
	BestCandidate(filter func(*pb.Message) bool) (*pb.Message, error)
	Picks() []ResolverPick
}

// ResolverPick records one consumption event from a Resolver. The
// score (rerank-max against the current query) is consulted by Apply
// when correlating new wire entries to the rule that produced them,
// so the assembly layer can put rectification inserts on the same
// score axis as Selected entries for unified budget shed.
type ResolverPick struct {
	MsgID string
	Score float64 // rerank-max against the query
}

// Apply runs rules in sequence against the wire. Returns the
// rectified wire and a map from rectification-insert pointer to
// that insert's score (rerank-max against the current query).
// Caller uses the score map to place rectification inserts on the
// same axis as Selected entries for unified budget decisions.
//
// Per-rule correlation: before each rule fires, Apply snapshots
// the set of pointers in the input wire and the resolver's pick
// log index. After the rule runs, any pointer in the output wire
// that wasn't in the input is a new insert; scan-order of new
// inserts within one rule's output matches chronological pick
// order within that rule (rules iterate wire in order and append
// inserts as they encounter orphans). That gives an exact
// insert→pick mapping without requiring rules to hand-record it.
func Apply(wire []*pb.LLMMessage, r Resolver, rules ...Rule) ([]*pb.LLMMessage, map[*pb.LLMMessage]float64, error) {
	insertScores := make(map[*pb.LLMMessage]float64)
	for _, rule := range rules {
		priorPtrs := make(map[*pb.LLMMessage]struct{}, len(wire))
		for _, m := range wire {
			priorPtrs[m] = struct{}{}
		}
		priorPickCount := len(r.Picks())

		next, err := rule(wire, r)
		if err != nil {
			return nil, nil, fmt.Errorf("rule: %w", err)
		}

		ruleP := r.Picks()[priorPickCount:]
		idx := 0
		for _, m := range next {
			if _, was := priorPtrs[m]; was {
				continue
			}
			if idx < len(ruleP) {
				insertScores[m] = ruleP[idx].Score
				idx++
			}
		}
		wire = next
	}
	return wire, insertScores, nil
}

// === Rules ===

// EnsureUserAnchor guarantees the wire contains at least one
// user-role message with non-empty text. When absent, the highest-
// scored user candidate is fetched and inserted at position 1
// (immediately after system) — the frame-setting position, not the
// tail. Tail is where the model reads "the live ask"; an anchor
// there would cause the model to re-enter that ask rather than
// continue whatever it was doing.
//
// The extracted content is exactly the candidate's text block
// wrapped in a minimal pb.LLMMessage with role=USER. Other blocks
// on the source message are not included.
var EnsureUserAnchor Rule = func(wire []*pb.LLMMessage, r Resolver) ([]*pb.LLMMessage, error) {
	if containsUserAnchor(wire) {
		return wire, nil
	}
	cand, err := r.BestCandidate(isUserAnchor)
	if err != nil {
		return nil, err
	}
	if cand == nil {
		return wire, nil
	}
	extracted := extractUserAnchor(cand)
	if extracted == nil {
		return wire, nil
	}
	return insertAfterSystem(wire, extracted), nil
}

// PairToolCallsWithResults ensures each tool_call block in the
// wire has a matching tool_result somewhere after it. When a
// counterpart isn't present, the highest-scored tool_result
// candidate is fetched, its block extracted, and its tool_call_id
// rebound to the orphan's id. The rebound block is emitted as a
// standalone pb.LLMMessage.
//
// Orphans remaining after this rule — calls whose Store had no
// tool_result candidate at all — are left for the canonicalizer to
// drop as honest fallback.
var PairToolCallsWithResults Rule = func(wire []*pb.LLMMessage, r Resolver) ([]*pb.LLMMessage, error) {
	out := make([]*pb.LLMMessage, 0, len(wire))
	for i, m := range wire {
		out = append(out, m)
		for _, b := range m.Content {
			tc := b.GetToolCall()
			if tc == nil {
				continue
			}
			if hasPairedResult(wire, i, tc.Id) {
				continue
			}
			cand, err := r.BestCandidate(hasToolResult)
			if err != nil {
				return nil, err
			}
			if cand == nil {
				continue
			}
			extracted := extractToolResultBlock(cand, tc.Id)
			if extracted == nil {
				continue
			}
			out = append(out, extracted)
		}
	}
	return out, nil
}

// PairToolResultsWithCalls ensures each tool_result block in the
// wire has a matching tool_call somewhere before it. Inverse of
// PairToolCallsWithResults. Fetched candidate's tool_call id is
// rebound to the orphan's tool_call_id.
var PairToolResultsWithCalls Rule = func(wire []*pb.LLMMessage, r Resolver) ([]*pb.LLMMessage, error) {
	out := make([]*pb.LLMMessage, 0, len(wire))
	for i, m := range wire {
		for _, b := range m.Content {
			tr := b.GetToolResult()
			if tr == nil {
				continue
			}
			if hasPairedCall(wire, i, tr.ToolCallId) {
				continue
			}
			cand, err := r.BestCandidate(hasToolCall)
			if err != nil {
				return nil, err
			}
			if cand == nil {
				continue
			}
			extracted := extractToolCallBlock(cand, tr.ToolCallId)
			if extracted == nil {
				continue
			}
			out = append(out, extracted)
		}
		out = append(out, m)
	}
	return out, nil
}

// === Candidate filters ===

func isUserAnchor(m *pb.Message) bool {
	if m.Role != pb.Role_ROLE_USER {
		return false
	}
	for _, b := range m.Content {
		if t := b.GetText(); t != nil && t.Text != "" {
			return true
		}
	}
	return false
}

func hasToolCall(m *pb.Message) bool {
	for _, b := range m.Content {
		if b.GetToolCall() != nil {
			return true
		}
	}
	return false
}

func hasToolResult(m *pb.Message) bool {
	for _, b := range m.Content {
		if b.GetToolResult() != nil {
			return true
		}
	}
	return false
}

// === Wire inspection ===

func containsUserAnchor(wire []*pb.LLMMessage) bool {
	for _, m := range wire {
		if m.Role != pb.Role_ROLE_USER {
			continue
		}
		for _, b := range m.Content {
			if t := b.GetText(); t != nil && t.Text != "" {
				return true
			}
		}
	}
	return false
}

// hasPairedResult reports whether a tool_result with the given
// tool_call_id appears anywhere in wire[i+1:]. Strict "immediately
// after" isn't required at the Rule layer — the canonicalizer
// re-orders so each call is followed by its matching results.
func hasPairedResult(wire []*pb.LLMMessage, i int, callID string) bool {
	for j := i + 1; j < len(wire); j++ {
		for _, b := range wire[j].Content {
			if tr := b.GetToolResult(); tr != nil && tr.ToolCallId == callID {
				return true
			}
		}
	}
	return false
}

// hasPairedCall reports whether a tool_call with the given id
// appears anywhere in wire[:i].
func hasPairedCall(wire []*pb.LLMMessage, i int, callID string) bool {
	for j := 0; j < i; j++ {
		for _, b := range wire[j].Content {
			if tc := b.GetToolCall(); tc != nil && tc.Id == callID {
				return true
			}
		}
	}
	return false
}

// === Block extraction (single-block LLMMessage construction) ===

// extractToolCallBlock walks the candidate's content, picks the
// first tool_call block, and returns a new pb.LLMMessage carrying
// ONLY that block with its id rebound to targetID. Role is
// ROLE_ASSISTANT (the protocol role for a message carrying
// tool_calls). Returns nil if the candidate has no tool_call block.
func extractToolCallBlock(cand *pb.Message, targetID string) *pb.LLMMessage {
	for _, b := range cand.Content {
		tc := b.GetToolCall()
		if tc == nil {
			continue
		}
		return &pb.LLMMessage{
			Role: pb.Role_ROLE_ASSISTANT,
			Content: []*pb.ContentBlock{{
				Block: &pb.ContentBlock_ToolCall{
					ToolCall: &pb.ToolCallContent{
						Id:        targetID,
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				},
			}},
		}
	}
	return nil
}

// extractToolResultBlock walks the candidate's content, picks the
// first tool_result block, returns a new pb.LLMMessage carrying
// ONLY that block with its tool_call_id rebound to targetID.
// Role is ROLE_ASSISTANT in storage (matches how the runner
// persists tool results — see service/agent/runner.go). The
// adapter's canonicalizer detects the tool_result block and emits
// role:"tool" on the OpenAI/ollama wire; Anthropic adapter wraps
// it in a user-role message. Role conversion is the adapter's
// concern, not the pipeline's.
func extractToolResultBlock(cand *pb.Message, targetID string) *pb.LLMMessage {
	for _, b := range cand.Content {
		tr := b.GetToolResult()
		if tr == nil {
			continue
		}
		return &pb.LLMMessage{
			Role: pb.Role_ROLE_ASSISTANT,
			Content: []*pb.ContentBlock{{
				Block: &pb.ContentBlock_ToolResult{
					ToolResult: &pb.ToolResultContent{
						ToolCallId: targetID,
						Content:    tr.Content,
					},
				},
			}},
		}
	}
	return nil
}

// extractUserAnchor picks the first non-empty text block from the
// candidate and returns a minimal user-role LLMMessage carrying
// only that text. No thinking, no tool calls, no extra blocks.
func extractUserAnchor(cand *pb.Message) *pb.LLMMessage {
	for _, b := range cand.Content {
		t := b.GetText()
		if t == nil || t.Text == "" {
			continue
		}
		return &pb.LLMMessage{
			Role: pb.Role_ROLE_USER,
			Content: []*pb.ContentBlock{{
				Block: &pb.ContentBlock_Text{
					Text: &pb.TextContent{Text: t.Text},
				},
			}},
		}
	}
	return nil
}

// === Wire mutation ===

func insertAfterSystem(wire []*pb.LLMMessage, msg *pb.LLMMessage) []*pb.LLMMessage {
	pos := 0
	if len(wire) > 0 && wire[0].Role == pb.Role_ROLE_SYSTEM {
		pos = 1
	}
	out := make([]*pb.LLMMessage, 0, len(wire)+1)
	out = append(out, wire[:pos]...)
	out = append(out, msg)
	out = append(out, wire[pos:]...)
	return out
}
