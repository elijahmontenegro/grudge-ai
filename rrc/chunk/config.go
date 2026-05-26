package chunk

// Config controls chunk size and overlap.
type Config struct {
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

// DefaultConfig: paragraph-friendly, cross-encoder-safe.
func DefaultConfig() Config {
	return Config{
		MaxChars:     2000,
		OverlapChars: 200,
	}
}
