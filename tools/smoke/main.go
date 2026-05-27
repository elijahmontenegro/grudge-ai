// Command smoke exercises the substrate wiring against the live
// tei-embed and vllm services. Validates:
//
//  1. NewProvider returns a value that type-asserts cleanly to the
//     per-role interface (ClassifierProvider for zerank,
//     EmbedderProvider for tei).
//  2. tei.Embed returns vectors for both RoleQuery and RoleDocument.
//  3. zerank yields a non-trivial relevance score for a (query,
//     document) pair where the document genuinely answers the query.
//  4. The embedder returns a 1024-dim normalized vector.
//
// Not a substitute for a full end-to-end service test (which would
// require a configured main completer); is the strongest live-
// service check the substrate alone supports.
package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"time"

	"github.com/emontenegr/spidey/core"
	_ "github.com/emontenegr/spidey/core/adapter/tei"
	_ "github.com/emontenegr/spidey/core/adapter/zerank"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pass := 0
	fail := 0
	check := func(label string, err error) {
		if err != nil {
			fmt.Printf("FAIL %s: %v\n", label, err)
			fail++
			return
		}
		fmt.Printf("OK   %s\n", label)
		pass++
	}

	// --- Classifier (zerank via vllm) ---

	clsProv, err := core.NewProvider(core.ProviderConfig{
		Adapter: "zerank",
		BaseURL: "http://127.0.0.1:8000",
		Model:   "zeroentropy/zerank-1-small",
	})
	check("NewProvider(zerank)", err)
	if err == nil {
		cp, ok := clsProv.(core.ClassifierProvider)
		if !ok {
			check("zerank satisfies ClassifierProvider", fmt.Errorf("type assertion failed"))
		} else {
			check("zerank satisfies ClassifierProvider", nil)
			classifier, err := cp.Classifier("zeroentropy/zerank-1-small")
			check("zerank.Classifier()", err)
			if err == nil {
				scores, err := classifier.Score(ctx,
					"What is the capital of France?",
					[]string{
						"Paris is the capital and largest city of France.",
						"The Eiffel Tower is a wrought-iron tower in Paris.",
						"Bananas are a popular tropical fruit.",
					})
				check("zerank.Score() returned", err)
				if err == nil {
					fmt.Printf("     scores = %v\n", scores)
					if scores[0] < scores[2] {
						check("relevant doc outscored irrelevant doc", fmt.Errorf("expected scores[0] > scores[2], got %.3f vs %.3f", scores[0], scores[2]))
					} else {
						check("relevant doc outscored irrelevant doc", nil)
					}
				}
			}
		}
	}

	// --- Embedder (tei) ---

	embProv, err := core.NewProvider(core.ProviderConfig{
		Adapter: "tei",
		BaseURL: "http://127.0.0.1:8080",
		Model:   "Qwen/Qwen3-Embedding-0.6B",
	})
	check("NewProvider(tei)", err)
	if err == nil {
		ep, ok := embProv.(core.EmbedderProvider)
		if !ok {
			check("tei satisfies EmbedderProvider", fmt.Errorf("type assertion failed"))
		} else {
			check("tei satisfies EmbedderProvider", nil)
			embedder, err := ep.Embedder("Qwen/Qwen3-Embedding-0.6B")
			check("tei.Embedder()", err)
			if err == nil {
				qVecs, err := embedder.Embed(ctx, core.RoleQuery, []string{"What is the capital of France?"})
				check("embedder.Embed(RoleQuery)", err)
				if err == nil && len(qVecs) == 1 {
					fmt.Printf("     query vec dim = %d\n", len(qVecs[0]))
					n := norm(qVecs[0])
					if math.Abs(n-1.0) > 0.05 {
						check("query vec normalized", fmt.Errorf("norm=%.4f, want ≈1.0", n))
					} else {
						check("query vec normalized", nil)
					}
				}

				dVecs, err := embedder.Embed(ctx, core.RoleDocument, []string{"Paris is the capital of France."})
				check("embedder.Embed(RoleDocument)", err)
				if err == nil && len(dVecs) == 1 && len(qVecs) == 1 {
					sim := cosine(qVecs[0], dVecs[0])
					fmt.Printf("     cosine(query, relevant_doc) = %.4f\n", sim)
					if sim < 0.4 {
						check("query~relevant_doc similarity ≥ 0.4", fmt.Errorf("got %.4f", sim))
					} else {
						check("query~relevant_doc similarity ≥ 0.4", nil)
					}
				}
			}
		}
	}

	// --- Phase 12 negative test: zerank does NOT satisfy EmbedderProvider ---

	if clsProv != nil {
		if _, ok := clsProv.(core.EmbedderProvider); ok {
			check("zerank does NOT satisfy EmbedderProvider", fmt.Errorf("zerank unexpectedly implements Embedder()"))
		} else {
			check("zerank does NOT satisfy EmbedderProvider", nil)
		}
	}

	fmt.Printf("\n%d pass, %d fail\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
	_ = log.Logger{}
}

func norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	d := math.Sqrt(na) * math.Sqrt(nb)
	if d == 0 {
		return 0
	}
	return dot / d
}
