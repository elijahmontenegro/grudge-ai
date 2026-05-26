package rrc

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/rrc/chunk"
)

// Assemble produces a wire-ready prompt for the Query message under
// the engine's protocol. Single-call entry point: OnMessage + Select
// + MMR + Radius + rectification + budget shed in one library call.
//
// Inputs:
//   - Query: the query message; must already exist in Corpus.
//   - Corpus: scope-loaded message list (thread-only or all-threads).
//   - ThreadCorpus: thread-only corpus used for the Radius slice; may
//     equal Corpus when Scope is thread-scoped.
//   - System: optional fixed-cost system prompt slot at wire head.
//   - Resolver: rectification-rule pool. When nil, rules don't fire
//     and the wire is sent as Selected+Radius only.
//   - Rules: rectification rules to run with the resolver.
//   - Budget / HeadroomPct / PerMsgDelim / FixedTokens: budget knobs.
//     FixedTokens is a codec-specific overhead the consumer
//     pre-computes (e.g. tool-schema JSON serialized to the provider
//     wire format); kept opaque to the engine.
//   - ExcludeIDs: pre-shed Selected ids the consumer wants suppressed
//     from the start (typical use: provider rejected an earlier
//     wire with context-window-exceeded; consumer adds the
//     prior round's lowest-score to the exclude list and re-calls).
//
// Returns the wire (ready to forward to a Completer), the post-MMR
// Selected list, the edges OnMessage emitted (consumer persists),
// the ids shed during budget walk, and per-stage telemetry.
//
// The shed loop drops the lowest-scored Selected entry or
// rectification insert until the wire fits the effective budget.
// System and Radius are fixed cost; if they alone exceed budget,
// Assemble returns the wire it built (overflowing) and the consumer
// surfaces the provider's eventual error.
func (e *Engine) Assemble(ctx context.Context, req AssembleRequest) (AssembleResult, error) {
	if req.Query == nil {
		return AssembleResult{}, fmt.Errorf("assemble: nil Query")
	}

	e.mu.Lock()
	edges, onMsgTel, err := e.OnMessage(ctx, req.Query, req.Corpus)
	if err != nil {
		e.mu.Unlock()
		return AssembleResult{}, fmt.Errorf("assemble OnMessage: %w", err)
	}
	selectStart := time.Now()
	selResult, err := e.Select(req.Query.Id, req.Scope, req.ThreadID)
	if err != nil {
		e.mu.Unlock()
		return AssembleResult{}, fmt.Errorf("assemble Select: %w", err)
	}
	selectMs := time.Since(selectStart).Milliseconds()
	cfg := e.cfg
	var mmrMs int64
	if cfg.DiversityLambda > 0 && cfg.DiversityLambda < 1 && len(selResult.Selected) > 1 {
		mmrStart := time.Now()
		mmrRanked, mmrErr := e.ApplyMMR(ctx, selResult.Selected, cfg.DiversityLambda)
		mmrMs = time.Since(mmrStart).Milliseconds()
		if mmrErr != nil {
			log.Printf("RRC: MMR rerank skipped query=%s: %v", req.Query.Id, mmrErr)
		} else {
			selResult.Selected = mmrRanked
		}
	}
	e.mu.Unlock()
	shedStart := time.Now()

	selectedScoreByID := make(map[string]float64, len(selResult.Selected))
	for _, s := range selResult.Selected {
		selectedScoreByID[s.MessageId] = float64(s.EffectiveScore)
	}

	dropped := make(map[string]bool, len(req.ExcludeIDs))
	for _, id := range req.ExcludeIDs {
		dropped[id] = true
	}

	effectiveBudget := req.Budget
	if req.HeadroomPct > 0 && req.HeadroomPct <= 1 {
		effectiveBudget = int(float64(req.Budget) * req.HeadroomPct)
	}

	var (
		finalWire     []*pb.LLMMessage
		finalSelected []*pb.LLMMessage
		finalRadius   []*pb.LLMMessage
		finalRectify  int
		finalTotal    int
	)

	for {
		selectedIDs := make(map[string]bool)
		for _, s := range selResult.Selected {
			if !dropped[s.MessageId] {
				selectedIDs[s.MessageId] = true
			}
		}
		var selectedMsgs []*pb.LLMMessage
		var wireIDs []string
		selectedPtrToID := make(map[*pb.LLMMessage]string)
		for _, msg := range req.Corpus {
			if selectedIDs[msg.Id] {
				llm := messageToLLM(msg)
				selectedMsgs = append(selectedMsgs, llm)
				selectedPtrToID[llm] = msg.Id
				wireIDs = append(wireIDs, msg.Id)
			}
		}

		var radiusMsgs []*pb.LLMMessage
		if cfg.RadiusSize > 0 && len(req.ThreadCorpus) > 0 {
			radiusMsgs = buildRadiusSlice(req.ThreadCorpus, selectedIDs, cfg.RadiusSize, &wireIDs)
		}

		var llmMsgs []*pb.LLMMessage
		if req.System != nil {
			llmMsgs = append(llmMsgs, req.System)
		}
		llmMsgs = append(llmMsgs, selectedMsgs...)
		llmMsgs = append(llmMsgs, radiusMsgs...)

		insertScores := map[*pb.LLMMessage]float64{}
		if req.Resolver != nil && len(req.Rules) > 0 {
			req.Resolver.ExcludeIDs(wireIDs)
			rectified, scores, aerr := Apply(llmMsgs, req.Resolver, req.Rules...)
			if aerr != nil {
				log.Printf("RRC: rectification failed query=%s: %v — pre-rectification wire", req.Query.Id, aerr)
			} else {
				llmMsgs = rectified
				insertScores = scores
			}
		}

		msgTokens := make([]int, len(llmMsgs))
		total := 0
		for i, m := range llmMsgs {
			msgTokens[i] = chunk.EstimateTokens(TextFromBlocks(m.Content))
			total += msgTokens[i]
		}
		total += req.FixedTokens
		total += len(llmMsgs) * req.PerMsgDelim

		if req.Budget <= 0 || total <= effectiveBudget {
			finalWire = llmMsgs
			finalSelected = selectedMsgs
			finalRadius = radiusMsgs
			finalRectify = len(insertScores)
			finalTotal = total
			break
		}

		dropRectByPtr := make(map[*pb.LLMMessage]bool)
		var dropSelectedID string
		for total > effectiveBudget {
			var bestPtr *pb.LLMMessage
			bestScore := math.Inf(1)
			bestIdx := -1
			for i, m := range llmMsgs {
				if dropRectByPtr[m] {
					continue
				}
				var s float64
				if rs, ok := insertScores[m]; ok {
					s = rs
				} else if id, ok := selectedPtrToID[m]; ok {
					s = selectedScoreByID[id]
				} else {
					continue
				}
				if s < bestScore {
					bestScore = s
					bestPtr = m
					bestIdx = i
				}
			}
			if bestPtr == nil {
				break
			}
			if id, ok := selectedPtrToID[bestPtr]; ok {
				dropSelectedID = id
				break
			}
			dropRectByPtr[bestPtr] = true
			total -= msgTokens[bestIdx] + req.PerMsgDelim
		}

		if dropSelectedID != "" {
			log.Printf("RRC: shed selected msg=%s (score=%.3f)", dropSelectedID, selectedScoreByID[dropSelectedID])
			dropped[dropSelectedID] = true
			continue
		}

		if len(dropRectByPtr) > 0 {
			log.Printf("RRC: shed %d rectification insert(s) to fit budget", len(dropRectByPtr))
			filtered := make([]*pb.LLMMessage, 0, len(llmMsgs))
			for _, m := range llmMsgs {
				if dropRectByPtr[m] {
					continue
				}
				filtered = append(filtered, m)
			}
			llmMsgs = filtered
		}

		finalWire = llmMsgs
		finalSelected = selectedMsgs
		finalRadius = radiusMsgs
		finalRectify = len(insertScores) - len(dropRectByPtr)
		finalTotal = total
		break
	}

	shedIDs := make([]string, 0, len(dropped))
	for id := range dropped {
		shedIDs = append(shedIDs, id)
	}

	shedMs := time.Since(shedStart).Milliseconds()

	return AssembleResult{
		Wire:      finalWire,
		Selection: selResult,
		Edges:     edges,
		Shed:      shedIDs,
		Telemetry: AssembleTelemetry{
			SelectedCount:   len(finalSelected),
			RadiusCount:     len(finalRadius),
			RectifiedCount:  finalRectify,
			SheddedCount:    len(shedIDs),
			TotalTokens:     finalTotal,
			EffectiveBudget: effectiveBudget,
			OnMessage:       onMsgTel,
			SelectMs:        selectMs,
			MMRMs:           mmrMs,
			ShedMs:          shedMs,
		},
	}, nil
}

// buildRadiusSlice produces the chronological Radius window with
// dynamical-anchor reachback. Last N thread messages are taken; if
// the most-recent user-text or assistant-text isn't in that window,
// reach further back to anchor it. Selection-included ids are
// skipped (Selection trumps Radius). Output preserves chronological
// order. wireIDs is appended in-place so the caller can seed the
// resolver with everything currently in the wire.
func buildRadiusSlice(threadCorpus []*pb.Message, selectedIDs map[string]bool, radiusN int, wireIDs *[]string) []*pb.LLMMessage {
	start := len(threadCorpus) - radiusN
	if start < 0 {
		start = 0
	}
	window := threadCorpus[start:]

	haveUserText, haveAssistantText := false, false
	for _, m := range window {
		if !hasTextBlock(m.Content) {
			continue
		}
		switch m.Role {
		case pb.Role_ROLE_USER:
			haveUserText = true
		case pb.Role_ROLE_ASSISTANT:
			haveAssistantText = true
		}
	}

	var prepended []*pb.Message
	if !haveUserText || !haveAssistantText {
		for i := start - 1; i >= 0 && (!haveUserText || !haveAssistantText); i-- {
			m := threadCorpus[i]
			if !hasTextBlock(m.Content) {
				continue
			}
			if m.Role == pb.Role_ROLE_USER && !haveUserText {
				prepended = append([]*pb.Message{m}, prepended...)
				haveUserText = true
			} else if m.Role == pb.Role_ROLE_ASSISTANT && !haveAssistantText {
				prepended = append([]*pb.Message{m}, prepended...)
				haveAssistantText = true
			}
		}
	}

	if len(prepended) > 0 {
		log.Printf("RRC: dynamical Radius reached back for %d semantic anchor(s) beyond the last %d",
			len(prepended), radiusN)
	}

	var out []*pb.LLMMessage
	emit := func(m *pb.Message) {
		if selectedIDs[m.Id] {
			return
		}
		out = append(out, messageToLLM(m))
		*wireIDs = append(*wireIDs, m.Id)
	}
	for _, m := range prepended {
		emit(m)
	}
	for _, m := range window {
		emit(m)
	}
	return out
}

// hasTextBlock reports whether a content-block list contains at
// least one non-empty text block. Used by the Radius slice builder
// to distinguish semantic-content messages (user prose, assistant
// replies) from tool-plumbing messages (tool_call / tool_result /
// thinking-only). Anchoring on text blocks keeps a deep tool loop
// from hiding the most-recent real conversation turn.
//
// Thinking is excluded — the model's internal monologue is not a
// conversational anchor.
func hasTextBlock(blocks []*pb.ContentBlock) bool {
	for _, b := range blocks {
		if t := b.GetText(); t != nil && t.Text != "" {
			return true
		}
	}
	return false
}

// messageToLLM lifts a stored Message into the wire-format
// LLMMessage. The role and content blocks pass through unchanged;
// storage-only fields (id, position, threadId, timestamps) drop.
func messageToLLM(msg *pb.Message) *pb.LLMMessage {
	return &pb.LLMMessage{
		Role:    msg.Role,
		Content: msg.Content,
	}
}

// AssembleRequest carries inputs to Engine.Assemble. See Assemble
// for field semantics.
type AssembleRequest struct {
	Query        *pb.Message
	Corpus       []*pb.Message
	ThreadCorpus []*pb.Message
	Scope        pb.SelectionScope
	ThreadID     string
	System       *pb.LLMMessage
	Resolver     excludingResolver
	Rules        []Rule
	Budget       int
	HeadroomPct  float64
	PerMsgDelim  int
	FixedTokens int
	ExcludeIDs   []string
}

// AssembleResult carries outputs from Engine.Assemble. See Assemble
// for field semantics. Selection carries the full Select() output
// so the consumer can publish it for introspection (Selected +
// Excluded + scope) — the engine's view of RRC's prerequisite
// detection, distinct from the budget-driven shed list.
type AssembleResult struct {
	Wire      []*pb.LLMMessage
	Selection *pb.SelectionResult
	Edges     []*pb.Edge
	Shed      []string
	Telemetry AssembleTelemetry
}

// AssembleTelemetry reports the assembly's per-stage signals — both
// the byte/count totals (selected, radius, rectified, shed) and the
// per-stage wall-clock decomposition (OnMessage, Select, MMR, Shed).
// Callers aggregating into higher-level profiling (per-tick traces,
// structured logs) read the timing fields directly without
// re-instrumenting the engine.
type AssembleTelemetry struct {
	SelectedCount   int
	RadiusCount     int
	RectifiedCount  int
	SheddedCount    int
	TotalTokens     int
	EffectiveBudget int

	// Per-stage timings (milliseconds). Sum across stages plus engine
	// overhead approximates the wall-clock duration of Assemble.
	OnMessage OnMessageTelemetry // includes DurationMs + per-call counters
	SelectMs  int64              // graph walk + transitive reduction
	MMRMs     int64              // diversity rerank (0 when DiversityLambda disables it)
	ShedMs    int64              // budget shed loop + token estimation + radius build
}

// excludingResolver augments the rule-pipeline Resolver with an
// ExcludeIDs seed call invoked by Assemble before each shed
// iteration's rule pass. The seed prevents rules from re-fetching
// any message already contributing to the wire under its original
// id.
type excludingResolver interface {
	Resolver
	ExcludeIDs(ids []string)
}
