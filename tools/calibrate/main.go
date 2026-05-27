// Command calibrate scores the labeled eval pairs through the zerank
// scorer and produces a precision/recall sweep across candidate
// EdgeThreshold values, so the engine's threshold is derived from
// real-model output rather than a fixed guess.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"github.com/emontenegr/spidey/core"
	_ "github.com/emontenegr/spidey/core/adapter/zerank"
)

type triple struct {
	ID          string   `json:"id"`
	Query       string   `json:"query"`
	Correct     string   `json:"correct"`
	Distractors []string `json:"distractors"`
}

type pairs struct {
	Categories map[string][]triple `json:"categories"`
}

type scored struct {
	Category string
	ID       string
	Score    float64
	Label    int // 1=positive (correct), 0=negative (distractor)
}

func main() {
	pairsPath := "eval/pairs.json"
	if v := os.Getenv("PAIRS"); v != "" {
		pairsPath = v
	}
	data, err := os.ReadFile(pairsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", pairsPath, err)
		os.Exit(1)
	}
	var p pairs
	if err := json.Unmarshal(data, &p); err != nil {
		fmt.Fprintf(os.Stderr, "parse: %v\n", err)
		os.Exit(1)
	}

	prov, err := core.NewProvider(core.ProviderConfig{
		Adapter: "zerank",
		BaseURL: "http://127.0.0.1:8000",
		Model:   "zeroentropy/zerank-1-small",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "provider: %v\n", err)
		os.Exit(1)
	}
	cp := prov.(core.ClassifierProvider)
	cls, err := cp.Classifier("zeroentropy/zerank-1-small")
	if err != nil {
		fmt.Fprintf(os.Stderr, "classifier: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	var all []scored
	var nQ int
	for cat, items := range p.Categories {
		for _, t := range items {
			nQ++
			candidates := append([]string{t.Correct}, t.Distractors...)
			start := time.Now()
			scores, err := cls.Score(ctx, t.Query, candidates)
			if err != nil {
				fmt.Fprintf(os.Stderr, "score %s: %v\n", t.ID, err)
				continue
			}
			elapsed := time.Since(start).Round(time.Millisecond)
			fmt.Fprintf(os.Stderr, "  [%d] %s/%s scored %d in %s — correct=%.3f distractors=%v\n",
				nQ, cat, t.ID, len(scores), elapsed, scores[0], roundN(scores[1:], 3))
			for i, s := range scores {
				lbl := 0
				if i == 0 {
					lbl = 1
				}
				all = append(all, scored{Category: cat, ID: t.ID, Score: s, Label: lbl})
			}
		}
	}

	pos := 0
	neg := 0
	for _, s := range all {
		if s.Label == 1 {
			pos++
		} else {
			neg++
		}
	}
	fmt.Printf("\n=== %d pairs scored: %d positives, %d negatives ===\n\n", len(all), pos, neg)

	// Sweep thresholds.
	fmt.Printf("%-10s %-10s %-10s %-10s %-10s %-10s\n", "threshold", "TP", "FP", "FN", "precision", "recall")
	fmt.Println("----------------------------------------------------------------------")
	thresholds := []float64{0.30, 0.35, 0.40, 0.45, 0.50, 0.55, 0.60, 0.65, 0.70, 0.75, 0.80, 0.85, 0.90, 0.95}
	for _, th := range thresholds {
		tp, fp, fn := 0, 0, 0
		for _, s := range all {
			above := s.Score >= th
			if s.Label == 1 && above {
				tp++
			} else if s.Label == 0 && above {
				fp++
			} else if s.Label == 1 && !above {
				fn++
			}
		}
		var precision, recall float64
		if tp+fp > 0 {
			precision = float64(tp) / float64(tp+fp)
		}
		if tp+fn > 0 {
			recall = float64(tp) / float64(tp+fn)
		}
		fmt.Printf("%-10.2f %-10d %-10d %-10d %-10.3f %-10.3f\n",
			th, tp, fp, fn, precision, recall)
	}

	// Distribution stats per label.
	fmt.Println("\n=== score distributions ===")
	posScores := []float64{}
	negScores := []float64{}
	for _, s := range all {
		if s.Label == 1 {
			posScores = append(posScores, s.Score)
		} else {
			negScores = append(negScores, s.Score)
		}
	}
	sort.Float64s(posScores)
	sort.Float64s(negScores)
	printDist("positives", posScores)
	printDist("negatives", negScores)

	// Recommend threshold: F1-max + 95%-precision points.
	fmt.Println("\n=== thresholds of interest ===")
	bestF1, bestF1Thr := 0.0, 0.0
	var thr95 float64 = -1
	for th := 0.30; th <= 0.95; th += 0.01 {
		tp, fp, fn := 0, 0, 0
		for _, s := range all {
			above := s.Score >= th
			if s.Label == 1 && above {
				tp++
			} else if s.Label == 0 && above {
				fp++
			} else if s.Label == 1 && !above {
				fn++
			}
		}
		var pr, rc, f1 float64
		if tp+fp > 0 {
			pr = float64(tp) / float64(tp+fp)
		}
		if tp+fn > 0 {
			rc = float64(tp) / float64(tp+fn)
		}
		if pr+rc > 0 {
			f1 = 2 * pr * rc / (pr + rc)
		}
		if f1 > bestF1 {
			bestF1 = f1
			bestF1Thr = th
		}
		if thr95 < 0 && pr >= 0.95 && tp > 0 {
			thr95 = th
		}
	}
	fmt.Printf("  best F1: %.3f at threshold %.2f\n", bestF1, bestF1Thr)
	if thr95 > 0 {
		fmt.Printf("  ≥95%% precision: at threshold %.2f\n", thr95)
	} else {
		fmt.Printf("  ≥95%% precision: not reached (all thresholds keep too many FPs)\n")
	}
}

func roundN(xs []float64, n int) []float64 {
	mult := math.Pow(10, float64(n))
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = math.Round(x*mult) / mult
	}
	return out
}

func printDist(label string, xs []float64) {
	if len(xs) == 0 {
		return
	}
	min := xs[0]
	max := xs[len(xs)-1]
	median := xs[len(xs)/2]
	p25 := xs[len(xs)/4]
	p75 := xs[3*len(xs)/4]
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	fmt.Printf("  %s (n=%d):  min=%.3f p25=%.3f median=%.3f mean=%.3f p75=%.3f max=%.3f\n",
		label, len(xs), min, p25, median, mean, p75, max)
}
