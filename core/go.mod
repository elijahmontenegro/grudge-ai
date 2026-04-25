module github.com/emontenegr/spidey/core

go 1.25.0

require github.com/emontenegr/spidey/gen/go v0.0.0

require google.golang.org/protobuf v1.36.11 // indirect

// Replace directive for local development. Required (in addition to
// go.work) because `go build` from a module subdirectory resolves
// dependencies via the module proxy even under workspace mode — the
// workspace `use ./gen/go` clause does not satisfy this lookup. When
// gen/go publishes to a registry, drop this.
replace github.com/emontenegr/spidey/gen/go => ../gen/go
