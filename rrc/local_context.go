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

// BuildActiveDiscourse returns the active reasoning path for the current
// turn: every message stored under currentTurnID (the triggering event
// plus the model/tool events it has spawned so far), in corpus order. This
// is the RRC-correct notion of Local Context — the in-flight discourse the
// continuation is interpreted from — and unlike a fixed last-N window it
// does not truncate: a 40-step tool loop keeps all 40 steps as local
// discourse, and the triggering event is never pushed out.
//
// Two fallbacks preserve behavior where turn identity is unavailable:
//   - currentTurnID == "" (an autonomous tick's first model call,
//     before any event of the tick is stored): fall back to the bounded
//     last-N window.
//   - the turn's messages are found but lack a user-text or assistant-text
//     anchor: reach back for the minimum anchor pair (shared with
//     BuildLocalContext) so the selector input is a disambiguating span,
//     not a lone ambiguous block. Everything else older stays a candidate,
//     not auto-included.
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

	window := append([]*threadv1.Message(nil), threadCorpus[start:]...)
	return append(reachBackForAnchors(threadCorpus, start, window), window...)
}

// BuildLocalContext returns the bounded same-thread discourse ending at
// the latest stored event. The last N messages form the base. If that
// base lacks the latest user-text or assistant-text anchor, the builder
// reaches back for that anchor without pulling the intervening turn.
func BuildLocalContext(threadCorpus []*threadv1.Message, n int) []*threadv1.Message {
	if len(threadCorpus) == 0 || n <= 0 {
		return nil
	}
	start := len(threadCorpus) - n
	if start < 0 {
		start = 0
	}
	window := append([]*threadv1.Message(nil), threadCorpus[start:]...)
	return append(reachBackForAnchors(threadCorpus, start, window), window...)
}

// reachBackForAnchors returns the prefix of prior messages needed so the
// window contains at least one user-text and one assistant-text anchor,
// scanning backward from before `start` and pulling only the missing
// anchors (not the intervening turns). Shared by BuildLocalContext and
// BuildActiveDiscourse. Returns a fresh slice in corpus order.
func reachBackForAnchors(threadCorpus []*threadv1.Message, start int, window []*threadv1.Message) []*threadv1.Message {
	haveUser, haveAssistant := false, false
	for _, m := range window {
		if !hasSemanticBlock(m.Content) {
			continue
		}
		haveUser = haveUser || m.Role == threadv1.Role_ROLE_USER
		haveAssistant = haveAssistant || m.Role == threadv1.Role_ROLE_ASSISTANT
	}

	var prefix []*threadv1.Message
	for i := start - 1; i >= 0 && (!haveUser || !haveAssistant); i-- {
		m := threadCorpus[i]
		if !hasSemanticBlock(m.Content) {
			continue
		}
		switch m.Role {
		case threadv1.Role_ROLE_USER:
			if !haveUser {
				prefix = append([]*threadv1.Message{m}, prefix...)
				haveUser = true
			}
		case threadv1.Role_ROLE_ASSISTANT:
			if !haveAssistant {
				prefix = append([]*threadv1.Message{m}, prefix...)
				haveAssistant = true
			}
		}
	}
	return prefix
}

// SerializeLocalContext serializes Local Context in discourse order with
// explicit role and block labels, then chunks that serialization for the
// scorer. Message IDs participate in the fingerprint but are not shown
// to the scorer.
func SerializeLocalContext(local []*threadv1.Message, cfg chunk.Config) *SerializedLocalContext {
	if len(local) == 0 {
		return nil
	}
	var serialized strings.Builder
	ids := make([]string, 0, len(local))
	for _, m := range local {
		ids = append(ids, m.Id)
		serialized.WriteString(serializeSemanticMessage(m))
	}
	text := serialized.String()
	if strings.TrimSpace(text) == "" {
		return nil
	}

	rawChunks := chunk.Split(text, cfg)
	serializedChunks := make([]SerializedLocalContextChunk, 0, len(rawChunks))
	if len(rawChunks) == 0 {
		serializedChunks = append(serializedChunks, SerializedLocalContextChunk{Index: 0, Text: text})
	} else {
		for _, c := range rawChunks {
			serializedChunks = append(serializedChunks, SerializedLocalContextChunk{Index: c.Index, Text: c.Text})
		}
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
