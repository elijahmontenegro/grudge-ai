// Package annindex is a pure-Go approximate-nearest-neighbour index over
// the chunk-vector store. It exists to restore RRC's corpus-invariance
// promise: brute-force vec0 KNN is O(N) per lookup (measured in
// service/rrcbench), and a graph index makes it ~O(log N).
//
// The design is three separable pieces:
//   - an HNSW graph (hnsw.go) — the sub-linear structure; this is what
//     makes per-step cost stop tracking corpus size.
//   - binary quantization (this file) — sign-bit codes with Hamming
//     distance, ~32x smaller than float32 so the in-RAM graph is
//     shippable on a local-first machine, and each hop is a popcount.
//   - a float rerank (done by the caller, service/oracle) — the graph
//     over-fetches a shortlist, which is then rescored against exact
//     float vectors from vec0. The index only has to surface the right
//     candidates; the exact rescore fixes their order.
//
// Approximate retrieval is safe for RRC by construction: the vector
// lookup only proposes candidates (the cross-encoder + calibrated
// acceptance do the real scoring, and low-similarity roots arrive via the
// provenance graph, not vectors).
package annindex

import (
	"math"
	"math/bits"
)

// Binarize maps a float32 embedding to its sign-bit binary code, packed
// 64 dimensions per uint64 word. Bit i is set iff vec[i] >= 0.
//
// This is the standard cosine->Hamming quantization: for the normalized
// embeddings this store holds, Hamming distance on sign bits is a monotone
// proxy for angular distance — coarse, but exact enough to shortlist
// candidates that the caller's float rerank then orders precisely.
func Binarize(vec []float32) []uint64 {
	code := make([]uint64, (len(vec)+63)/64)
	for i, v := range vec {
		if v >= 0 {
			code[i>>6] |= 1 << uint(i&63)
		}
	}
	return code
}

// Hamming is the number of differing bits between two equal-length codes —
// the distance the graph navigates under. XOR + hardware popcount.
func Hamming(a, b []uint64) int {
	var d int
	for i := range a {
		d += bits.OnesCount64(a[i] ^ b[i])
	}
	return d
}

// QuantizeInt8 scalar-quantizes a vector to int8 with a per-vector scale:
// code[i] = round(v[i]/scale), scale = max|v|/127, so every vector uses the
// full int8 range and dequant(code)[i] = code[i]*scale reconstructs v[i]
// closely. Per-vector (not a shared) scale keeps one dimension's outlier from
// crushing the precision of every other vector. Returns the codes and the
// scale to dequantize them. int8 is 1 KB/vec (4x smaller than float32) — the
// rerank code, held in RAM, never fetched.
func QuantizeInt8(vec []float32) ([]int8, float64) {
	var maxAbs float64
	for _, x := range vec {
		if a := math.Abs(float64(x)); a > maxAbs {
			maxAbs = a
		}
	}
	code := make([]int8, len(vec))
	if maxAbs == 0 {
		return code, 0
	}
	scale := maxAbs / 127
	for i, x := range vec {
		q := math.Round(float64(x) / scale)
		if q > 127 {
			q = 127
		} else if q < -128 {
			q = -128
		}
		code[i] = int8(q)
	}
	return code, scale
}

// AsymmetricInt8Score scores a float query against an int8-quantized vector:
// scale · Σ query[i]·code[i] ≈ query·vector. It keeps the query in full
// precision and the database vector in int8, so it tracks cosine closely
// while touching only the in-RAM code — no float re-fetch. Higher is nearer.
func AsymmetricInt8Score(query []float32, code []int8, scale float64) float64 {
	var s float64
	for i, q := range query {
		s += float64(q) * float64(code[i])
	}
	return s * scale
}
