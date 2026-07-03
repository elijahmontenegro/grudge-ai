package rrc

// scoreCache stores serialized-local-context/candidate-chunk scores.
type scoreCache struct {
	scores  map[scoreKey]float64
	persist ScorePersister
}

type scoreKey struct {
	LocalContextFingerprint string
	LocalContextChunkIndex  int
	CandidateMsgID          string
	CandidateChunkIdx       int
}

type PersistedScore struct {
	LocalContextFingerprint string
	LocalContextChunkIndex  int
	CandidateMsgID          string
	CandidateChunkIdx       int
	Score                   float64
}

type ScorePersister func(localContextFingerprint string, localContextChunkIndex int, candidateID string, candidateChunkIndex int, score float64)

func newScoreCache() *scoreCache {
	return &scoreCache{scores: make(map[scoreKey]float64)}
}

func (sc *scoreCache) setPersister(p ScorePersister) { sc.persist = p }

func (sc *scoreCache) setLocalContext(fingerprint string, localContextChunkIndex int, candidateID string, candidateChunkIndex int, score float64) {
	k := scoreKey{
		LocalContextFingerprint: fingerprint,
		LocalContextChunkIndex:  localContextChunkIndex,
		CandidateMsgID:          candidateID,
		CandidateChunkIdx:       candidateChunkIndex,
	}
	sc.scores[k] = score
	if sc.persist != nil {
		sc.persist(fingerprint, localContextChunkIndex, candidateID, candidateChunkIndex, score)
	}
}

func (sc *scoreCache) getLocalContext(fingerprint string, localContextChunkIndex int, candidateID string, candidateChunkIndex int) (float64, bool) {
	s, ok := sc.scores[scoreKey{
		LocalContextFingerprint: fingerprint,
		LocalContextChunkIndex:  localContextChunkIndex,
		CandidateMsgID:          candidateID,
		CandidateChunkIdx:       candidateChunkIndex,
	}]
	return s, ok
}

func (sc *scoreCache) loadSilent(k scoreKey, score float64) {
	sc.scores[k] = score
}

func (sc *scoreCache) all() map[scoreKey]float64 {
	out := make(map[scoreKey]float64, len(sc.scores))
	for k, v := range sc.scores {
		out[k] = v
	}
	return out
}
