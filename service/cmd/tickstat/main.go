// Command tickstat reads the tick_traces table for diagnostic
// per-tick latency decomposition. Reports the most recent N traces
// as a table by default, or computes p50/p90/p99 percentiles per
// stage across the requested thread when -percentiles is set.
//
// Stage columns:
//
//	rrc      — Engine.OnMessage (chunking + embed + KNN + rerank + edges)
//	sel      — Engine.Select (graph walk + transitive reduction)
//	asm      — MMR + budget shed + token estimation
//	cmpl     — time-to-first-event (model response wait, network round-trip)
//	strm     — first event → stream close (network streaming + processing)
//	prst     — sum of per-InsertMessage durations (subset of strm)
//	total    — SendMessage wall clock
//
// Diagnostic reading: high cmpl → model slow. High strm-cmpl, low
// prst → streaming network slow. High prst → SQLite contention.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/emontenegr/grudge/service/storage"
)

func main() {
	var (
		dataDir     string
		threadID    string
		last        int
		percentiles bool
	)
	flag.StringVar(&dataDir, "data", defaultDataDir(), "Grudge data directory (contains grudge.db)")
	flag.StringVar(&threadID, "thread", "", "thread id to inspect (required)")
	flag.IntVar(&last, "last", 25, "show the most recent N traces (table mode)")
	flag.BoolVar(&percentiles, "percentiles", false, "print p50/p90/p99 per stage instead of the per-row table")
	flag.Parse()

	if threadID == "" {
		fmt.Fprintln(os.Stderr, "tickstat: -thread is required")
		flag.Usage()
		os.Exit(2)
	}

	db, err := storage.Open(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tickstat: open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	queryLimit := last
	if percentiles {
		queryLimit = 0 // all
	}
	traces, err := db.ListTickTraces(threadID, queryLimit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tickstat: list: %v\n", err)
		os.Exit(1)
	}
	if len(traces) == 0 {
		fmt.Fprintln(os.Stderr, "tickstat: no traces for that thread (yet)")
		os.Exit(0)
	}

	if percentiles {
		printPercentiles(traces)
		return
	}
	printTable(traces)
}

func printTable(traces []*storage.TickTrace) {
	fmt.Printf("%-5s %-19s %-6s %-7s %-7s %-7s %-7s %-7s %-7s %-7s %s\n",
		"round", "created_at", "total", "rrc", "sel", "asm", "cmpl", "strm", "prst", "errored", "model")
	for _, t := range traces {
		errMark := ""
		if t.Errored {
			errMark = "ERR"
		}
		fmt.Printf("%-5d %-19s %-6d %-7d %-7d %-7d %-7d %-7d %-7d %-7s %s\n",
			t.Round,
			t.CreatedAt.Format("2006-01-02 15:04:05"),
			t.TotalMs,
			t.RRCOnMessageMs,
			t.SelectMs,
			t.AssembleMs,
			t.CompleteMs,
			t.StreamMs,
			t.PersistMs,
			errMark,
			t.CompleterModel,
		)
	}
}

func printPercentiles(traces []*storage.TickTrace) {
	n := len(traces)
	fmt.Printf("traces: %d (across all rounds for the thread)\n\n", n)

	stages := []struct {
		name   string
		getter func(*storage.TickTrace) int64
	}{
		{"total", func(t *storage.TickTrace) int64 { return t.TotalMs }},
		{"rrc", func(t *storage.TickTrace) int64 { return t.RRCOnMessageMs }},
		{"sel", func(t *storage.TickTrace) int64 { return t.SelectMs }},
		{"asm", func(t *storage.TickTrace) int64 { return t.AssembleMs }},
		{"cmpl", func(t *storage.TickTrace) int64 { return t.CompleteMs }},
		{"strm", func(t *storage.TickTrace) int64 { return t.StreamMs }},
		{"prst", func(t *storage.TickTrace) int64 { return t.PersistMs }},
	}

	fmt.Printf("%-6s %-8s %-8s %-8s %-8s\n", "stage", "p50_ms", "p90_ms", "p99_ms", "max_ms")
	for _, s := range stages {
		values := make([]int64, n)
		for i, t := range traces {
			values[i] = s.getter(t)
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		fmt.Printf("%-6s %-8d %-8d %-8d %-8d\n",
			s.name,
			percentile(values, 0.50),
			percentile(values, 0.90),
			percentile(values, 0.99),
			values[len(values)-1],
		)
	}
}

// percentile returns the requested percentile from a sorted ascending
// slice. Uses nearest-rank — simpler than linear interpolation, and
// sufficient for diagnostic histograms.
func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// defaultDataDir mirrors what config.Load() resolves to on each
// platform. Hardcoded for the CLI's no-deps shape — config.Load
// would pull in the whole settings layer and the tool would need to
// understand provider config it never reads.
func defaultDataDir() string {
	if d := os.Getenv("GRUDGE_DATA_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	// Match the layout used by config.Load on Windows (.local/share/grudge)
	// and Linux/macOS (same path). Cross-platform-uniform via the env-var
	// escape hatch for non-standard installs.
	return home + "/.local/share/grudge"
}
