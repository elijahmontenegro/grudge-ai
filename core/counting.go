package core

import (
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
)

// A counting projection extracts, from one wire message, the text its
// adapter's codec will actually send to the provider. Codecs send
// different subsets of a message's content blocks (some drop thinking,
// some send text only), so budget sizing that counts the full content
// systematically overstates the prompt for those providers and sheds
// context for nothing. Each adapter registers its projection beside
// its provider factory — the projection is part of the codec's
// contract and lives with it, so a codec change updates the count in
// the same package or fails its own tests.
var countProjectionRegistry = map[string]func(*llmv1.LLMMessage) string{}

// RegisterCountProjection associates an adapter name with its counting
// projection. Called from each adapter's init(), like RegisterProvider.
func RegisterCountProjection(name string, proj func(*llmv1.LLMMessage) string) {
	if name == "" || proj == nil {
		return
	}
	countProjectionRegistry[name] = proj
}

// CountProjection returns the counting projection registered for the
// adapter, or nil when the adapter registered none — callers treat nil
// as "count everything", which is exact only for codecs that resend
// every block type.
func CountProjection(adapter string) func(*llmv1.LLMMessage) string {
	return countProjectionRegistry[adapter]
}
