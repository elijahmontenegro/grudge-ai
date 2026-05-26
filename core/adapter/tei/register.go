package tei

import (
	"strings"

	"github.com/emontenegr/spidey/core"
)

// queryPrefixForModel returns the canonical query-side instruction prefix
// for asymmetric instruction-tuned embedders. The prefix is part of the
// query text Qwen3-Embedding expects; TEI doesn't have per-request prompt
// support, so the client embeds it directly. For symmetric models like
// bge-m3 returns "" and EmbedQuery routes identically to EmbedDocument.
//
// The Spidey-specific task description is what makes this useful for RRC
// — it tells the model the retrieval is "find the prior message containing
// the prerequisite info this follow-up depends on", not generic web search.
func queryPrefixForModel(model string) string {
	switch {
	case strings.HasPrefix(model, "Qwen/Qwen3-Embedding"),
		strings.HasPrefix(model, "Qwen3-Embedding"):
		return "Instruct: Given a follow-up message in a conversation, " +
			"retrieve the prior message that contains the prerequisite information " +
			"needed to answer it\nQuery: "
	default:
		return ""
	}
}

func init() {
	core.RegisterProvider("tei", func(cfg core.ProviderConfig) (any, error) {
		return New(Config{
			BaseURL:     cfg.BaseURL,
			QueryPrefix: queryPrefixForModel(cfg.Model),
		}), nil
	})
}
