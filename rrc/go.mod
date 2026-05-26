module github.com/emontenegr/spidey/rrc

go 1.25.0

require (
	github.com/emontenegr/spidey/gen/go v0.0.0
	github.com/pkoukk/tiktoken-go v0.1.8
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/dlclark/regexp2 v1.10.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
)

// Replace directive for local development. Required (in addition to
// go.work) because `go build` from a module subdirectory resolves
// dependencies via the module proxy even under workspace mode. When
// gen/go publishes to a registry, drop this.
replace github.com/emontenegr/spidey/gen/go => ../gen/go
