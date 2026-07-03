package rrc

import (
	"fmt"
	"sort"

	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
)

// DeliveryGroup is an atomic restoration unit. Root is the message that
// Local Context or Selection admitted; Messages also contains every exact
// protocol counterpart required to send Root through the provider API.
type DeliveryGroup struct {
	RootID   string
	RootIDs  []string
	Score    float64
	Messages []*pb.Message
}

// ProtocolIndex resolves relational protocol structure from the Store.
// Counterparts are keyed by their original tool-call IDs and thread; no
// relevance score or identifier rewriting participates in closure.
type ProtocolIndex struct {
	callByKey   map[string]*pb.Message
	resultByKey map[string]*pb.Message
	ambiguous   map[string]bool
}

func NewProtocolIndex(corpus []*pb.Message) *ProtocolIndex {
	p := &ProtocolIndex{
		callByKey:   make(map[string]*pb.Message),
		resultByKey: make(map[string]*pb.Message),
		ambiguous:   make(map[string]bool),
	}
	for _, m := range corpus {
		for _, b := range m.Content {
			if tc := b.GetToolCall(); tc != nil && tc.Id != "" {
				key := protocolKey(m.ThreadId, tc.Id)
				if prior := p.callByKey[key]; prior != nil && prior.Id != m.Id {
					p.ambiguous["call\x00"+key] = true
				}
				p.callByKey[key] = m
			}
			if tr := b.GetToolResult(); tr != nil && tr.ToolCallId != "" {
				key := protocolKey(m.ThreadId, tr.ToolCallId)
				if prior := p.resultByKey[key]; prior != nil && prior.Id != m.Id {
					p.ambiguous["result\x00"+key] = true
				}
				p.resultByKey[key] = m
			}
		}
	}
	return p
}

// CloseGroup computes exact transitive protocol closure for root. A
// missing counterpart is an integrity error: silently dropping root would
// discard gate-passed prerequisite context, while substitution would bind
// unrelated historical operations.
func (p *ProtocolIndex) CloseGroup(root *pb.Message, score float64) (DeliveryGroup, error) {
	if root == nil {
		return DeliveryGroup{}, fmt.Errorf("protocol closure: nil root")
	}
	seen := map[string]bool{root.Id: true}
	queue := []*pb.Message{root}
	messages := []*pb.Message{root}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, b := range current.Content {
			var counterpart *pb.Message
			var relation string
			switch {
			case b.GetToolCall() != nil:
				tc := b.GetToolCall()
				key := protocolKey(current.ThreadId, tc.Id)
				if p.ambiguous["result\x00"+key] {
					return DeliveryGroup{}, fmt.Errorf(
						"protocol closure: tool call %s has multiple results in thread %s",
						tc.Id, current.ThreadId,
					)
				}
				counterpart = p.resultByKey[key]
				relation = "tool result"
			case b.GetToolResult() != nil:
				tr := b.GetToolResult()
				key := protocolKey(current.ThreadId, tr.ToolCallId)
				if p.ambiguous["call\x00"+key] {
					return DeliveryGroup{}, fmt.Errorf(
						"protocol closure: tool result %s has multiple calls in thread %s",
						tr.ToolCallId, current.ThreadId,
					)
				}
				counterpart = p.callByKey[key]
				relation = "tool call"
			default:
				continue
			}
			if counterpart == nil {
				return DeliveryGroup{}, fmt.Errorf(
					"protocol closure: message %s requires exact %s in thread %s",
					current.Id, relation, current.ThreadId,
				)
			}
			if !seen[counterpart.Id] {
				seen[counterpart.Id] = true
				messages = append(messages, counterpart)
				queue = append(queue, counterpart)
			}
		}
	}

	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].ThreadId != messages[j].ThreadId {
			return messages[i].ThreadId < messages[j].ThreadId
		}
		return messages[i].Position < messages[j].Position
	})
	return DeliveryGroup{
		RootID: root.Id, RootIDs: []string{root.Id},
		Score: score, Messages: messages,
	}, nil
}

func protocolKey(threadID, callID string) string {
	return threadID + "\x00" + callID
}
