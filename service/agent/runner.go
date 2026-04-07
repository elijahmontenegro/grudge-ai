package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/adapter"
	"github.com/emontenegr/spidey/service/storage"
)

// Runner orchestrates the agent loop for a thread. Sequential per thread —
// one active LLM call at a time. RRC integrates transparently.
type Runner struct {
	engine     *rrc.Engine
	completer  core.Completer
	db         *storage.DB
	threadID   string
	mu         sync.Mutex
	roundCount int
	mode       Mode
}

// Mode represents the agent's current mode.
type Mode int

const (
	ModeNormal     Mode = iota
	ModeAutonomous
	ModePlan
)

// NewRunner creates an agent runner for a thread.
func NewRunner(engine *rrc.Engine, completer core.Completer, db *storage.DB, threadID string) *Runner {
	return &Runner{
		engine:    engine,
		completer: completer,
		db:        db,
		threadID:  threadID,
	}
}

// SendMessage processes a user message: store, score, select, complete, carry-forward.
func (r *Runner) SendMessage(ctx context.Context, content string, scope pb.SelectionScope) (*pb.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Get corpus
	corpus, err := r.db.ThreadCorpus(r.threadID)
	if err != nil {
		return nil, fmt.Errorf("load corpus: %w", err)
	}

	// Create user message
	msg := &pb.Message{
		Id:       fmt.Sprintf("msg-%s-%d", r.threadID, len(corpus)),
		Role:     pb.Role_ROLE_USER,
		Content:  adapter.TextToProto(content),
		Position: int64(len(corpus)),
		ThreadId: r.threadID,
	}
	if err := r.db.InsertMessage(msg); err != nil {
		return nil, fmt.Errorf("store message: %w", err)
	}

	// Score against corpus
	edges, err := r.engine.OnMessage(ctx, msg, corpus)
	if err != nil {
		return nil, fmt.Errorf("scoring: %w", err)
	}
	for _, edge := range edges {
		r.db.InsertEdge(edge)
	}

	// Select prerequisites
	result, err := r.engine.Select(msg.Id, scope, r.threadID)
	if err != nil {
		return nil, fmt.Errorf("selection: %w", err)
	}

	// Build LLM payload from selection
	allMsgs, _ := r.db.ThreadCorpus(r.threadID)
	msgMap := make(map[string]*pb.Message)
	for _, m := range allMsgs {
		msgMap[m.Id] = m
	}

	var llmMsgs []*pb.LLMMessage
	for _, sel := range result.Selected {
		if m, ok := msgMap[sel.MessageId]; ok {
			llmMsgs = append(llmMsgs, adapter.MessageToLLM(m))
		}
	}
	// Always include the prompt
	llmMsgs = append(llmMsgs, adapter.MessageToLLM(msg))

	// Complete
	resp, err := r.completer.Complete(ctx, &pb.CompletionRequest{
		Messages: llmMsgs,
	})
	if err != nil {
		return nil, fmt.Errorf("completion: %w", err)
	}

	// Store assistant response
	assistantMsg := &pb.Message{
		Id:       fmt.Sprintf("msg-%s-%d", r.threadID, len(allMsgs)+1),
		Role:     pb.Role_ROLE_ASSISTANT,
		Content:  resp.Message.Content,
		Position: int64(len(allMsgs) + 1),
		ThreadId: r.threadID,
	}
	if err := r.db.InsertMessage(assistantMsg); err != nil {
		return nil, fmt.Errorf("store response: %w", err)
	}

	// Score assistant message
	updatedCorpus, _ := r.db.ThreadCorpus(r.threadID)
	edges, err = r.engine.OnMessage(ctx, assistantMsg, updatedCorpus[:len(updatedCorpus)-1])
	if err != nil {
		return nil, fmt.Errorf("scoring response: %w", err)
	}
	for _, edge := range edges {
		r.db.InsertEdge(edge)
	}

	// Carry-forward: extract thinking blocks
	var thinkingBlocks []*pb.ThinkingContent
	for _, b := range resp.Message.Content {
		if t := b.GetThinking(); t != nil {
			thinkingBlocks = append(thinkingBlocks, t)
		}
	}
	if len(thinkingBlocks) > 0 {
		r.engine.CarryForward(ctx, &pb.CarryForwardInput{
			EventId:        result.EventId,
			ThreadId:       r.threadID,
			ThinkingBlocks: thinkingBlocks,
		})
	}

	r.roundCount++
	return assistantMsg, nil
}

// RoundCount returns the number of completed rounds.
func (r *Runner) RoundCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.roundCount
}

// SetMode sets the agent's current mode.
func (r *Runner) SetMode(m Mode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode = m
}
