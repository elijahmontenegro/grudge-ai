package agent

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/adapter"
	"github.com/emontenegr/spidey/service/storage"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// Runner orchestrates the agent loop for a thread via ADK.
// RRC integrates as the model.LLM implementation — ADK doesn't know.
type Runner struct {
	engine    *rrc.Engine
	completer core.Completer
	db        *storage.DB
	threadID  string
	tools     []tool.Tool
	adkRunner *runner.Runner
	mu        sync.Mutex
	mode      Mode
	autoState *AutonomousState
	onStream  adapter.StreamCallback
}

// Mode represents the agent's current mode.
type Mode int

const (
	ModeNormal     Mode = iota
	ModeAutonomous
	ModePlan
)

// NewRunner creates an agent runner for a thread.
// SetStreamCallback sets the callback for streaming deltas (for subscription publishing).
func (r *Runner) SetStreamCallback(cb adapter.StreamCallback) {
	r.onStream = cb
}

func NewRunner(engine *rrc.Engine, completer core.Completer, db *storage.DB, threadID string, tools []tool.Tool, modelName string) (*Runner, error) {
	r := &Runner{
		engine:    engine,
		completer: completer,
		db:        db,
		threadID:  threadID,
		tools:     tools,
	}

	// RRC-as-LLM: ADK calls this thinking it's an LLM
	rrcLLM := adapter.NewRRCLLM(engine, completer, db, threadID, modelName)
	// Wire streaming: use closure so it captures the runner's current callback
	rrcLLM.OnStream = func(delta, thinking string, done bool) {
		if r.onStream != nil {
			r.onStream(delta, thinking, done)
		}
	}

	// Create the ADK agent with RRC as its model
	rootAgent, err := llmagent.New(llmagent.Config{
		Name:        "spidey",
		Description: "Spidey agentic assistant with RRC-powered selective memory",
		Model:       rrcLLM,
		Tools:       tools,
		AfterModelCallbacks: []llmagent.AfterModelCallback{
			r.afterModelCallback,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	// Create the ADK runner
	adkRunner, err := runner.New(runner.Config{
		AppName:           "spidey",
		Agent:             rootAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create runner: %w", err)
	}

	r.adkRunner = adkRunner
	return r, nil
}

// SendMessage processes a user message through the ADK agent loop.
func (r *Runner) SendMessage(ctx context.Context, content string, scope pb.SelectionScope) (*pb.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Store user message
	corpus, err := r.db.ThreadCorpus(r.threadID)
	if err != nil {
		return nil, fmt.Errorf("load corpus: %w", err)
	}

	userMsg := &pb.Message{
		Id:       fmt.Sprintf("msg-%s-%d", r.threadID, len(corpus)),
		Role:     pb.Role_ROLE_USER,
		Content:  adapter.TextToProto(content),
		Position: int64(len(corpus)),
		ThreadId: r.threadID,
	}
	if err := r.db.InsertMessage(userMsg); err != nil {
		return nil, err
	}

	// Run through ADK
	msg := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{{Text: content}},
	}

	var lastEvent *session.Event
	for event, err := range r.adkRunner.Run(ctx, "user", r.threadID, msg, adkagent.RunConfig{}) {
		if err != nil {
			log.Printf("adk event error: %v", err)
			continue
		}
		lastEvent = event
	}

	// Extract assistant response from last event
	if lastEvent != nil && lastEvent.LLMResponse.Content != nil {
		protoMsg := adapter.GenaiContentToProto(lastEvent.LLMResponse.Content)
		assistantMsg := &pb.Message{
			Id:       fmt.Sprintf("msg-%s-%d", r.threadID, len(corpus)+1),
			Role:     pb.Role_ROLE_ASSISTANT,
			Content:  protoMsg.Content,
			Position: int64(len(corpus) + 1),
			ThreadId: r.threadID,
		}
		if err := r.db.InsertMessage(assistantMsg); err != nil {
			return nil, err
		}
		return assistantMsg, nil
	}

	return nil, fmt.Errorf("no response from agent")
}

// afterModelCallback extracts thinking blocks for carry-forward after every LLM call.
func (r *Runner) afterModelCallback(
	ctx adkagent.CallbackContext,
	llmResponse *model.LLMResponse,
	llmResponseError error,
) (*model.LLMResponse, error) {
	if llmResponseError != nil || llmResponse == nil || llmResponse.Content == nil {
		return llmResponse, llmResponseError
	}

	thinking := adapter.ExtractThinkingFromGenai(llmResponse.Content)
	if len(thinking) > 0 {
		r.engine.CarryForward(ctx, &pb.CarryForwardInput{
			EventId:        fmt.Sprintf("cf-%s", r.threadID),
			ThreadId:       r.threadID,
			ThinkingBlocks: thinking,
		})
	}

	return llmResponse, nil
}

// SetMode sets the agent's current mode.
func (r *Runner) SetMode(m Mode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode = m
}

// Engine exposes the runner's engine for fork/merge operations.
func (r *Runner) Engine() *rrc.Engine {
	return r.engine
}
