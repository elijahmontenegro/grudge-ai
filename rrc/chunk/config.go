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

	// Estimator is the token estimator every consumer of this config
	// uses. Required for chunking and budget sizing; DefaultConfig
	// leaves it nil (the library does not choose a tokenizer for you)
	// and Estimate panics on first use if the consumer never set one —
	// an explicit contract instead of a silent 0-token estimator that
	// would let every budget check pass.
	Estimator TokenEstimator
}

// Estimate counts tokens in s using the config's estimator. Panics
// when no estimator is set — callers have a silent-units problem
// otherwise.
func (c Config) Estimate(s string) int {
	if c.Estimator == nil {
		panic("chunk: no TokenEstimator on Config — set Config.Estimator at startup (e.g. with rrc/tiktoken)")
	}
	return c.Estimator.Estimate(s)
}

// DefaultConfig: paragraph-friendly, cross-encoder-safe.
func DefaultConfig() Config {
	return Config{
		MaxChars:     2000,
		OverlapChars: 200,
	}
}
