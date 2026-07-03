// Package eval ships the labeled prerequisite seed set with the binary.
//
// pairs.json is the canonical eval set (categories A–F: a query, its true
// prerequisite, and distractors). The Python eval harness reads it by path;
// the running system embeds it here so self-calibration (service/calibration)
// works out of the box with zero user setup — no repo checkout, no file to
// point at. One canonical file, two consumers.
package eval

import _ "embed"

//go:embed pairs.json
var SeedPairs []byte

// Seed returns the embedded labeled seed set (raw JSON).
func Seed() []byte { return SeedPairs }
