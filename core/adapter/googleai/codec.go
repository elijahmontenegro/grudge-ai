package googleai

import (
	"strings"

	llmv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/llm/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
)

type generateRequest struct {
	Contents         []content         `json:"contents"`
	SystemInstruct   *content          `json:"systemInstruction,omitempty"`
	GenerationConfig *generationConfig `json:"generationConfig,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text string `json:"text,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens *int32   `json:"maxOutputTokens,omitempty"`
	Temperature     *float32 `json:"temperature,omitempty"`
	TopP            *float32 `json:"topP,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

type generateResponse struct {
	Candidates []candidate   `json:"candidates"`
	UsageMeta  usageMetadata `json:"usageMetadata"`
}

type candidate struct {
	Content content `json:"content"`
}

type usageMetadata struct {
	PromptTokenCount     int32 `json:"promptTokenCount"`
	CandidatesTokenCount int32 `json:"candidatesTokenCount"`
}

type embedContentRequest struct {
	Content content `json:"content"`
}

type embedContentResponse struct {
	Embedding embedValues `json:"embedding"`
}

type embedValues struct {
	Values []float32 `json:"values"`
}

type batchEmbedRequest struct {
	Requests []embedContentRequest `json:"requests"`
}

type batchEmbedResponse struct {
	Embeddings []embedValues `json:"embeddings"`
}

// --- helpers ---

func toGenerateRequest(req *llmv1.CompletionRequest) generateRequest {
	var sysContent *content
	var contents []content

	for _, m := range req.Messages {
		if m.Role == threadv1.Role_ROLE_SYSTEM {
			text := textFromBlocks(m.Content)
			sysContent = &content{Parts: []part{{Text: text}}}
			continue
		}
		role := "user"
		if m.Role == threadv1.Role_ROLE_ASSISTANT {
			role = "model"
		}
		parts := make([]part, 0, len(m.Content))
		for _, b := range m.Content {
			if t := b.GetText(); t != nil {
				parts = append(parts, part{Text: t.Text})
			}
		}
		contents = append(contents, content{Role: role, Parts: parts})
	}

	var genCfg *generationConfig
	if req.MaxTokens != nil || req.Temperature != nil || req.TopP != nil || len(req.Stop) > 0 {
		genCfg = &generationConfig{
			MaxOutputTokens: req.MaxTokens,
			Temperature:     req.Temperature,
			TopP:            req.TopP,
			StopSequences:   req.Stop,
		}
	}

	return generateRequest{
		Contents:         contents,
		SystemInstruct:   sysContent,
		GenerationConfig: genCfg,
	}
}

func fromGeminiParts(parts []part) []*threadv1.ContentBlock {
	blocks := make([]*threadv1.ContentBlock, 0, len(parts))
	for _, p := range parts {
		if p.Text != "" {
			blocks = append(blocks, &threadv1.ContentBlock{Block: &threadv1.ContentBlock_Text{Text: &threadv1.TextContent{Text: p.Text}}})
		}
	}
	return blocks
}

func textFromBlocks(blocks []*threadv1.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if t := b.GetText(); t != nil {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}
