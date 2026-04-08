package adapter

import (
	"context"
	"fmt"
	"iter"

	"github.com/emontenegr/spidey/core"
	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc"
	"github.com/emontenegr/spidey/service/storage"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// RRCLLM implements model.LLM. ADK calls this thinking it's an LLM;
// RRC intercepts transparently, selects prerequisites, forwards to the
// real LLM, and feeds carry-forward back.
// StreamCallback is called for each streaming delta so the service can
// publish to GraphQL subscriptions.
type StreamCallback func(delta, thinking string, done bool)

type RRCLLM struct {
	engine    *rrc.Engine
	completer core.Completer
	db        *storage.DB
	threadID  string
	modelName string
	OnStream  StreamCallback // set by service to publish deltas
}

// NewRRCLLM creates an RRC-as-LLM adapter.
func NewRRCLLM(engine *rrc.Engine, completer core.Completer, db *storage.DB, threadID, modelName string) *RRCLLM {
	return &RRCLLM{
		engine:    engine,
		completer: completer,
		db:        db,
		threadID:  threadID,
		modelName: modelName,
	}
}

func (r *RRCLLM) Name() string { return r.modelName }

// GenerateContent implements model.LLM. This is the RRC interception point.
// ADK sends the full conversation; RRC selects prerequisites and forwards
// only those to the real LLM.
func (r *RRCLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// Convert genai contents to proto messages for RRC scoring
		var protoMsgs []*pb.Message
		for i, c := range req.Contents {
			llmMsg := GenaiContentToProto(c)
			protoMsgs = append(protoMsgs, &pb.Message{
				Id:       fmt.Sprintf("adk-%s-%d", r.threadID, i),
				Role:     llmMsg.Role,
				Content:  llmMsg.Content,
				Position: int64(i),
				ThreadId: r.threadID,
			})
		}

		if len(protoMsgs) == 0 {
			yield(nil, fmt.Errorf("empty request"))
			return
		}

		prompt := protoMsgs[len(protoMsgs)-1]

		// Score the prompt against predecessors
		if len(protoMsgs) > 1 {
			r.engine.OnMessage(ctx, prompt, protoMsgs[:len(protoMsgs)-1])
		}

		// Select prerequisites
		result, err := r.engine.Select(prompt.Id, pb.SelectionScope_SELECTION_SCOPE_THREAD, r.threadID)
		if err != nil {
			yield(nil, fmt.Errorf("rrc select: %w", err))
			return
		}

		// Build selected content list
		selectedIDs := make(map[string]bool)
		for _, s := range result.Selected {
			selectedIDs[s.MessageId] = true
		}

		var selectedContents []*genai.Content
		for i, msg := range protoMsgs {
			if selectedIDs[msg.Id] || msg.Id == prompt.Id {
				selectedContents = append(selectedContents, req.Contents[i])
			}
		}
		if len(selectedContents) == 0 {
			selectedContents = req.Contents[len(req.Contents)-1:]
		}

		// Build proto CompletionRequest for the real LLM
		var llmMsgs []*pb.LLMMessage
		for _, c := range selectedContents {
			llmMsgs = append(llmMsgs, GenaiContentToProto(c))
		}

		protoReq := &pb.CompletionRequest{
			Messages: llmMsgs,
			Model:    req.Model,
			Stream:   stream,
		}
		if req.Config != nil {
			if req.Config.Temperature != nil {
				t := float32(*req.Config.Temperature)
				protoReq.Temperature = &t
			}
			if req.Config.TopP != nil {
				t := float32(*req.Config.TopP)
				protoReq.TopP = &t
			}
			if req.Config.MaxOutputTokens > 0 {
				t := req.Config.MaxOutputTokens
				protoReq.MaxTokens = &t
			}
		}

		if stream {
			ch, err := r.completer.Stream(ctx, protoReq)
			if err != nil {
				yield(nil, err)
				return
			}

			var fullText string
			for chunk := range ch {
				resp := &model.LLMResponse{Partial: true}
				if t := chunk.GetText(); t != nil {
					fullText += t.Text
					resp.Content = &genai.Content{
						Role:  "model",
						Parts: []*genai.Part{{Text: t.Text}},
					}
					if r.OnStream != nil {
						r.OnStream(t.Text, "", false)
					}
				}
				if t := chunk.GetThinking(); t != nil {
					resp.Content = &genai.Content{
						Role:  "model",
						Parts: []*genai.Part{{Text: t.Text, Thought: true}},
					}
					if r.OnStream != nil {
						r.OnStream("", t.Text, false)
					}
				}
				if chunk.Done {
					resp.Partial = false
					resp.TurnComplete = true
					if chunk.Error != nil {
						resp.ErrorMessage = *chunk.Error
					}
					if r.OnStream != nil {
						r.OnStream("", "", true)
					}
				}
				if !yield(resp, nil) {
					return
				}
			}

			// Carry-forward from thinking blocks in the full response
			r.carryForward(ctx, result.EventId, fullText)

		} else {
			protoResp, err := r.completer.Complete(ctx, protoReq)
			if err != nil {
				yield(nil, err)
				return
			}

			genaiContent := ProtoResponseToGenai(protoResp)
			resp := &model.LLMResponse{
				Content:      genaiContent,
				TurnComplete: true,
			}

			// Carry-forward
			thinking := ExtractThinkingFromGenai(genaiContent)
			if len(thinking) > 0 {
				r.engine.CarryForward(ctx, &pb.CarryForwardInput{
					EventId:        result.EventId,
					ThreadId:       r.threadID,
					ThinkingBlocks: thinking,
				})
			}

			yield(resp, nil)
		}
	}
}

func (r *RRCLLM) carryForward(ctx context.Context, eventID, _ string) {
	// Thinking blocks are extracted during streaming and fed back
	// The full carry-forward happens in the AfterModelCallback plugin
	_ = eventID
}
