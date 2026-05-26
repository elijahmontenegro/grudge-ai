package chunk

import "strings"

// Split splits text into chunks. Short text (≤MaxChars) returns one
// chunk. Longer text walks forward in MaxChars windows, backing each
// end to the nearest natural boundary (paragraph > line > sentence >
// word). Subsequent chunks start OverlapChars earlier than the
// previous chunk ended so context bridges.
func Split(text string, cfg Config) []Chunk {
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
