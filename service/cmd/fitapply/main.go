// fitinstall is a one-shot: it runs stage-3's exact union-fit chain
// (seed anchor + replay labels -> Fit -> Validate -> Health -> B>0 gate
// -> Save) using replay labels ALREADY collected by a completed judge
// pass (dumped via GRUDGE_MASSFIT_DUMP), instead of re-running the
// judge. The labels are the expensive artifact; this consumes them.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/elijahmontenegro/grudge/core"
	"github.com/elijahmontenegro/grudge/core/adapter/zerank"
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
	"github.com/elijahmontenegro/grudge/rrc/calibrate/seed"
	"github.com/elijahmontenegro/grudge/rrc/calibrate/seedfit"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: fitinstall <labels.tsv> <calibrator.json> <scorerModelID> <edgeCountAtFit>")
		os.Exit(2)
	}
	labelsPath, calPath, scorerModelID := os.Args[1], os.Args[2], os.Args[3]
	edgeCount, err := strconv.Atoi(os.Args[4])
	must(err)

	// Replay labels: sim \t mass \t true|false per line.
	f, err := os.Open(labelsPath)
	must(err)
	defer f.Close()
	var replay []calibrate.LabeledSample
	massPairs, contrastPairs := 0, 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.Fields(sc.Text())
		if len(parts) != 3 {
			continue
		}
		sim, err := strconv.ParseFloat(parts[0], 64)
		must(err)
		mass, err := strconv.ParseFloat(parts[1], 64)
		must(err)
		s := calibrate.LabeledSample{Sim: sim, Mass: mass, IsPrereq: parts[2] == "true"}
		if s.Mass > 0 {
			massPairs++
		} else {
			contrastPairs++
		}
		replay = append(replay, s)
	}
	must(sc.Err())
	fmt.Printf("replay labels: %d (%d mass / %d contrast)\n", len(replay), massPairs, contrastPairs)

	// Live scorer — the same instrument the labels' sims are in.
	base := scorerModelID[strings.LastIndex(scorerModelID, "@")+1:]
	sp := zerank.New(zerank.Config{BaseURL: base}).(core.ScorerProvider)
	scorer, err := sp.Scorer("")
	must(err)

	ctx := context.Background()
	seedSamples, pos, neg, err := seedfit.Samples(ctx, scorer, seed.Pairs(), 0)
	must(err)
	fmt.Printf("seed samples: %d (%d pos / %d neg)\n", len(seedSamples), pos, neg)

	union := append(seedSamples, replay...)
	cal, err := calibrate.Fit(union, calibrate.FitConfig{L2: 1e-4})
	must(err)
	must(calibrate.Validate(*cal, union))
	must(seedfit.Health(ctx, scorer, seed.Pairs(), *cal))
	if cal.B <= 0 {
		fmt.Fprintf(os.Stderr, "REFUSED: fitted B=%.3f <= 0 — keeping current artifact\n", cal.B)
		os.Exit(1)
	}
	must(calibrate.Save(calPath, calibrate.Artifact{
		Calibrator:           *cal,
		ScorerModelID:        scorerModelID,
		Samples:              len(union),
		LogLoss:              cal.LogLoss(union),
		MassSamples:          len(replay),
		ProvenanceEdgesAtFit: edgeCount,
	}))
	fmt.Printf("INSTALLED: A=%.3f B=%.3f C=%.3f log-loss=%.4f (%d seed + %d replay; next refit at %d edges)\n",
		cal.A, cal.B, cal.C, cal.LogLoss(union), len(seedSamples), len(replay), 2*edgeCount)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fitinstall:", err)
		os.Exit(1)
	}
}
