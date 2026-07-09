package rrc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
)

const localContextSerializationVersion = "local-context-v1"

// SerializedLocalContextChunk is one scorer-sized piece of the serialized Local Context.
type SerializedLocalContextChunk struct {
	Index int
	Text  string
}

// SerializedLocalContext is the deterministic scorer form of bounded
// Local Context. It is transient: the ordered source messages remain the
// canonical record, while Fingerprint identifies this exact serialization
// for score persistence and audit.
type SerializedLocalContext struct {
	EventID     string
	Fingerprint string
	MessageIDs  []string
	Chunks      []SerializedLocalContextChunk
}

// BuildActiveDiscourse returns the in-flight TURN RECORD: every message
// stored under currentTurnID (the triggering event plus the model/tool
// events it has spawned so far), in corpus order, built FORWARD as the
// turn advances — and unlike a fixed last-N window it does not truncate: a
// 40-step tool loop keeps all 40 steps, and the triggering event is never
// pushed out.
//
// The turn record is the DELIVERY set (TurnDelivery — the model must see
// its own turn whole, tools included). Local Context proper — the semantic
// discourse that queries and anchors — is its SemanticMessages projection;
// tools are turn record, never discourse.
//
// The turn is the bounding unit. Nothing reaches backward out of it: prior
// turns already spent their selections producing this one, and a
// referential fragment ("yes, do that") is disambiguated by the system's
// own mechanisms — retrieval (similarity plus banked provenance mass) and,
// at the next model call, the model's thinking restating the referent —
// never by grabbing a positional neighbor. A lone user question is a
// complete, valid Local Context. (An earlier reachBackForAnchors imported
// the nearest prior user/assistant message when the turn lacked an
// "anchor pair"; that backward positional grab is a rejected anti-pattern
// — it blended off-topic neighbors into the query and measurably
// suppressed true prerequisites below the acceptance floor.)
//
// One fallback where turn identity is unavailable — currentTurnID == ""
// or no message of the turn stored yet (an autonomous tick's first model
// call): the bounded last-N recency window, the discourse being continued
// when there is no new item.
func BuildActiveDiscourse(threadCorpus []*threadv1.Message, currentTurnID string, fallbackN int) []*threadv1.Message {
	if len(threadCorpus) == 0 {
		return nil
	}
	if currentTurnID == "" {
		return BuildLocalContext(threadCorpus, fallbackN)
	}

	// The active turn's messages are a contiguous suffix of the corpus
	// (they are the most recently stored). Find the first index that
	// carries currentTurnID; the window runs from there to the end.
	start := -1
	for i, m := range threadCorpus {
		if m.TurnId == currentTurnID {
			start = i
			break
		}
	}
	if start < 0 {
		// No message of this turn is stored yet (e.g. autonomous tick,
		// first model call). Fall back to the recency window.
		return BuildLocalContext(threadCorpus, fallbackN)
	}

	return append([]*threadv1.Message(nil), threadCorpus[start:]...)
}

// SemanticMessages projects a message window to its semantic members —
// those carrying user/assistant text or thinking (co-equal; see
// hasSemanticBlock). This is the Local Context projection of a turn
// record: the discourse that queries and anchors. Tool-only steps (and
// image/attachment-only messages) are turn record — delivered, excluded
// from re-retrieval, provenance-banked — but not discourse.
func SemanticMessages(msgs []*threadv1.Message) []*threadv1.Message {
	var out []*threadv1.Message
	for _, m := range msgs {
		if hasSemanticBlock(m.Content) {
			out = append(out, m)
		}
	}
	return out
}

// BuildProvenanceSpine returns the message IDs of the immediately preceding
// turn in the window — the provenance walk's entry into the recorded graph.
// A fresh turn's own messages have no incoming provenance edges (edges are
// recorded contributor → anchor at generation time, i.e. after), so a
// turn-only walk seed finds nothing at the trigger call and the mass lift
// could neither act nor ever calibrate (replay reconstructs trigger-call
// cones). The spine is the discourse state this turn continues: seeding the
// walk with it lets the fresh turn INHERIT the influence its thread already
// banked — the prior turn's spent selections, flowing forward through
// recorded edges — instead of re-deriving them.
//
// The spine is a GRAPH-WALK seed and nothing else: it contributes no query
// chunk (dilution is structurally impossible), is not delivered, is not
// excluded from retrieval (prior-turn messages stay cosine candidates on
// merit), and is not a provenance contributor. Turn-shaped by design — the
// turn is the discourse unit, not a message count. Legacy rows without turn
// identity form no spine; with no current turn id (the recency-window
// fallback) the window already spans prior turns and needs no spine.
func BuildProvenanceSpine(window []*threadv1.Message, currentTurnID string) []string {
	if currentTurnID == "" {
		return nil
	}
	spineTurn := ""
	for i := len(window) - 1; i >= 0; i-- {
		m := window[i]
		if m.TurnId == "" || m.TurnId == currentTurnID {
			continue
		}
		spineTurn = m.TurnId
		break
	}
	if spineTurn == "" {
		return nil
	}
	var ids []string
	for _, m := range window {
		if m.TurnId == spineTurn {
			ids = append(ids, m.Id)
		}
	}
	return ids
}

// BuildLocalContext returns the bounded same-thread discourse ending at
// the latest stored event: the last N messages, nothing more. It is the
// no-turn-identity fallback (autonomous continuation with no new item) —
// a recency window over the discourse being continued, never a reach for
// disambiguation.
func BuildLocalContext(threadCorpus []*threadv1.Message, n int) []*threadv1.Message {
	if len(threadCorpus) == 0 || n <= 0 {
		return nil
	}
	start := len(threadCorpus) - n
	if start < 0 {
		start = 0
	}
	return append([]*threadv1.Message(nil), threadCorpus[start:]...)
}

// SerializeLocalContext serializes Local Context in discourse order with
// explicit role and block labels, chunking PER MESSAGE for the scorer: each
// semantic message is its own query unit, and a chunk never spans two
// messages. Q is a span of independent items — the selection aggregator
// max-merges per candidate over these chunks, so the one message that
// actually bears the query dominates a candidate's score instead of being
// blended into a size-window with unrelated neighbors (the measured
// dilution: a recall question fused with an off-topic prior answer scored
// the true prerequisite 0.46 vs 0.84 solo, below the acceptance floor).
//
// Chunk indices are GLOBAL and monotonic across the whole serialization:
// the score cache and persisted scores key on (fingerprint, chunk index),
// and chunk.Split restarts its index per message — reusing it would
// collide message-0-chunk-0 with message-1-chunk-0 and silently corrupt
// scoring. A message with no semantic content (tool/image/attachment-only)
// contributes its id to MessageIDs — membership: delivered, excluded from
// re-retrieval, provenance-banked — but no query chunk. Message IDs
// participate in the fingerprint but are not shown to the scorer.
//
// currentTurnID discriminates the in-flight turn inside the window: when
// set, only that turn's semantic messages become query chunks — the
// window-tail (the delivered preceding turn) contributes membership ids
// and nothing else, because prior turns already spent their selections
// and their text in the query is the reach-back dilution reborn. ""
// means the whole span is the discourse (the no-turn-identity recency
// fallback, tests, benches) and reproduces the undiscriminated behavior
// byte-for-byte.
func SerializeLocalContext(local []*threadv1.Message, currentTurnID string, cfg chunk.Config) *SerializedLocalContext {
	if len(local) == 0 {
		return nil
	}
	ids := make([]string, 0, len(local))
	var serializedChunks []SerializedLocalContextChunk
	next := 0
	for _, m := range local {
		ids = append(ids, m.Id)
		if currentTurnID != "" && m.TurnId != currentTurnID {
			continue
		}
		text := serializeSemanticMessage(m)
		if strings.TrimSpace(text) == "" {
			continue
		}
		rawChunks := chunk.Split(text, cfg)
		if len(rawChunks) == 0 {
			serializedChunks = append(serializedChunks, SerializedLocalContextChunk{Index: next, Text: text})
			next++
			continue
		}
		for _, c := range rawChunks {
			serializedChunks = append(serializedChunks, SerializedLocalContextChunk{Index: next, Text: c.Text})
			next++
		}
	}
	if len(serializedChunks) == 0 {
		return nil
	}

	h := sha256.New()
	fmt.Fprintf(h, "version:%s\n", localContextSerializationVersion)
	for _, id := range ids {
		fmt.Fprintf(h, "message:%s\n", id)
	}
	for _, c := range serializedChunks {
		fmt.Fprintf(h, "chunk:%d\n%s\n", c.Index, c.Text)
	}
	fingerprint := hex.EncodeToString(h.Sum(nil))
	anchor := local[len(local)-1]
	return &SerializedLocalContext{
		EventID:     "sel-" + anchor.Id,
		Fingerprint: fingerprint,
		MessageIDs:  ids,
		Chunks:      serializedChunks,
	}
}

// serializeSemanticMessage is the semantic-only projection of a message for
// the Local Context QUERY: user/assistant text and thinking, through one
// uniform path — nothing else. Text and thinking are co-equal by design;
// thinking is the model's own reasoning, the richest disambiguation signal
// in the system, and no path may privilege one semantic block type over
// another. Tool calls/results, images, and attachments are turn record
// (delivered to the model, excluded from re-retrieval, provenance-banked)
// but never query material: whatever mattered about them re-enters the
// discourse through the model's thinking and response, which ARE serialized
// here. Returns "" for a message with no semantic content. Candidate
// indexing keeps the full-fidelity SerializeMessageForScoring below — only
// the query is discourse-shaped.
func serializeSemanticMessage(m *threadv1.Message) string {
	var body strings.Builder
	for _, b := range m.Content {
		switch {
		case b.GetText() != nil && b.GetText().Text != "":
			fmt.Fprintf(&body, "[text]\n%s\n", b.GetText().Text)
		case b.GetThinking() != nil && b.GetThinking().Text != "":
			fmt.Fprintf(&body, "[thinking]\n%s\n", b.GetThinking().Text)
		}
	}
	if body.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("[message role=%s]\n%s[/message]\n", roleLabel(m.Role), body.String())
}

// SerializeMessageForScoring is the role-aware, block-aware serialization
// used for candidate indexing (every stored message, all block types).
// The stored message remains the lossless source of truth.
func SerializeMessageForScoring(m *threadv1.Message) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[message role=%s]\n", roleLabel(m.Role))
	for _, b := range m.Content {
		switch {
		case b.GetText() != nil:
			fmt.Fprintf(&sb, "[text]\n%s\n", b.GetText().Text)
		case b.GetThinking() != nil:
			fmt.Fprintf(&sb, "[thinking]\n%s\n", b.GetThinking().Text)
		case b.GetToolCall() != nil:
			tc := b.GetToolCall()
			fmt.Fprintf(&sb, "[tool_call id=%s name=%s]\n%s\n", tc.Id, tc.Name, tc.Arguments)
		case b.GetToolResult() != nil:
			tr := b.GetToolResult()
			fmt.Fprintf(&sb, "[tool_result tool_call_id=%s error=%t]\n%s\n", tr.ToolCallId, tr.IsError, tr.Content)
		case b.GetImage() != nil:
			im := b.GetImage()
			fmt.Fprintf(&sb, "[image media_type=%s bytes=%d]\n", im.MediaType, len(im.Data))
		case b.GetAttachment() != nil:
			a := b.GetAttachment()
			fmt.Fprintf(&sb, "[attachment filename=%s mime_type=%s path=%s]\n%s\n",
				a.Filename, a.MimeType, a.Path, a.InlinedText)
		}
	}
	sb.WriteString("[/message]\n")
	return sb.String()
}

func roleLabel(role threadv1.Role) string {
	switch role {
	case threadv1.Role_ROLE_USER:
		return "user"
	case threadv1.Role_ROLE_ASSISTANT:
		return "assistant"
	case threadv1.Role_ROLE_SYSTEM:
		return "system"
	default:
		return "unspecified"
	}
}

// hasSemanticBlock reports whether blocks carry any semantic content —
// non-empty user/assistant text or non-empty thinking. Semantic content is
// what anchors and queries Local Context; tool blocks, images, and
// attachments are turn record, not discourse. Text and thinking are
// deliberately co-equal: a thinking-only assistant step is a full semantic
// anchor (thinking is the primary disambiguation signal), and no path may
// privilege one semantic block type over another.
func hasSemanticBlock(blocks []*threadv1.ContentBlock) bool {
	for _, b := range blocks {
		if t := b.GetText(); t != nil && t.Text != "" {
			return true
		}
		if th := b.GetThinking(); th != nil && th.Text != "" {
			return true
		}
	}
	return false
}
