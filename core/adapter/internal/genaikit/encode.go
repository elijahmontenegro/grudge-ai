package genaikit

import (
	"encoding/json"
	"fmt"

	"github.com/elijahmontenegro/grudge/core/genaicodec"
	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"google.golang.org/genai"
)

// encode converts a llmv1.CompletionRequest into the genai SDK's (contents,
// config) pair. The Content/Part mapping is shared via core/genaicodec; this
// adds the request-level concerns genaicodec doesn't cover: hoisting the
// system message out of the turn list (genai has no system role — it travels
// via GenerateContentConfig.SystemInstruction), generation params, tool
// declarations, and tool choice.
func (c *Completer) encode(req *llmv1.CompletionRequest) ([]*genai.Content, *genai.GenerateContentConfig, error) {
	cfg := &genai.GenerateContentConfig{}

	var contents []*genai.Content
	for _, m := range req.Messages {
		if m.Role == threadv1.Role_ROLE_SYSTEM {
			// Hoist system content into SystemInstruction rather than a turn.
			cfg.SystemInstruction = genaicodec.ProtoToContent(m)
			continue
		}
		contents = append(contents, genaicodec.ProtoToContent(m))
	}

	if req.MaxTokens != nil {
		cfg.MaxOutputTokens = *req.MaxTokens
	}
	if req.Temperature != nil {
		cfg.Temperature = req.Temperature
	}
	if req.TopP != nil {
		cfg.TopP = req.TopP
	}
	if len(req.Stop) > 0 {
		cfg.StopSequences = req.Stop
	}

	if len(req.Tools) > 0 {
		decls := make([]*genai.FunctionDeclaration, 0, len(req.Tools))
		for _, t := range req.Tools {
			fd := &genai.FunctionDeclaration{Name: t.Name, Description: t.Description}
			// ParametersJsonSchema accepts a parsed JSON-Schema object
			// directly (any), so no *genai.Schema translation is needed.
			if t.ParametersJson != "" {
				var schema any
				if err := json.Unmarshal([]byte(t.ParametersJson), &schema); err == nil {
					fd.ParametersJsonSchema = schema
				}
			}
			decls = append(decls, fd)
		}
		cfg.Tools = []*genai.Tool{{FunctionDeclarations: decls}}
	}

	toolConfig, err := toToolConfig(req.ToolChoice)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", c.Name, err)
	}
	cfg.ToolConfig = toolConfig

	return contents, cfg, nil
}

// toToolConfig maps the proto ToolChoice to genai's ToolConfig. Unset
// or UNSPECIFIED omits the field — the model decides, Gemini's own
// default. NAMED maps to ANY + AllowedFunctionNames (Gemini has no
// single-tool-forced mode; ANY restricted to one name is the
// equivalent); an empty tool name can't be encoded and is refused
// rather than silently sent as AUTO.
func toToolConfig(tc *llmv1.ToolChoice) (*genai.ToolConfig, error) {
	if tc == nil || tc.Mode == llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_UNSPECIFIED {
		return nil, nil
	}
	fcc := &genai.FunctionCallingConfig{}
	switch tc.Mode {
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_AUTO:
		fcc.Mode = genai.FunctionCallingConfigModeAuto
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NONE:
		fcc.Mode = genai.FunctionCallingConfigModeNone
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_REQUIRED:
		fcc.Mode = genai.FunctionCallingConfigModeAny
	case llmv1.ToolChoiceMode_TOOL_CHOICE_MODE_NAMED:
		if tc.NamedTool == "" {
			return nil, fmt.Errorf("tool_choice NAMED requires a non-empty named_tool")
		}
		fcc.Mode = genai.FunctionCallingConfigModeAny
		fcc.AllowedFunctionNames = []string{tc.NamedTool}
	default:
		return nil, fmt.Errorf("unknown tool_choice mode %v", tc.Mode)
	}
	return &genai.ToolConfig{FunctionCallingConfig: fcc}, nil
}
