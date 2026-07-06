package rrcbench

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sort"
	"testing"

	"github.com/elijahmontenegro/grudge/service/annindex"
)

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func l2normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(n))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// TestANNRecallRealEmbeddings measures binary-shortlist + cosine-rerank
// recall on REAL Qwen3-Embedding-0.6B vectors read from a dev database —
// the actual embedding geometry, not a synthetic stand-in whose difficulty
// I get to choose. This is the only measurement that settles whether 1-bit
// quantization is good enough for this embedder. Gated behind RRC_REAL_DB
// (path to a spidey/grudge DB) so it self-skips where no real corpus exists.
// Opened read-only + immutable: it never writes or locks the user's data.
func TestANNRecallRealEmbeddings(t *testing.T) {
	path := os.Getenv("RRC_REAL_DB")
	if path == "" {
		t.Skip("set RRC_REAL_DB=<path to spidey.db> to measure recall on real Qwen3 vectors")
	}
	db, err := sql.Open("sqlite3", "file:"+path+"?_pragma=trusted_schema(ON)&mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(
		`SELECT message_id, chunk_index, embedding FROM chunk_vectors WHERE model_id = ?`,
		"Qwen/Qwen3-Embedding-0.6B")
	if err != nil {
		t.Fatalf("query real embeddings: %v", err)
	}
	defer rows.Close()

	var keys []string
	vecs := make(map[string][]float32)
	ix := annindex.New(annindex.Config{Seed: 1})
	for rows.Next() {
		var mid string
		var cidx int
		var blob []byte
		if err := rows.Scan(&mid, &cidx, &blob); err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("%s:%d", mid, cidx)
		v := l2normalize(decodeVec(blob))
		keys = append(keys, key)
		vecs[key] = v
		ix.Add(key, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(keys) < 50 {
		t.Fatalf("only %d real vectors — too few to measure recall", len(keys))
	}

	const k = 10
	overfetches := []int{2, 4, 8, 16, 32}
	type ks struct {
		key string
		s   float64
	}

	// Precompute each query's true cosine top-k once (excluding self).
	truthOf := make(map[string]map[string]bool, len(keys))
	for _, qk := range keys {
		qv := vecs[qk]
		arr := make([]ks, 0, len(keys)-1)
		for _, key := range keys {
			if key != qk {
				arr = append(arr, ks{key, dot(qv, vecs[key])})
			}
		}
		sort.Slice(arr, func(a, b int) bool { return arr[a].s > arr[b].s })
		kk := min(k, len(arr))
		tr := make(map[string]bool, kk)
		for i := range kk {
			tr[arr[i].key] = true
		}
		truthOf[qk] = tr
	}

	t.Logf("REAL Qwen3-Embedding-0.6B: recall@%d vs binary-shortlist over-fetch, %d vectors", k, len(keys))
	for _, of := range overfetches {
		var hit, total int
		for _, qk := range keys {
			qv := vecs[qk]
			// Search already reranks the Hamming shortlist by asymmetric score;
			// take its top-k (excluding self) directly — no float rerank.
			short := ix.Search(qv, k*of)
			truth := truthOf[qk]
			seen := 0
			for _, c := range short {
				if c.Key == qk {
					continue
				}
				if truth[c.Key] {
					hit++
				}
				if seen++; seen == k {
					break
				}
			}
			total += len(truth)
		}
		t.Logf("  over-fetch x%-3d (shortlist %-4d) -> recall@%d = %.4f", of, k*of, k, float64(hit)/float64(total))
	}

	// Ceiling for a finer rerank code: int8 with a global scale preserves the
	// vectors' magnitudes, so q_float · dequant(int8) tracks cosine closely.
	// This is the recall available if the rerank code escalates from 1-bit to
	// int8 (1 KB/vec — still compact, still in RAM, still no disk fetch).
	var maxAbs float32
	for _, v := range vecs {
		for _, x := range v {
			if a := float32(math.Abs(float64(x))); a > maxAbs {
				maxAbs = a
			}
		}
	}
	scale := float32(127) / maxAbs
	int8codes := make(map[string][]int8, len(keys))
	for key, v := range vecs {
		c := make([]int8, len(v))
		for i, x := range v {
			c[i] = int8(x * scale)
		}
		int8codes[key] = c
	}
	var hit, total int
	for _, qk := range keys {
		qv := vecs[qk]
		arr := make([]ks, 0, len(keys)-1)
		for _, key := range keys {
			if key == qk {
				continue
			}
			c := int8codes[key]
			var s float64
			for i := range qv {
				s += float64(qv[i]) * float64(c[i])
			}
			arr = append(arr, ks{key, s})
		}
		sort.Slice(arr, func(a, b int) bool { return arr[a].s > arr[b].s })
		truth := truthOf[qk]
		for i := range min(k, len(arr)) {
			if truth[arr[i].key] {
				hit++
			}
		}
		total += len(truth)
	}
	t.Logf("  int8 asymmetric (brute force, 1 KB/vec)      -> recall@%d = %.4f", k, float64(hit)/float64(total))
}
