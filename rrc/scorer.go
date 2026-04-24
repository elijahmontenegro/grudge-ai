package rrc

// ScoreCache holds chunk-pair reranker scores in memory. Global across
// threads. Chunks are derived from immutable messages so cached scores
// never become stale; model changes add new rows under a new model_id
// at the persistence layer (the engine filters by currently-configured
// model). Scores are at chunk granularity because a single message
// contributes different relevance to different targets depending on
// which of its sections is being matched — the character bible's
// Marcus Webb chunk is highly relevant to chapters mentioning Webb
// and irrelevant to chapters focused elsewhere. Message-pair
// aggregation is the engine's concern; the cache just stores atoms.
type ScoreCache struct {
	scores  map[ScoreKey]float64
	persist ScorePersister
}

// ScoreKey identifies a chunk-to-chunk scoring pair.
type ScoreKey struct {
	FromMsgID    string
	FromChunkIdx int
	ToMsgID      string
	ToChunkIdx   int
}

// ScorePersister is the write-through hook called on every Set. A
// function (not an interface) so storage stays ignorant of rrc. The
// service wires it to storage.DB.InsertChunkScore at startup.
type ScorePersister func(fromID string, fromIdx int, toID string, toIdx int, score float64)

func newScoreCache() *ScoreCache {
	return &ScoreCache{scores: make(map[ScoreKey]float64)}
}

// SetPersister installs the write-through hook. nil disables
// persistence (used by tests or the startup pre-boot phase).
func (sc *ScoreCache) SetPersister(p ScorePersister) { sc.persist = p }

// Set records a score and fires the persister if configured.
func (sc *ScoreCache) Set(fromID string, fromIdx int, toID string, toIdx int, score float64) {
	k := ScoreKey{FromMsgID: fromID, FromChunkIdx: fromIdx, ToMsgID: toID, ToChunkIdx: toIdx}
	sc.scores[k] = score
	if sc.persist != nil {
		sc.persist(fromID, fromIdx, toID, toIdx, score)
	}
}

// Get returns the cached score for a chunk pair, or (0, false).
func (sc *ScoreCache) Get(fromID string, fromIdx int, toID string, toIdx int) (float64, bool) {
	s, ok := sc.scores[ScoreKey{FromMsgID: fromID, FromChunkIdx: fromIdx, ToMsgID: toID, ToChunkIdx: toIdx}]
	return s, ok
}

// loadSilent writes without firing the persister. Used for bulk
// startup loads where the DB already has the rows.
func (sc *ScoreCache) loadSilent(k ScoreKey, score float64) {
	sc.scores[k] = score
}

// All returns a copy of the underlying map. Used by Fork / Merge.
func (sc *ScoreCache) All() map[ScoreKey]float64 {
	out := make(map[ScoreKey]float64, len(sc.scores))
	for k, v := range sc.scores {
		out[k] = v
	}
	return out
}

// FuseScore computes the fused edge score. Cross-encoder relevance
// (reranker output) is the dominant semantic signal; temporal
// proximity breaks ties between comparably-scored candidates.
// Weights live in EngineConfig so tuning is configuration, not code.
func FuseScore(cfg EngineConfig, rerankerScore, temporalProximity float64) float64 {
	return cfg.WeightCE*rerankerScore + cfg.WeightTemp*temporalProximity
}

// TemporalProximity computes 1/(1+distance) where distance is the
// absolute position difference between two messages. Hyperbolic
// decay: adjacent ~0.5, 9 apart ~0.1, 99 apart ~0.01.
func TemporalProximity(posA, posB int64) float64 {
	d := posA - posB
	if d < 0 {
		d = -d
	}
	return 1.0 / (1.0 + float64(d))
}
