package rrc

// EngineConfig holds tunable parameters for the RRC engine.
type EngineConfig struct {
	EdgeThreshold float64 // Edge creation threshold (default: 0.5)
	WeightCE      float64 // Cross-encoder weight (default: 0.7)
	WeightQUD     float64 // QUD weight (default: 0.2)
	WeightTemp    float64 // Temporal proximity weight (default: 0.1)
	ScoreFloor    float64 // Traversal cutoff (default: 0.01)
}

// DefaultConfig returns the default engine configuration with spec-derived values.
func DefaultConfig() EngineConfig {
	return EngineConfig{
		EdgeThreshold: 0.5,
		WeightCE:      0.7,
		WeightQUD:     0.2,
		WeightTemp:    0.1,
		ScoreFloor:    0.01,
	}
}
