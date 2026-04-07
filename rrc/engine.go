package rrc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Engine implements the RRC algorithm. NOT goroutine-safe — the caller
// serializes access. Operates on data structures provided at construction
// or via Load methods.
type Engine struct {
	classifier Classifier
	completer  Completer
	dag        *DAG
	scores     *ScoreCache
	qudGraphs  map[string]*QUDGraph // keyed by thread ID
	cfg        EngineConfig
}

// NewEngine creates an RRC engine with injected classifier and completer.
func NewEngine(cfg EngineConfig, classifier Classifier, completer Completer) *Engine {
	return &Engine{
		classifier: classifier,
		completer:  completer,
		dag:        newDAG(),
		scores:     newScoreCache(),
		qudGraphs:  make(map[string]*QUDGraph),
		cfg:        cfg,
	}
}

// OnMessage scores a new message against all predecessors in the corpus.
// Returns created edges. O(N) classifier calls where N is corpus size.
func (e *Engine) OnMessage(ctx context.Context, msg *pb.Message, corpus []*pb.Message) ([]*pb.Edge, error) {
	if len(corpus) == 0 {
		return nil, nil
	}

	// Build batch classify request: score every (predecessor, msg) pair
	pairs := make([]*pb.ClassifyRequest, 0, len(corpus))
	for _, prior := range corpus {
		if prior.Id == msg.Id {
			continue
		}
		pairs = append(pairs, &pb.ClassifyRequest{
			TextA: textFromMessage(prior),
			TextB: textFromMessage(msg),
		})
	}

	if len(pairs) == 0 {
		return nil, nil
	}

	resp, err := e.classifier.ClassifyBatch(ctx, &pb.BatchClassifyRequest{Pairs: pairs})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrClassifierFailed, err)
	}

	var edges []*pb.Edge
	pairIdx := 0
	for _, prior := range corpus {
		if prior.Id == msg.Id {
			continue
		}

		ceScore := entailmentScore(resp.Results[pairIdx])
		e.scores.Set(prior.Id, msg.Id, ceScore)

		if ceScore >= e.cfg.EdgeThreshold {
			// Check for QUD edge boost
			qudWeight := 0.0
			if qg, ok := e.qudGraphs[msg.ThreadId]; ok {
				for _, de := range qg.DerivedEdges() {
					if de.FromMessageId == prior.Id && de.ToMessageId == msg.Id {
						qudWeight = 1.0
						break
					}
				}
			}

			temporal := TemporalProximity(prior.Position, msg.Position)
			fusedScore := FuseScore(e.cfg, ceScore, qudWeight, temporal)

			edge := &pb.Edge{
				FromMessageId:    prior.Id,
				ToMessageId:      msg.Id,
				Score:            float32(fusedScore),
				Source:           edgeSource(qudWeight > 0),
				CrossEncoderScore: float32(ceScore),
				QudWeight:        float32(qudWeight),
				TemporalProximity: float32(temporal),
				DetectedAt:       timestamppb.Now(),
				FromThreadId:     prior.ThreadId,
				ToThreadId:       msg.ThreadId,
			}
			e.dag.AddEdge(edge)
			edges = append(edges, edge)
		}
		pairIdx++
	}

	return edges, nil
}

// Select performs prerequisite selection for a prompt. Best-first backward
// traversal through the DAG, score floor cutoff, transitive reduction.
func (e *Engine) Select(promptID string, scope pb.SelectionScope, threadID string) (*pb.SelectionResult, error) {
	// Validate inputs
	if scope == pb.SelectionScope_SELECTION_SCOPE_THREAD && threadID == "" {
		return nil, ErrThreadNotFound
	}

	// Zero-return is valid — if the prompt has no edges, return empty selection.
	// The model is capable without augmentation for a novel prompt.

	// Subgraph extraction
	selected := extractSubgraph(e.dag, promptID, threadID, scope, e.cfg)

	// Transitive reduction on selected subgraph
	selected = transitiveReduction(selected)

	// Build result
	result := &pb.SelectionResult{
		EventId:  fmt.Sprintf("sel-%s", promptID),
		Scope:    scope,
		ThreadId: threadID,
	}

	// Collect all scored but unselected messages for the excluded list
	excluded := make(map[string]bool)
	for _, s := range selected {
		excluded[s.MessageID] = true
	}

	for _, s := range selected {
		result.Selected = append(result.Selected, &pb.SelectedMessage{
			MessageId:      s.MessageID,
			EffectiveScore: float32(s.EffectiveScore),
			HopDepth:       int32(s.HopDepth),
			ViaEdges:       s.ViaEdges,
			ThreadId:       s.ThreadID,
			CrossThread:    s.CrossThread,
		})
	}

	return result, nil
}

// CarryForward processes thinking blocks from an LLM response. Updates the
// QUD graph and discovers new edges via cross-encoder scoring of thinking text.
func (e *Engine) CarryForward(ctx context.Context, input *pb.CarryForwardInput) error {
	threadID := input.ThreadId
	qg, ok := e.qudGraphs[threadID]
	if !ok {
		qg = newQUDGraph()
		e.qudGraphs[threadID] = qg
	}

	for _, tb := range input.ThinkingBlocks {
		// Edge discovery: score thinking text against prior messages via cross-encoder
		// The service provides the corpus; here we score the thinking text as a virtual message
		// This is handled by OnMessage when the service creates a synthetic message from thinking

		// QUD extraction via small fast model
		qudResp, err := e.completer.Complete(ctx, &pb.CompletionRequest{
			Messages: []*pb.LLMMessage{
				{
					Role: pb.Role_ROLE_SYSTEM,
					Content: []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{
						Text: "Extract questions under discussion from the following reasoning text. " +
							"For each question: provide the question text, whether it is newly raised or addresses an existing question, " +
							"and its status (open, partially_addressed, resolved). " +
							"Respond in JSON format: [{\"question\": \"...\", \"action\": \"raise\"|\"address\"|\"resolve\", \"target_qud\": \"...\"}]",
					}}}},
				},
				{
					Role: pb.Role_ROLE_USER,
					Content: []*pb.ContentBlock{{Block: &pb.ContentBlock_Text{Text: &pb.TextContent{
						Text: tb.Text,
					}}}},
				},
			},
			Model: "", // Service configures the small fast model at construction
		})
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCompleterFailed, err)
		}

		// Parse QUD operations from response and apply to graph
		applyQUDOperations(qg, qudResp, input.EventId, threadID)
	}

	// Feed derived edges back into the DAG
	for _, edge := range qg.DerivedEdges() {
		if !e.dag.HasMessage(edge.FromMessageId) || !e.dag.HasMessage(edge.ToMessageId) {
			continue
		}
		e.dag.AddEdge(edge)
	}

	return nil
}

// Fork creates an ephemeral engine for a forked thread. Inherits a snapshot of
// the parent's DAG and score cache. The fork has its own QUD graph.
func (e *Engine) Fork(parentThreadID string) (*Engine, error) {
	if _, ok := e.qudGraphs[parentThreadID]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrThreadNotFound, parentThreadID)
	}

	// Snapshot the DAG edges
	fork := &Engine{
		classifier: e.classifier,
		completer:  e.completer,
		dag:        newDAG(),
		scores:     newScoreCache(),
		qudGraphs:  make(map[string]*QUDGraph),
		cfg:        e.cfg,
	}

	// Copy all edges
	for _, edge := range e.dag.AllEdges() {
		fork.dag.AddEdge(edge)
	}

	// Copy all scores
	for k, v := range e.scores.All() {
		fork.scores.Set(k[0], k[1], v)
	}

	return fork, nil
}

// Merge integrates a fork's messages and edges into the parent. Cached scores
// transfer (messages are immutable). The fork's QUD graph does not merge.
func (e *Engine) Merge(fork *Engine, parentThreadID string) error {
	if _, ok := e.qudGraphs[parentThreadID]; !ok {
		return fmt.Errorf("%w: %s", ErrThreadNotFound, parentThreadID)
	}

	// Transfer edges from fork that don't exist in parent
	for _, edge := range fork.dag.AllEdges() {
		e.dag.AddEdge(edge)
	}

	// Transfer score cache entries
	for k, v := range fork.scores.All() {
		if _, exists := e.scores.Get(k[0], k[1]); !exists {
			e.scores.Set(k[0], k[1], v)
		}
	}

	return nil
}

// LoadDAG loads persisted edges into the engine (service calls on startup).
func (e *Engine) LoadDAG(edges []*pb.Edge) {
	for _, edge := range edges {
		e.dag.AddEdge(edge)
	}
}

// LoadScoreCache loads persisted scores into the engine.
func (e *Engine) LoadScoreCache(scores map[[2]string]float64) {
	for k, v := range scores {
		e.scores.Set(k[0], k[1], v)
	}
}

// LoadQUDGraph loads a persisted QUD graph for a thread.
func (e *Engine) LoadQUDGraph(threadID string, graph *pb.QUDGraph) {
	qg := &QUDGraph{
		graph: graph,
	}
	e.qudGraphs[threadID] = qg
}

// --- internal helpers ---

// textFromMessage extracts a text representation from a message's content blocks.
func textFromMessage(msg *pb.Message) string {
	var sb strings.Builder
	for _, block := range msg.Content {
		if t := block.GetText(); t != nil {
			sb.WriteString(t.Text)
		} else if t := block.GetThinking(); t != nil {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

// entailmentScore extracts the entailment probability from a classify response.
func entailmentScore(resp *pb.ClassifyResponse) float64 {
	for _, label := range resp.Labels {
		if label.Name == "entailment" {
			return float64(label.Probability)
		}
	}
	return 0
}

// edgeSource returns the appropriate EdgeSource based on whether QUD contributed.
func edgeSource(hasQUD bool) pb.EdgeSource {
	if hasQUD {
		return pb.EdgeSource_EDGE_SOURCE_BOTH
	}
	return pb.EdgeSource_EDGE_SOURCE_CROSS_ENCODER
}

// applyQUDOperations parses the small model's QUD extraction response and
// applies operations to the QUD graph. This is a best-effort parse —
// malformed output is silently ignored (fail fast applies to infrastructure
// errors, not model output quality).
func applyQUDOperations(qg *QUDGraph, resp *pb.CompletionResponse, eventID, threadID string) {
	if resp.Message == nil || len(resp.Message.Content) == 0 {
		return
	}

	// The small model responds with text containing JSON. Extract and parse.
	var text string
	for _, block := range resp.Message.Content {
		if t := block.GetText(); t != nil {
			text += t.Text
		}
	}

	// Simple JSON array parse for QUD operations
	// Format: [{"question": "...", "action": "raise"|"address"|"resolve", "target_qud": "..."}]
	type qudOp struct {
		Question  string `json:"question"`
		Action    string `json:"action"`
		TargetQUD string `json:"target_qud"`
	}

	// Find JSON array in text
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start == -1 || end == -1 || end <= start {
		return
	}

	var ops []qudOp
	if err := json.Unmarshal([]byte(text[start:end+1]), &ops); err != nil {
		return // malformed model output — silently ignore
	}

	for _, op := range ops {
		switch op.Action {
		case "raise":
			qudID := fmt.Sprintf("qud-%s-%d", eventID, len(qg.Proto().Quds))
			qg.AddQUD(&pb.QUD{
				Id:            qudID,
				Question:      op.Question,
				EstablishedBy: eventID,
				ParentQudId:   op.TargetQUD,
				Status:        pb.QUDStatus_QUD_STATUS_OPEN,
			})
		case "address":
			if op.TargetQUD != "" {
				qg.UpdateStatus(op.TargetQUD, pb.QUDStatus_QUD_STATUS_PARTIALLY_ADDRESSED, eventID)
			}
		case "resolve":
			if op.TargetQUD != "" {
				qg.UpdateStatus(op.TargetQUD, pb.QUDStatus_QUD_STATUS_RESOLVED, eventID)
			}
		}
	}
}
