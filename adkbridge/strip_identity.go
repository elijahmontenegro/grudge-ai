package adkbridge

import (
	"fmt"
	"strings"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
)

// StripADKIdentity returns a BeforeModelCallback that removes the identity
// sentence ADK's identityRequestProcessor appends to SystemInstruction.
//
// ADK's processor (internal/llminternal/identity_request_processor.go) injects
//
//	You are an agent. Your internal name is "<name>". The description about you is "<desc>".
//
// (the description clause is omitted when Description is empty) after the
// composed Instruction. That trailing assertion collides with the persona
// framing in identity.adoc — the framework's "You are an agent" wording and
// duplicated name reference contradict the composed system prompt.
//
// The system prompt the model sees must be exactly what the prompt assembler
// produced. This callback runs after all RequestProcessors and just before the
// model call, scrubbing ADK's contribution from req.Config.SystemInstruction
// while leaving the composed prompt intact.
func StripADKIdentity(agentName, agentDescription string) llmagent.BeforeModelCallback {
	stamp := fmt.Sprintf("You are an agent. Your internal name is %q.", agentName)
	if agentDescription != "" {
		stamp += fmt.Sprintf(" The description about you is %q.", agentDescription)
	}

	return func(_ agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
		if req == nil || req.Config == nil || req.Config.SystemInstruction == nil {
			return nil, nil
		}
		for _, part := range req.Config.SystemInstruction.Parts {
			if part == nil || part.Text == "" {
				continue
			}
			// AppendInstructions joins with "\n\n" when the prior Part is
			// non-empty (utils.AppendInstructions). Identity is appended
			// after the Instruction, so the typical shape is
			// "<composed>\n\n<stamp>". Strip both joined and bare forms,
			// then tidy any stray trailing newlines.
			part.Text = strings.ReplaceAll(part.Text, "\n\n"+stamp, "")
			part.Text = strings.ReplaceAll(part.Text, stamp, "")
			part.Text = strings.TrimRight(part.Text, "\n")
		}
		return nil, nil
	}
}
