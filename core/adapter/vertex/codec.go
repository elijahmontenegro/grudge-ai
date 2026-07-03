package vertex

import (
	"encoding/json"

	"github.com/elijahmontenegro/grudge/core/genaicodec"
	pb "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/v1"
	"google.golang.org/genai"
)

// encode converts a pb.CompletionRequest into the genai SDK's (contents,
// config) pair. The Content/Part mapping is shared via core/genaicodec; this
// adds the request-level concerns genaicodec doesn't cover: hoisting the
// system message out of the turn list (genai has no system role — it travels
// via GenerateContentConfig.SystemInstruction), generation params, and tool
// declarations.
func (c *completer) encode(req *pb.CompletionRequest) ([]*genai.Content, *genai.GenerateContentConfig) {
	cfg := &genai.GenerateContentConfig{}

	var contents []*genai.Content
	for _, m := range req.Messages {
		if m.Role == pb.Role_ROLE_SYSTEM {
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

	return contents, cfg
}
