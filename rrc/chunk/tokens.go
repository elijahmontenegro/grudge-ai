package chunk

// TokenEstimator counts tokens in a string under some tokenization
// scheme. Implementations are goroutine-safe. Consumers who don't
// install one get the panic-on-call estimateTokens below — making the
// contract explicit instead of silently running with a 0-token
// estimator that would let every budget check pass.
//
// Default implementation lives at rrc/tiktoken (cl100k_base BPE) but
// is opt-in: importing chunk does not pull tiktoken-go into the
// consumer's binary. Consumers wire one via SetDefaultEstimator at
// boot.
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
// boot and call chunk.SetDefaultEstimator(tiktoken.New()).
func SetDefaultEstimator(e TokenEstimator) {
	defaultEstimator = e
}

// estimateTokens delegates to the default estimator. Panics if none
// is installed — callers have a silent-units problem otherwise.
func estimateTokens(s string) int {
	if defaultEstimator == nil {
		panic("chunk: no TokenEstimator installed — call chunk.SetDefaultEstimator(...) at startup (e.g. with rrc/tiktoken)")
	}
	return defaultEstimator.Estimate(s)
}

// EstimateTokens exposes the package-level estimator for callers
// outside this package. The assembler uses it for context-budget
// sizing. Panics if no estimator is installed.
func EstimateTokens(s string) int { return estimateTokens(s) }
