package rrc

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/proto/pbtext"
)

// Assemble scores one bounded Local Context, selects deep-history
// prerequisites, applies exact protocol closure, and emits a provider-ready
// wire. Local Context and its required closure have priority over selected
// history; selected roots and their closure are shed atomically.
func (e *Engine) Assemble(ctx context.Context, req AssembleRequest) (AssembleResult, error) {
	if req.Anchor == nil {
		return AssembleResult{}, fmt.Errorf("assemble: nil Anchor")
	}
	if len(req.LocalContext) == 0 {
		return AssembleResult{}, fmt.Errorf("assemble: empty LocalContext")
	}
	if req.LocalContext[len(req.LocalContext)-1].Id != req.Anchor.Id {
		return AssembleResult{}, fmt.Errorf("assemble: Anchor must end LocalContext")
	}

	effectiveBudget := req.Budget
	if req.HeadroomPct > 0 && req.HeadroomPct <= 1 {
		effectiveBudget = int(float64(req.Budget) * req.HeadroomPct)
	}
	// Bounded protocol index for Local Context closure: Local Context plus the
	// messages sharing its turns, where any tool-call/result counterparts live.
	protocolLocal, err := e.protocolScope(req.LocalContext, req.Store)
	if err != nil {
		return AssembleResult{}, fmt.Errorf("assemble: local protocol scope: %w", err)
	}

	local := append([]*threadv1.Message(nil), req.LocalContext...)
	pinned := pinnedLocalIDs(local)
	var localGroups []DeliveryGroup
	var localWire []*llmv1.LLMMessage
	for {
		groups, err := closeRoots(protocolLocal, local, nil)
		if err != nil {
			return AssembleResult{}, err
		}
		wire := groupsToWire(groups, nil)
		total := e.wireTokens(req.CountText, req.System, nil, wire, req.FixedTokens, req.PerMsgDelim)
		if req.Budget <= 0 || total <= effectiveBudget {
			localGroups, localWire = groups, wire
			break
		}
		drop := oldestUnpinned(local, pinned)
		if drop == "" {
			return AssembleResult{}, fmt.Errorf(
				"assemble: pinned Local Context requires %d tokens, budget is %d",
				total, effectiveBudget,
			)
		}
		local = removeMessage(local, drop)
	}

	serializedLocal := req.SerializedLocalContext
	if serializedLocal == nil || !sameIDs(serializedLocal.MessageIDs, messageIDs(local)) {
		serializedLocal = SerializeLocalContext(local, e.cfg.Chunk)
	}

	var (
		edges                 []*rrcv1.Edge
		prerequisiteSelection PrerequisiteSelectionTelemetry
		selected              *rrcv1.SelectionResult
		selectMs              int64
		mmrMs                 int64
	)
	switch {
	case req.PriorSelection != nil:
		// Overflow-retry reuse (A5): one outbound call owns one selector input
		// and one selection event. A context-overflow retry must NOT re-run
		// prerequisite selection — that would re-walk provenance, re-add DAG
		// edges, and re-emit the selection event. Instead it reuses the prior
		// selection verbatim and only re-runs shed-to-fit below with the
		// accumulated ExcludeIDs, shedding whole delivery groups. Edges were
		// already published on the first attempt, so PriorEdges is empty here.
		selected = req.PriorSelection
	case serializedLocal != nil:
		e.mu.Lock()
		var err error
		edges, prerequisiteSelection, err = e.selectPrerequisitesLocked(ctx, serializedLocal, req.Anchor, req.Scope, req.ThreadID)
		if err != nil {
			e.mu.Unlock()
			return AssembleResult{}, fmt.Errorf("assemble SelectPrerequisites: %w", err)
		}
		selectStart := time.Now()
		selected, err = e.selectLocked(req.Anchor.Id, req.Scope, req.ThreadID)
		selectMs = time.Since(selectStart).Milliseconds()
		if err != nil {
			e.mu.Unlock()
			return AssembleResult{}, fmt.Errorf("assemble Select: %w", err)
		}
		selected.EventId = serializedLocal.EventID
		selected.LocalContextFingerprint = serializedLocal.Fingerprint
		selected.LocalContextMessageIds = append([]string(nil), serializedLocal.MessageIDs...)
		selected.AnchorMessageId = req.Anchor.Id

		if e.cfg.DiversityLambda > 0 && e.cfg.DiversityLambda < 1 && len(selected.Selected) > 1 {
			mmrStart := time.Now()
			ranked, mmrErr := e.ApplyMMR(ctx, selected.Selected, e.cfg.DiversityLambda)
			mmrMs = time.Since(mmrStart).Milliseconds()
			if mmrErr != nil {
				e.logger.Warn("RRC: MMR rerank skipped", "localContext", serializedLocal.Fingerprint, "err", mmrErr)
			} else {
				selected.Selected = ranked
			}
		}
		e.mu.Unlock()
	default:
		selected = &rrcv1.SelectionResult{
			EventId:         "sel-" + req.Anchor.Id,
			Scope:           req.Scope,
			ThreadId:        req.ThreadID,
			AnchorMessageId: req.Anchor.Id,
		}
	}

	dropped := make(map[string]bool, len(req.ExcludeIDs))
	for _, id := range req.ExcludeIDs {
		dropped[id] = true
	}
	localIDs := make(map[string]bool)
	for _, g := range localGroups {
		for _, m := range g.Messages {
			localIDs[m.Id] = true
		}
	}
	// Selected content and protocol scope, both bounded: fetch the selected
	// messages' content by id, and build a protocol index over them plus their
	// turn peers (where the counterparts CloseGroup needs live).
	selectedIDs := make([]string, 0, len(selected.Selected))
	for _, s := range selected.Selected {
		selectedIDs = append(selectedIDs, s.MessageId)
	}
	corpusByID, err := req.Store.Messages(selectedIDs)
	if err != nil {
		return AssembleResult{}, fmt.Errorf("assemble: fetch selected content: %w", err)
	}
	selectedMsgs := make([]*threadv1.Message, 0, len(corpusByID))
	for _, m := range corpusByID {
		selectedMsgs = append(selectedMsgs, m)
	}
	protocolSel, err := e.protocolScope(selectedMsgs, req.Store)
	if err != nil {
		return AssembleResult{}, fmt.Errorf("assemble: selected protocol scope: %w", err)
	}

	shedStart := time.Now()
	var finalSelected []DeliveryGroup
	var finalWire []*llmv1.LLMMessage
	var total int
	for {
		var groups []DeliveryGroup
		for _, s := range selected.Selected {
			if dropped[s.MessageId] || localIDs[s.MessageId] {
				continue
			}
			root := corpusByID[s.MessageId]
			if root == nil {
				return AssembleResult{}, fmt.Errorf("assemble: selected message %s missing from corpus", s.MessageId)
			}
			group, err := protocolSel.CloseGroup(root, float64(s.EffectiveScore))
			if err != nil {
				return AssembleResult{}, err
			}
			if groupOverlaps(group, localIDs) {
				continue
			}
			groups = append(groups, group)
		}
		groups = mergeDeliveryGroups(groups)
		sort.SliceStable(groups, func(i, j int) bool {
			return corpusByID[groups[i].RootID].Position < corpusByID[groups[j].RootID].Position
		})

		selectedWire := groupsToWire(groups, localIDs)
		finalWire = make([]*llmv1.LLMMessage, 0, 1+len(selectedWire)+len(localWire))
		if req.System != nil {
			finalWire = append(finalWire, req.System)
		}
		finalWire = append(finalWire, selectedWire...)
		finalWire = append(finalWire, localWire...)
		total = e.wireTokens(req.CountText, nil, finalWire, nil, req.FixedTokens, req.PerMsgDelim)
		if req.Budget <= 0 || total <= effectiveBudget {
			finalSelected = groups
			break
		}
		drop, ok := lowestScoreGroup(groups)
		if !ok {
			return AssembleResult{}, fmt.Errorf(
				"assemble: fixed system and Local Context require %d tokens, budget is %d",
				total, effectiveBudget,
			)
		}
		for _, id := range drop.RootIDs {
			dropped[id] = true
		}
	}

	shedIDs := make([]string, 0, len(dropped))
	for id := range dropped {
		shedIDs = append(shedIDs, id)
	}
	sort.Strings(shedIDs)

	return AssembleResult{
		Wire:                   finalWire,
		Selection:              selected,
		SerializedLocalContext: serializedLocal,
		Edges:                  edges,
		Shed:                   shedIDs,
		Delivered:              finalSelected,
		LocalGroups:            localGroups,
		Telemetry: AssembleTelemetry{
			SelectedCount:         len(finalSelected),
			LocalContextCount:     len(local),
			ClosureCount:          closureCount(finalSelected) + closureCount(localGroups),
			SheddedCount:          len(shedIDs),
			TotalTokens:           total,
			EffectiveBudget:       effectiveBudget,
			PrerequisiteSelection: prerequisiteSelection,
			SelectMs:              selectMs,
			MMRMs:                 mmrMs,
			ShedMs:                time.Since(shedStart).Milliseconds(),
		},
	}, nil
}

// protocolScope builds a bounded ProtocolIndex over msgs plus every message
// sharing a turn with them — the set where their tool-call/result counterparts
// live — instead of indexing the whole corpus.
func (e *Engine) protocolScope(msgs []*threadv1.Message, store CorpusStore) (*ProtocolIndex, error) {
	if store == nil {
		return NewProtocolIndex(msgs), nil
	}
	peers, err := store.TurnPeers(msgs)
	if err != nil {
		return nil, err
	}
	all := make([]*threadv1.Message, 0, len(msgs)+len(peers))
	all = append(all, msgs...)
	all = append(all, peers...)
	return NewProtocolIndex(all), nil
}

// CorpusStore gives Assemble bounded, on-demand access to message content,
// replacing the full-corpus slice so per-step cost stops scaling with history.
// The only reads Assemble needs are the selected set's content and the
// protocol counterparts of the assembly set — both bounded.
type CorpusStore interface {
	// Messages returns the given messages by id (the selected set's content).
	Messages(ids []string) (map[string]*threadv1.Message, error)
	// TurnPeers returns every message sharing a (thread, turn) with any input
	// message — the bounded superset containing their tool-call/result
	// counterparts, since a call and its result share a turn.
	TurnPeers(msgs []*threadv1.Message) ([]*threadv1.Message, error)
}

type AssembleRequest struct {
	SerializedLocalContext *SerializedLocalContext
	Anchor                 *threadv1.Message
	Store                  CorpusStore
	LocalContext           []*threadv1.Message
	Scope                  threadv1.SelectionScope
	ThreadID               string
	System                 *llmv1.LLMMessage
	Budget                 int
	HeadroomPct            float64
	PerMsgDelim            int
	FixedTokens            int
	ExcludeIDs             []string

	// CountText overrides how a wire message's text is extracted for
	// budget counting. Providers' codecs send different subsets of a
	// message's content blocks (some drop thinking, some send text
	// only), so the caller injects the projection matching what its
	// adapter will actually put on the wire — counting content that
	// is never sent systematically overstates the prompt and sheds
	// context for nothing. Nil counts everything
	// (pbtext.TextFromBlocks), which is exact only for adapters that
	// resend all block types.
	CountText func(*llmv1.LLMMessage) string

	// PriorSelection, when set, makes Assemble reuse an earlier selection
	// verbatim instead of re-running prerequisite selection — the overflow-
	// retry reuse path (A5). One outbound model call owns one selector input
	// and one selection event; a context-overflow retry only re-runs
	// shed-to-fit (dropping whole delivery groups via ExcludeIDs), never
	// re-selection. Leave nil for the first attempt.
	PriorSelection *rrcv1.SelectionResult
}

type AssembleResult struct {
	Wire                   []*llmv1.LLMMessage
	Selection              *rrcv1.SelectionResult
	SerializedLocalContext *SerializedLocalContext
	Edges                  []*rrcv1.Edge
	Shed                   []string
	Telemetry              AssembleTelemetry

	// Delivered holds the selected delivery groups that survived
	// shed-to-fit — the structured form of what Wire flattened, so a
	// consumer can see which group each wire message belongs to.
	// LocalGroups holds the Local Context's protocol-closed groups.
	Delivered   []DeliveryGroup
	LocalGroups []DeliveryGroup
}

type AssembleTelemetry struct {
	SelectedCount         int
	LocalContextCount     int
	ClosureCount          int
	SheddedCount          int
	TotalTokens           int
	EffectiveBudget       int
	PrerequisiteSelection PrerequisiteSelectionTelemetry
	SelectMs              int64
	MMRMs                 int64
	ShedMs                int64
}

func closeRoots(index *ProtocolIndex, roots []*threadv1.Message, scores map[string]float64) ([]DeliveryGroup, error) {
	groups := make([]DeliveryGroup, 0, len(roots))
	for _, root := range roots {
		score := math.Inf(1)
		if scores != nil {
			score = scores[root.Id]
		}
		group, err := index.CloseGroup(root, score)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return mergeDeliveryGroups(groups), nil
}

func groupsToWire(groups []DeliveryGroup, already map[string]bool) []*llmv1.LLMMessage {
	seen := make(map[string]bool)
	for id := range already {
		seen[id] = true
	}
	var messages []*threadv1.Message
	for _, group := range groups {
		for _, m := range group.Messages {
			if seen[m.Id] {
				continue
			}
			seen[m.Id] = true
			messages = append(messages, m)
		}
	}
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].ThreadId != messages[j].ThreadId {
			return messages[i].ThreadId < messages[j].ThreadId
		}
		if messages[i].Position != messages[j].Position {
			return messages[i].Position < messages[j].Position
		}
		return messages[i].Id < messages[j].Id
	})
	out := make([]*llmv1.LLMMessage, 0, len(messages))
	for _, m := range messages {
		out = append(out, messageToLLM(m))
	}
	return out
}

func (e *Engine) wireTokens(countText func(*llmv1.LLMMessage) string, system *llmv1.LLMMessage, head, tail []*llmv1.LLMMessage, fixed, delim int) int {
	if countText == nil {
		countText = func(m *llmv1.LLMMessage) string { return pbtext.TextFromBlocks(m.Content) }
	}
	total := fixed
	if system != nil {
		head = append([]*llmv1.LLMMessage{system}, head...)
	}
	for _, m := range append(head, tail...) {
		total += e.cfg.Chunk.Estimate(countText(m)) + delim
	}
	return total
}

func pinnedLocalIDs(local []*threadv1.Message) map[string]bool {
	pinned := make(map[string]bool)
	if len(local) == 0 {
		return pinned
	}
	pinned[local[len(local)-1].Id] = true
	haveUser, haveAssistant := false, false
	for i := len(local) - 1; i >= 0 && (!haveUser || !haveAssistant); i-- {
		m := local[i]
		if !hasTextBlock(m.Content) {
			continue
		}
		if m.Role == threadv1.Role_ROLE_USER && !haveUser {
			pinned[m.Id], haveUser = true, true
		}
		if m.Role == threadv1.Role_ROLE_ASSISTANT && !haveAssistant {
			pinned[m.Id], haveAssistant = true, true
		}
	}
	return pinned
}

func oldestUnpinned(local []*threadv1.Message, pinned map[string]bool) string {
	for _, m := range local {
		if !pinned[m.Id] {
			return m.Id
		}
	}
	return ""
}

func removeMessage(messages []*threadv1.Message, id string) []*threadv1.Message {
	out := make([]*threadv1.Message, 0, len(messages)-1)
	for _, m := range messages {
		if m.Id != id {
			out = append(out, m)
		}
	}
	return out
}

func lowestScoreGroup(groups []DeliveryGroup) (DeliveryGroup, bool) {
	var selected DeliveryGroup
	found := false
	lowest := math.Inf(1)
	for _, g := range groups {
		if g.Score < lowest {
			lowest, selected, found = g.Score, g, true
		}
	}
	return selected, found
}

func mergeDeliveryGroups(groups []DeliveryGroup) []DeliveryGroup {
	byClosure := make(map[string]int, len(groups))
	out := make([]DeliveryGroup, 0, len(groups))
	for _, group := range groups {
		key := closureKey(group.Messages)
		if index, ok := byClosure[key]; ok {
			out[index].RootIDs = append(out[index].RootIDs, group.RootIDs...)
			if group.Score > out[index].Score {
				out[index].RootID = group.RootID
				out[index].Score = group.Score
			}
			continue
		}
		group.RootIDs = append([]string(nil), group.RootIDs...)
		byClosure[key] = len(out)
		out = append(out, group)
	}
	for i := range out {
		sort.Strings(out[i].RootIDs)
	}
	return out
}

func closureKey(messages []*threadv1.Message) string {
	var key string
	for _, m := range messages {
		key += m.ThreadId + "\x00" + m.Id + "\x00"
	}
	return key
}

func groupOverlaps(group DeliveryGroup, ids map[string]bool) bool {
	for _, m := range group.Messages {
		if ids[m.Id] {
			return true
		}
	}
	return false
}

func closureCount(groups []DeliveryGroup) int {
	n := 0
	for _, g := range groups {
		if len(g.Messages) > 1 {
			n += len(g.Messages) - 1
		}
	}
	return n
}

func messageIDs(messages []*threadv1.Message) []string {
	ids := make([]string, len(messages))
	for i, m := range messages {
		ids[i] = m.Id
	}
	return ids
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func messageToLLM(msg *threadv1.Message) *llmv1.LLMMessage {
	return &llmv1.LLMMessage{Role: msg.Role, Content: msg.Content}
}
