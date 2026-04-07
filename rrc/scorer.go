package rrc

// ScoreCache holds cross-encoder pairwise dependency scores. Global across all
// threads. Messages are immutable so cached scores never become stale.
type ScoreCache struct {
	scores map[[2]string]float64 // [from_id, to_id] -> cross-encoder score
}

func newScoreCache() *ScoreCache {
	return &ScoreCache{
		scores: make(map[[2]string]float64),
	}
}

// Set records a cross-encoder score for the (from, to) pair.
func (sc *ScoreCache) Set(fromID, toID string, score float64) {
	sc.scores[[2]string{fromID, toID}] = score
}

// Get retrieves the cached score. Returns 0, false if not cached.
func (sc *ScoreCache) Get(fromID, toID string) (float64, bool) {
	s, ok := sc.scores[[2]string{fromID, toID}]
	return s, ok
}

// FuseScore computes the fused score for an edge given the three signal components.
// score = w1*ceScore + w2*qudWeight + w3*temporalProximity
func FuseScore(cfg EngineConfig, ceScore, qudWeight, temporalProximity float64) float64 {
	return cfg.WeightCE*ceScore + cfg.WeightQUD*qudWeight + cfg.WeightTemp*temporalProximity
}

// TemporalProximity computes 1/(1+distance) where distance is the absolute
// position difference between two messages. Hyperbolic decay: adjacent ~0.5,
// 9 apart ~0.1, 99 apart ~0.01.
func TemporalProximity(posA, posB int64) float64 {
	d := posA - posB
	if d < 0 {
		d = -d
	}
	return 1.0 / (1.0 + float64(d))
}

// All returns the full score map for serialization/persistence.
func (sc *ScoreCache) All() map[[2]string]float64 {
	return sc.scores
}
