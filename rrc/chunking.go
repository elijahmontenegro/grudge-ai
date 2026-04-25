package rrc

import "strings"

// Chunk is a contiguous slice of a message's text. Chunks are the
// unit of embedding and cross-encoder scoring. Messages remain the
// graph unit — chunks aggregate back to message-level edges via
// max-score merging. Protocol §6.5 explicitly allows message-level or
// paragraph-level granularity; we chose paragraph-aware chunking so
// bge-reranker can focus its 8192-token window on the specific
// sub-section of a long reference document that matches a query
// instead of being silently truncated to the document's intro.
type Chunk struct {
	Index     int    // position within the message's chunk list (0-based)
	Text      string // the chunk's text
	ByteStart int    // byte offset into the original message text
	ByteEnd   int    // exclusive
	TokenEst  int    // rough estimate (chars/4)
}

// ChunkConfig controls chunk size and overlap.
type ChunkConfig struct {
	// MaxChars is the hard cap per chunk. Chosen so a pair of chunks
	// (query + candidate) still fits inside an 8192-token model window
	// with wide headroom: 2000 chars ≈ 500 tokens, so a pair is ~1000
	// tokens, 12% of the model's limit.
	MaxChars int
	// OverlapChars preserves context across chunk boundaries so a
	// reference split between two chunks can still score against a
	// query that matches the bridging content.
	OverlapChars int
}

// DefaultChunkConfig: paragraph-friendly, cross-encoder-safe.
func DefaultChunkConfig() ChunkConfig {
	return ChunkConfig{
		MaxChars:     2000,
		OverlapChars: 200,
	}
}

// ChunkText splits text into chunks. Short text (≤MaxChars) returns
// one chunk. Longer text walks forward in MaxChars windows, backing
// each end to the nearest natural boundary (paragraph > line >
// sentence > word). Subsequent chunks start OverlapChars earlier than
// the previous chunk ended so context bridges.
func ChunkText(text string, cfg ChunkConfig) []Chunk {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = 2000
	}
	if cfg.OverlapChars < 0 {
		cfg.OverlapChars = 0
	}
	if cfg.OverlapChars >= cfg.MaxChars {
		cfg.OverlapChars = cfg.MaxChars / 4
	}

	if len(text) <= cfg.MaxChars {
		return []Chunk{{
			Index:     0,
			Text:      text,
			ByteStart: 0,
			ByteEnd:   len(text),
			TokenEst:  estimateTokens(text),
		}}
	}

	var chunks []Chunk
	i := 0
	for i < len(text) {
		end := i + cfg.MaxChars
		if end >= len(text) {
			end = len(text)
		} else {
			// Back off to a natural boundary in the second half of the
			// window — far enough back that we don't strand most of the
			// chunk, close enough that we don't cut mid-sentence.
			end = findBoundary(text, i, end, cfg.MaxChars/2)
		}
		piece := strings.TrimSpace(text[i:end])
		if piece != "" {
			chunks = append(chunks, Chunk{
				Index:     len(chunks),
				Text:      piece,
				ByteStart: i,
				ByteEnd:   end,
				TokenEst:  estimateTokens(piece),
			})
		}
		if end >= len(text) {
			break
		}
		// Forward by (MaxChars - OverlapChars) to create overlap.
		next := end - cfg.OverlapChars
		if next <= i {
			// Guarantee forward progress when overlap would stall us —
			// happens if a single tiny boundary region immediately
			// follows i and overlap swallows all of it.
			next = i + 1
		}
		i = next
	}
	return chunks
}

// findBoundary searches backward from `maxEnd` for the last natural
// break point (paragraph, line, sentence-terminator, whitespace),
// within a window of `window` chars. Returns `maxEnd` if none found —
// the caller accepts a mid-word cut as a last resort rather than
// overflowing the chunk size.
func findBoundary(text string, start, maxEnd, window int) int {
	minEnd := maxEnd - window
	if minEnd < start {
		minEnd = start
	}
	if minEnd >= maxEnd {
		return maxEnd
	}
	slice := text[minEnd:maxEnd]
	// Paragraph (double newline): strongest break.
	if idx := strings.LastIndex(slice, "\n\n"); idx >= 0 {
		return minEnd + idx + 2
	}
	// Single newline.
	if idx := strings.LastIndex(slice, "\n"); idx >= 0 {
		return minEnd + idx + 1
	}
	// Sentence-terminator followed by space.
	for _, term := range []string{". ", "! ", "? ", ".\t", "!\t", "?\t"} {
		if idx := strings.LastIndex(slice, term); idx >= 0 {
			return minEnd + idx + len(term)
		}
	}
	// Whitespace.
	if idx := strings.LastIndexAny(slice, " \t"); idx >= 0 {
		return minEnd + idx + 1
	}
	return maxEnd
}

// TokenEstimator counts tokens in a string under some tokenization
// scheme. Implementations are goroutine-safe. Consumers who don't
// install one get the panic-on-call defaultEstimator below — making
// the contract explicit instead of silently running with a 0-token
// estimator that would let every budget check pass.
//
// Default implementation lives at rrc/tiktoken (cl100k_base BPE) but
// is opt-in: importing rrc does not pull tiktoken-go into the
// consumer's binary. Consumers wire one via SetDefaultEstimator at
// boot, or pass one through EngineConfig.Estimator (Move B).
type TokenEstimator interface {
	Estimate(s string) int
}

// defaultEstimator is consulted by the package-level EstimateTokens
// helper. nil at package init; library consumers MUST set it via
// SetDefaultEstimator before any code that estimates tokens runs
// (chunking, budget sizing).
var defaultEstimator TokenEstimator

// SetDefaultEstimator installs a TokenEstimator as the package-level
// default. The standard library/main pattern: import rrc/tiktoken at
// boot and call rrc.SetDefaultEstimator(tiktoken.New()).
func SetDefaultEstimator(e TokenEstimator) {
	defaultEstimator = e
}

// estimateTokens delegates to the default estimator. Panics if none
// is installed — callers have a silent-units problem otherwise.
func estimateTokens(s string) int {
	if defaultEstimator == nil {
		panic("rrc: no TokenEstimator installed — call rrc.SetDefaultEstimator(...) at startup (e.g. with rrc/tiktoken)")
	}
	return defaultEstimator.Estimate(s)
}

// EstimateTokens exposes the package-level estimator for callers
// outside this package. The assembler uses it for context-budget
// sizing. Panics if no estimator is installed.
func EstimateTokens(s string) int { return estimateTokens(s) }
