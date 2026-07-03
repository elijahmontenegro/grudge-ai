// Package seed ships the labeled prerequisite seed set with the library.
//
// pairs.json is the canonical seed set (categories A–F: a query, its true
// prerequisite, and distractors). It lives beside the fitting code
// (rrc/calibrate) because self-calibration is a library capability: any
// importer of the engine gets a working cold-start fit with zero setup —
// no repo checkout, no file to point at. The Python eval harness under
// eval/ reads this same file by path. One canonical file.
package seed

import _ "embed"

//go:embed pairs.json
var pairs []byte

// Pairs returns the embedded labeled seed set (raw JSON).
func Pairs() []byte { return pairs }
