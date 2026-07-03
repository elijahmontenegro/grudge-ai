// Command calibrate is the DEVELOPER tool for fitting/inspecting the RRC
// acceptance calibrator offline. The running system does not need it:
// substrate.Holder self-calibrates in the background (fit from the embedded
// seed set, persist, live-swap) whenever a scorer is configured and no fitted
// calibrator exists for it. Use this command to regenerate an artifact
// against a custom pairs file, inspect fit quality in CI, or debug a scorer's
// score distribution — not as a required setup step.
//
// Usage:
//
//	calibrate -scorer-adapter zerank -scorer-url http://127.0.0.1:8000 \
//	          -scorer-model zeroentropy/zerank-1-small \
//	          [-pairs eval/pairs.json] [-out calibrator.json]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/elijahmontenegro/grudge/core"
	_ "github.com/elijahmontenegro/grudge/core/adapter/gcpranking"
	_ "github.com/elijahmontenegro/grudge/core/adapter/tei"
	_ "github.com/elijahmontenegro/grudge/core/adapter/zerank"
	"github.com/elijahmontenegro/grudge/eval"
	"github.com/elijahmontenegro/grudge/rrc"
	"github.com/elijahmontenegro/grudge/rrc/calibrate"
	"github.com/elijahmontenegro/grudge/rrc/calibrate/seedfit"
)

func main() {
	var (
		pairsPath = flag.String("pairs", "", "labeled pairs file (default: the embedded seed set)")
		adapter   = flag.String("scorer-adapter", "zerank", "scorer adapter name")
		baseURL   = flag.String("scorer-url", "http://127.0.0.1:8000", "scorer base URL")
		model     = flag.String("scorer-model", "zeroentropy/zerank-1-small", "scorer model id")
		out       = flag.String("out", "calibrator.json", "output calibrator path")
	)
	flag.Parse()

	seed := eval.Seed()
	if *pairsPath != "" {
		b, err := os.ReadFile(*pairsPath)
		if err != nil {
			fail("read pairs: %v", err)
		}
		seed = b
	}

	prov, err := core.NewProvider(core.ProviderConfig{Adapter: *adapter, BaseURL: *baseURL, Model: *model})
	if err != nil {
		fail("provider: %v", err)
	}
	sp, ok := prov.(core.ScorerProvider)
	if !ok {
		fail("adapter %q is not a ScorerProvider", *adapter)
	}
	scr, err := sp.Scorer(*model)
	if err != nil {
		fail("scorer: %v", err)
	}

	// Seed data is mass-less; carry the default bootstrap's structural-lift
	// ratio forward rather than zeroing it (matches the runtime path).
	prior := rrc.DefaultConfig().Calibrator
	res, err := seedfit.Fit(context.Background(), scr, seed, prior)
	if err != nil {
		fail("fit: %v", err)
	}
	if err := calibrate.Save(*out, res.Calibrator, *model, res.Samples, res.LogLoss); err != nil {
		fail("save: %v", err)
	}

	cal := res.Calibrator
	fmt.Printf("Fitted calibrator over %d samples (%d pos, %d neg), scorer=%s\n", res.Samples, res.Positives, res.Negatives, *model)
	fmt.Printf("  coefficients: A(sim)=%.4f B(mass)=%.4f C=%.4f\n", cal.A, cal.B, cal.C)
	fmt.Printf("  log-loss: %.4f\n", res.LogLoss)
	fmt.Printf("  P(prereq|sim=0.8,mass=0)=%.3f  P(prereq|sim=0.2,mass=0)=%.3f\n",
		cal.Predict(0.8, 0), cal.Predict(0.2, 0))
	fmt.Printf("  wrote %s\n", *out)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
