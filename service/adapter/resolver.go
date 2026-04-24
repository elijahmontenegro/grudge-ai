package adapter

// StoreResolver — concrete Resolver backed by the persisted corpus
// and the reranker score cache. Built fresh per query: the query
// message id anchors the score lookup, the corpus snapshot bounds
// the search space, the initial exclusion set removes messages
// already contributing to the input wire.
//
// Scoring surface is the same one RRC's Selection uses — persisted
// chunk-pair reranker scores from the current model — consulted
// after Selection's discrimination gates. A candidate that failed
// gate 1/2/3 and didn't make it into edges still has its score in
// the cache and is eligible for protocol-slot fill here.
//
// Consumption: each BestCandidate pick marks the candidate used.
// Subsequent calls within the same pipeline pass skip it. A caller
// can pre-seed the used set with messages already in the input
// wire so the resolver never re-fetches content that's already
// contributing to the payload.

import (
	"fmt"
	"sort"

	pb "github.com/emontenegr/spidey/gen/go/spidey/v1"
	"github.com/emontenegr/spidey/service/storage"
)

// StoreResolver is a Resolver backed by a corpus snapshot and the
// reranker score cache for a specific (query message, reranker
// model) pair. Not safe for concurrent use — construct per pass.
type StoreResolver struct {
	// ordered is corpus sorted score-desc with recency tiebreak.
	// BestCandidate walks this in order.
	ordered []*pb.Message
	// used tracks message ids the resolver has either consumed
	// (returned from BestCandidate) or been told to exclude (ids
	// already in the input wire).
	used map[string]bool
	// scores is the rerank-max map keyed by candidate msgID.
	// Exposed via Score() so the unified budget walk can place
	// rectification picks and Selected entries on the same axis.
	scores map[string]float64
	// picks is the chronological log of candidates consumed by
	// BestCandidate. Caller reads this after Apply to correlate
	// rectification inserts with their scores.
	picks []ResolverPick
}

// ResolverPick records one consumption event.
type ResolverPick struct {
	MsgID string
	Score float64 // rerank-max against the query
}

// NewStoreResolver loads scope-appropriate corpus and the query-
// specific score map in one shot, sorts, and returns ready to use.
// Cheap — single corpus query (~10k rows at most), single score
// query (bounded by rerank top-K).
func NewStoreResolver(db *storage.DB, threadID, queryID, rerankerModelID string, scope pb.SelectionScope) (*StoreResolver, error) {
	var corpus []*pb.Message
	var err error
	if scope == pb.SelectionScope_SELECTION_SCOPE_ALL_THREADS {
		corpus, err = db.AllCorpus()
	} else {
		corpus, err = db.ThreadCorpus(threadID)
	}
	if err != nil {
		return nil, fmt.Errorf("corpus load: %w", err)
	}

	scores, err := db.MaxScoresFrom(queryID, rerankerModelID)
	if err != nil {
		return nil, fmt.Errorf("score lookup: %w", err)
	}

	ordered := make([]*pb.Message, len(corpus))
	copy(ordered, corpus)
	sort.SliceStable(ordered, func(i, j int) bool {
		si, iOK := scores[ordered[i].Id]
		sj, jOK := scores[ordered[j].Id]
		if iOK && jOK {
			return si > sj
		}
		if iOK != jOK {
			return iOK // scored beats unscored
		}
		return ordered[i].Position > ordered[j].Position
	})

	return &StoreResolver{
		ordered: ordered,
		used:    make(map[string]bool),
		scores:  scores,
	}, nil
}

// Score returns the rerank-max value cached for the given message
// id against the resolver's query, or 0 if no score is cached.
// Caller uses this to place Selected entries (which carry their
// own walk-effective score) on the same rerank-max axis as
// rectification picks for unified budget decisions.
func (r *StoreResolver) Score(msgID string) float64 {
	return r.scores[msgID]
}

// Picks returns the chronological list of candidates consumed
// during this pass. Reset between passes by constructing a fresh
// resolver.
func (r *StoreResolver) Picks() []ResolverPick {
	return r.picks
}

// ExcludeIDs marks ids as unavailable for picking. Typical use:
// seed with every pb.Message id already contributing to the input
// wire before the pipeline runs, so no rule re-fetches content
// that's already there under a rebound identity.
func (r *StoreResolver) ExcludeIDs(ids []string) {
	for _, id := range ids {
		r.used[id] = true
	}
}

// BestCandidate walks the score-ordered corpus and returns the
// first message that passes the filter and isn't in the used set.
// On pick, marks the returned message used and records the pick
// (with its rerank-max score) in the pick log.
func (r *StoreResolver) BestCandidate(filter func(*pb.Message) bool) (*pb.Message, error) {
	for _, m := range r.ordered {
		if r.used[m.Id] {
			continue
		}
		if filter(m) {
			r.used[m.Id] = true
			r.picks = append(r.picks, ResolverPick{MsgID: m.Id, Score: r.scores[m.Id]})
			return m, nil
		}
	}
	return nil, nil
}
