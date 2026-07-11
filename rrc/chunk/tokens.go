package chunk

// TokenEstimator counts tokens in a string under some tokenization
// scheme. Implementations are goroutine-safe. It is carried on Config
// (not a package global) so every consumer of a config — chunking,
// budget sizing, wire estimation — shares one explicit estimator.
//
// Default implementation lives at rrc/tiktoken (cl100k_base BPE) but
// is opt-in: importing chunk does not pull tiktoken-go into the
// consumer's binary.
type TokenEstimator interface {
	Estimate(s string) int
}
