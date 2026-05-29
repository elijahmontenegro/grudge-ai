package tools

import (
	"strings"
	"testing"
)

// requiredToolDescriptions aliases AllToolNames (defined in
// descriptions.go) so test failures point at the source-of-truth list
// when adding a new tool without its .adoc file.
var requiredToolDescriptions = AllToolNames

// TestAllToolDescriptionsLoad enforces that every tool name in
// requiredToolDescriptions has a .adoc file under descriptions/ that
// loads successfully and produces a non-empty body.
//
// Adding a new tool: append its name here and create
// service/agent/tools/descriptions/<kebab-name>.adoc.
func TestAllToolDescriptionsLoad(t *testing.T) {
	for _, name := range requiredToolDescriptions {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("descriptionFor(%q) panicked: %v", name, r)
				}
			}()
			d := descriptionFor(name)
			if d == "" {
				t.Fatalf("descriptionFor(%q) returned empty body", name)
			}
		})
	}
}

// TestToolDescriptionsAreSubstantive enforces that descriptions carry
// real coaching, not stub placeholders. The threshold is intentionally
// low (50 chars) — it catches obviously-thin entries without prescribing
// length. The smallest legitimate descriptions in the Claude Code prior
// art (e.g. "Get the description of a task by ID") run ≥60 chars.
func TestToolDescriptionsAreSubstantive(t *testing.T) {
	const minLen = 50
	for _, name := range requiredToolDescriptions {
		t.Run(name, func(t *testing.T) {
			d := descriptionFor(name)
			trimmed := strings.TrimSpace(d)
			if len(trimmed) < minLen {
				t.Errorf("descriptionFor(%q) is %d chars; minimum %d to count as substantive coaching",
					name, len(trimmed), minLen)
			}
		})
	}
}
