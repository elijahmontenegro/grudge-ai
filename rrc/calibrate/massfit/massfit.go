// Package massfit fits the mass axis (B) of the acceptance calibrator
// from replayed corpus history. The seed set is static — every seed
// sample carries mass=0, so B's gradient there is identically zero and
// the seed fit can only carry a prior ratio forward. B's true
// empirical value requires data where mass actually varies: a real
// corpus with provenance edges, replayed turn by turn, with candidates
// labeled by regenerative counterfactual coherence (regenjudge).
//
// KNOWN FIDELITY LIMITS (deliberate first-cut scope, documented so the
// gap is a decision rather than a surprise): replay reconstructs one
// selection per turn — the FIRST outbound call — while live selection
// runs per call in multi-step tool loops; mass is computed under
// ALL_THREADS while much live traffic is THREAD-scoped (replay mass is
// an upper bound there); and labels come from regenjudge's one-call
// judgment form. The faithful next step, if fit quality demands it, is
// replaying per selection EVENT from the persisted selections table
// rather than per turn from the raw corpus.
//
// Replay reconstructs, for each historical turn: the Local Context
// window as selection saw it (the immediately preceding turn ∪ the turn
// through its trigger — not the turn's finished message group), seeding
// the mass walk from the whole window exactly as live selection seeds it
// from the serialized membership, the corpus and provenance edges as
// they existed when the turn ran (as-of filtering by timestamp), the
// chain-ruled provenance mass of every reachable candidate (via
// rrc.ProvenanceMass — the engine's exact walk, not a
// reimplementation), the candidate's live similarity under the CURRENT
// scorer (the fit must pair labels with the score distribution being
// calibrated, not whatever scorer ran historically), and the judge's
// verdict. Mass-bearing candidates are paired with randomly drawn
// zero-mass contrast candidates — the randomized-inclusion corrective
// for coverage bias: labeling only what the current policy surfaces
// would teach the fit the policy back.
//
// The package is corpus-in, samples-out: no storage, no engine, no
// LLM dependency — the judge and scorer arrive as one-method
// interfaces, so tests inject fakes and the rrc stratum stays
// self-contained.
package massfit

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"time"

	rrcv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/rrc/v1"
	threadv1 "github.com/elijahmontenegro/grudge/proto/gen/go/grudge/thread/v1"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
	"github.com/elijahmontenegro/grudge/rrc/calibrate/regenjudge"
	"github.com/elijahmontenegro/grudge/rrc/chunk"
)

// judgeMaxAttempts / maxToleratedDrops make the replay resilient to
// TRANSIENT scorer/judge failures without letting SYSTEMATIC breakage
// silently thin the sample set. The judge is often a cloud model
// (regenjudge runs the main completer); a 128-call replay firing rapidly
// will occasionally hit a rate-limit or a momentary timeout, and one such
// hiccup must not abort the whole fit. Each call retries a few times; a
// still-failing sample is DROPPED (never biases the fit — transient
// failures are random w.r.t. sim/mass/label) and counted in Stats.Dropped
// so the caller can log it (no silent cap). Only when drops exceed the
// tolerance is it systematic — a broken judge/scorer — and the replay
// aborts loudly.
const judgeMaxAttempts = 3
const maxToleratedDrops = 8

// retryTransient runs op up to judgeMaxAttempts with linear backoff,
// returning the last error if all attempts fail. Cancellation is honored
// between attempts.
func retryTransient(ctx context.Context, op func() error) error {
	var err error
	for attempt := 0; attempt < judgeMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		if err = op(); err == nil {
			return nil
		}
	}
	return err
}

// Scorer is the one-method scoring contract replay needs. Structurally
// identical to rrc.Scorer / core.Scorer, declared here so importing the
// replay pulls neither.
type Scorer interface {
	Score(ctx context.Context, query string, candidates []string) ([]float64, error)
}

// maxLabels bounds one replay run's judge work. Per-run cost is a
// constant, not corpus-proportional — the per-step-invariance stance;
// total cost across a corpus's life is amortized by the caller's
// doubling watermark. Deliberately generous relative to the seed set's
// ~155 samples so the mass axis is not starved.
const maxLabels = 128

// perTurnMassCap bounds mass-bearing candidates taken per turn, so one
// heavily-provenanced turn cannot monopolize the label budget.
const perTurnMassCap = 4

// localChunkCap bounds how many Local Context chunks score each
// candidate (max-merged) — mirrors the engine's per-chunk max-merge
// while keeping per-candidate scorer calls constant.
const localChunkCap = 4

// randSeed makes contrast sampling deterministic for a given corpus:
// replays are reproducible, tests are stable. The value is arbitrary;
// varying it buys nothing (the corpus itself varies).
const randSeed = 1

// Stats reports what a replay run labeled, for logs and telemetry.
type Stats struct {
	TurnsConsidered int
	TurnsSampled    int
	MassPairs       int
	ContrastPairs   int
	// Truncated reports that at least one turn's provenance walk hit
	// provenanceReachCap — the no-silent-cap contract, surfaced.
	Truncated bool
	// Dropped counts (candidate) samples skipped after a scorer or judge
	// call failed every retry — transient infrastructure hiccups tolerated
	// up to maxToleratedDrops. Surfaced so a thinned sample set is never
	// silent; past the tolerance Replay aborts instead.
	Dropped int
}

// turnGroup is one historical turn: its id and messages in corpus order.
type turnGroup struct {
	id       string
	messages []*threadv1.Message
}

// Replay walks the corpus turn by turn and returns judge-labeled
// (sim, mass) samples. It tolerates a bounded number of TRANSIENT
// scorer/judge failures (each call retries; a persistently-failing
// sample is dropped and counted in Stats.Dropped — surfaced, never
// silent), and aborts only when drops exceed the tolerance (systematic
// breakage). It errors when the corpus yields no mass-bearing pairs at
// all — the caller armed on raw edge counts, and "structure exists but
// none of it is reachable" must surface, not fit B from nothing.
func Replay(ctx context.Context, corpus []*threadv1.Message, edges []*rrcv1.Edge, scorer Scorer, judge calibrate.CounterfactualJudge, chunkCfg chunk.Config) ([]calibrate.LabeledSample, Stats, error) {
	if scorer == nil {
		return nil, Stats{}, fmt.Errorf("massfit: nil scorer")
	}
	if judge == nil {
		return nil, Stats{}, fmt.Errorf("massfit: nil judge")
	}

	byID := make(map[string]*threadv1.Message, len(corpus))
	for _, m := range corpus {
		byID[m.Id] = m
	}
	turns := groupTurns(corpus)

	rng := rand.New(rand.NewSource(randSeed))
	var samples []calibrate.LabeledSample
	var stats Stats

	// The replay window must be the Local Context AS SELECTION SAW IT:
	// the immediately preceding turn ∪ the turn through its trigger — NOT
	// the turn's full post-hoc message group (the generated messages did
	// not exist at selection time; using them would leak the future). The
	// mass walk seeds from the whole window, exactly as live selection
	// seeds it from the serialized membership: a trigger has no incoming
	// provenance edges — they are recorded at generation, i.e. after — so
	// a turn-only walk would find nothing at any trigger call and B could
	// never fit. Window members are delivered, so they are excluded from
	// candidacy AND from the walk's own output — what surfaces (and gets
	// judged) is their undelivered ancestry.
	fallbackN := rrc.DefaultConfig().LocalContextSize

	byThread := make(map[string][]*threadv1.Message)
	for _, m := range corpus {
		byThread[m.ThreadId] = append(byThread[m.ThreadId], m)
	}

	// Chronological order, not storage order: AllCorpus sorts by
	// (thread_id, position), and first-appearance grouping over that
	// would let whichever threads sort lexicographically first eat the
	// whole label budget.
	sort.SliceStable(turns, func(i, j int) bool {
		return turns[i].messages[0].CreatedAt.AsTime().Before(turns[j].messages[0].CreatedAt.AsTime())
	})

	for _, turn := range turns {
		if len(samples) >= maxLabels {
			break
		}
		stats.TurnsConsidered++

		// Only user-triggered turns replay. A turn whose first stored
		// message is model output (an autonomous tick's empty-content
		// call) had no selector input live — replaying it would score
		// candidates against text that did not exist at selection time.
		if turn.messages[0].Role != threadv1.Role_ROLE_USER {
			continue
		}
		anchorTime := turn.messages[0].CreatedAt.AsTime()
		threadID := turn.messages[0].ThreadId

		// Thread corpus strictly before the trigger, plus the trigger
		// itself — symmetric with the candidate/edge cutoffs below, so
		// same-timestamp rows (imported corpora) can't enter the cone
		// window while being excluded as candidates.
		var threadThroughTrigger []*threadv1.Message
		for _, m := range byThread[threadID] {
			if m.CreatedAt.AsTime().Before(anchorTime) || m.Id == turn.messages[0].Id {
				threadThroughTrigger = append(threadThroughTrigger, m)
			}
		}
		coneMsgs := rrc.BuildActiveDiscourse(threadThroughTrigger, turn.id, fallbackN)
		if len(coneMsgs) == 0 {
			continue
		}
		// Window = the immediately preceding turn ∪ the turn-through-
		// trigger, mirroring the live window fetch (turn-complete; legacy
		// empty-turn rows never form a tail). Derived from the replay's
		// own corpus slice — no storage reads.
		windowMsgs := coneMsgs
		if turn.id != "" {
			tailTurn := ""
			for i := len(threadThroughTrigger) - 1; i >= 0; i-- {
				m := threadThroughTrigger[i]
				if m.TurnId != "" && m.TurnId != turn.id {
					tailTurn = m.TurnId
					break
				}
			}
			if tailTurn != "" {
				var tail []*threadv1.Message
				for _, m := range threadThroughTrigger {
					if m.TurnId == tailTurn {
						tail = append(tail, m)
					}
				}
				windowMsgs = append(tail, coneMsgs...)
			}
		}
		window := make(map[string]bool, len(windowMsgs))
		walkAnchors := make([]string, 0, len(windowMsgs))
		for _, m := range windowMsgs {
			window[m.Id] = true
			walkAnchors = append(walkAnchors, m.Id)
		}

		// The world as this turn saw it.
		var edgesAsOf []*rrcv1.Edge
		for _, e := range edges {
			if e != nil && e.DetectedAt != nil && e.DetectedAt.AsTime().Before(anchorTime) {
				edgesAsOf = append(edgesAsOf, e)
			}
		}
		asOf := make(map[string]bool, len(corpus))
		var asOfIDs []string
		for _, m := range corpus {
			if m.CreatedAt.AsTime().Before(anchorTime) && !window[m.Id] && rrc.SerializeMessageForScoring(m) != "" {
				asOf[m.Id] = true
				asOfIDs = append(asOfIDs, m.Id)
			}
		}
		if len(asOfIDs) == 0 {
			continue
		}

		// Mass under ALL_THREADS: provenance is a structural fact of the
		// corpus, and the /\'s home turf includes cross-thread roots.
		// KNOWN FIDELITY LIMIT: live THREAD-scoped selections prune
		// cross-thread chains this replay keeps, so replay mass is an
		// upper bound on live mass for those turns.
		mass, trunc := rrc.ProvenanceMass(edgesAsOf, walkAnchors, threadID, threadv1.SelectionScope_SELECTION_SCOPE_ALL_THREADS)
		if trunc {
			stats.Truncated = true
		}

		var massIDs []string
		for id, m := range mass {
			if m > 0 && asOf[id] {
				massIDs = append(massIDs, id)
			}
		}
		if len(massIDs) == 0 {
			continue
		}
		// Deterministic: heaviest mass first, id-tiebroken.
		sort.Slice(massIDs, func(i, j int) bool {
			if mass[massIDs[i]] != mass[massIDs[j]] {
				return mass[massIDs[i]] > mass[massIDs[j]]
			}
			return massIDs[i] < massIDs[j]
		})
		// Half the per-turn quota goes to the top of the mass ranking,
		// half to a seeded-random draw from the REST of the mass-bearing
		// pool — without the random half, B would be fitted on a bimodal
		// (top-tail vs exact-zero) distribution and its slope across the
		// mid-mass range, where the acceptance boundary actually
		// operates, would be pure extrapolation.
		if len(massIDs) > perTurnMassCap {
			head := massIDs[:perTurnMassCap/2]
			rest := append([]string(nil), massIDs[perTurnMassCap/2:]...)
			rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
			take := perTurnMassCap - len(head)
			if take > len(rest) {
				take = len(rest)
			}
			picked := append(append([]string(nil), head...), rest[:take]...)
			sort.Strings(picked)
			massIDs = picked
		}

		// Contrast: equal count of zero-mass candidates, drawn seeded-random
		// from the same as-of corpus — the coverage-bias corrective.
		contrastIDs := drawContrast(rng, asOfIDs, mass, len(massIDs))

		local := rrc.SerializeLocalContext(windowMsgs, turn.id, chunkCfg)
		if local == nil || len(local.Chunks) == 0 {
			continue
		}
		stats.TurnsSampled++

		for _, group := range []struct {
			ids      []string
			withMass bool
		}{{massIDs, true}, {contrastIDs, false}} {
			for _, id := range group.ids {
				if len(samples) >= maxLabels {
					break
				}
				var sim float64
				serr := retryTransient(ctx, func() error {
					var e error
					sim, e = scoreCandidate(ctx, scorer, local, byID[id], chunkCfg)
					return e
				})
				var isPrereq bool
				jerr := retryTransient(ctx, func() error {
					var e error
					isPrereq, e = judge.IsPrerequisite(ctx, turn.id, id)
					return e
				})
				if serr != nil || jerr != nil {
					// Transient hiccup survived the retries: drop this sample
					// rather than abort the whole replay — a dropped sample
					// doesn't bias the fit (failures are random w.r.t.
					// sim/mass/label). Past the tolerance it is systematic
					// breakage, not noise, and we abort loudly.
					stats.Dropped++
					if stats.Dropped > maxToleratedDrops {
						return nil, stats, fmt.Errorf("massfit: %d transient scorer/judge failures exceed tolerance (%d) — infrastructure unstable, aborting (last scorer=%v judge=%v)", stats.Dropped, maxToleratedDrops, serr, jerr)
					}
					continue
				}
				m := 0.0
				if group.withMass {
					m = mass[id]
					stats.MassPairs++
				} else {
					stats.ContrastPairs++
				}
				samples = append(samples, calibrate.LabeledSample{Sim: sim, Mass: m, IsPrereq: isPrereq})
			}
		}
	}

	if stats.MassPairs == 0 {
		// Drops with zero surviving mass pairs is a judge/scorer failure
		// wearing corpus-youth's clothes — the mass pairs existed, we just
		// couldn't label them. On a small corpus this never trips the
		// in-loop count tolerance, so surface it here as infrastructure,
		// not a cold-start deferral (which would wrongly defer instead of
		// alerting).
		if stats.Dropped > 0 {
			return nil, stats, fmt.Errorf("massfit: no mass pairs survived labeling — %d dropped to scorer/judge failures; infrastructure unstable, aborting", stats.Dropped)
		}
		// Not a failure: provenance edges exist but none reach an eligible
		// candidate through any turn's cone, so B cannot be grounded yet. The
		// caller treats ErrCorpusTooYoung as a deferral — keep the current
		// calibrator and recheck as the corpus grows — distinct from a genuine
		// scorer/judge fault, which aborts earlier.
		return nil, stats, fmt.Errorf("massfit: no mass-bearing (turn, candidate) pairs reachable in %d turns: %w", stats.TurnsConsidered, ErrCorpusTooYoung)
	}
	return samples, stats, nil
}

// ErrCorpusTooYoung marks a replay that found no mass-bearing (turn, candidate)
// pairs — provenance edges exist but none reach an eligible candidate through a
// turn's cone yet, so B cannot be grounded. It is a cold-start deferral, not a
// failure: the caller keeps the current artifact and rechecks as the corpus
// grows. Note a single-thread corpus can still fit B when its provenance chains
// reach far enough back — the signal here is reachability, not thread count.
var ErrCorpusTooYoung = errors.New("massfit: no mass-bearing pairs reachable yet")

// groupTurns splits the corpus into turns (messages sharing a non-empty
// turn_id) in first-appearance order.
func groupTurns(corpus []*threadv1.Message) []turnGroup {
	index := make(map[string]int)
	var turns []turnGroup
	for _, m := range corpus {
		if m.TurnId == "" {
			continue
		}
		i, ok := index[m.TurnId]
		if !ok {
			i = len(turns)
			index[m.TurnId] = i
			turns = append(turns, turnGroup{id: m.TurnId})
		}
		turns[i].messages = append(turns[i].messages, m)
	}
	return turns
}

// drawContrast picks n zero-mass candidate ids, seeded-random over the
// as-of corpus.
func drawContrast(rng *rand.Rand, asOfIDs []string, mass map[string]float64, n int) []string {
	shuffled := append([]string(nil), asOfIDs...)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	var out []string
	for _, id := range shuffled {
		if len(out) >= n {
			break
		}
		if mass[id] == 0 {
			out = append(out, id)
		}
	}
	return out
}

// scoreCandidate reproduces the engine's scoring shape: each Local
// Context chunk scores the candidate's chunks, max-merged to one sim —
// the same per-chunk max-merge SelectPrerequisites banks into edges.
func scoreCandidate(ctx context.Context, scorer Scorer, local *rrc.SerializedLocalContext, cand *threadv1.Message, chunkCfg chunk.Config) (float64, error) {
	if cand == nil {
		return 0, fmt.Errorf("candidate missing from corpus")
	}
	text := rrc.SerializeMessageForScoring(cand)
	candChunks := chunk.Split(text, chunkCfg)
	candTexts := make([]string, 0, len(candChunks))
	for _, c := range candChunks {
		candTexts = append(candTexts, c.Text)
	}
	if len(candTexts) == 0 {
		candTexts = []string{text}
	}

	best := 0.0
	locals := local.Chunks
	if len(locals) > localChunkCap {
		locals = locals[:localChunkCap]
	}
	for _, lc := range locals {
		scores, err := scorer.Score(ctx, lc.Text, candTexts)
		if err != nil {
			return 0, err
		}
		for _, s := range scores {
			if s > best {
				best = s
			}
		}
	}
	return best, nil
}

// NewCorpusProvider adapts an in-memory corpus to regenjudge's
// ContextProvider: the turn's own messages are the local discourse,
// the candidate resolves by id. This is the production provider —
// replay already holds the whole corpus, so the judge needs no storage
// access of its own.
func NewCorpusProvider(corpus []*threadv1.Message) regenjudge.ContextProvider {
	byID := make(map[string]*threadv1.Message, len(corpus))
	byTurn := make(map[string][]*threadv1.Message)
	for _, m := range corpus {
		byID[m.Id] = m
		if m.TurnId != "" {
			byTurn[m.TurnId] = append(byTurn[m.TurnId], m)
		}
	}
	return corpusProvider{byID: byID, byTurn: byTurn}
}

type corpusProvider struct {
	byID   map[string]*threadv1.Message
	byTurn map[string][]*threadv1.Message
}

func (p corpusProvider) Resolve(turnID, candidateID string) (regenjudge.TurnContext, error) {
	local := p.byTurn[turnID]
	if len(local) == 0 {
		return regenjudge.TurnContext{}, fmt.Errorf("massfit: unknown turn %q", turnID)
	}
	cand := p.byID[candidateID]
	if cand == nil {
		return regenjudge.TurnContext{}, fmt.Errorf("massfit: unknown candidate %q", candidateID)
	}
	return regenjudge.TurnContext{LocalContext: local, Candidate: cand}, nil
}
