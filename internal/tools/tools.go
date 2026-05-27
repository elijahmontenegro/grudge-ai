//go:build tools

// Package tools imports build-time tool dependencies so `go mod tidy`
// keeps their transitive go.sum entries tracked. This file is never
// compiled in normal builds (the `tools` build tag is never set
// elsewhere); it exists solely so go mod knows the tool binaries are
// dependencies of the repository.
//
// Run the tools via Taskfile targets:
//   - task gqlgen        — regenerate GraphQL bindings
//   - task gqlgen:check  — verify generated output is up to date
package tools

import (
	_ "github.com/99designs/gqlgen"
)
