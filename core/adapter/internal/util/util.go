// Package util holds tiny helpers shared across the built-in provider
// adapters. Internal to core/adapter so only adapters depend on it; not part
// of the public core surface.
package util

// Ptr returns a pointer to v. Adapters use it for the optional string fields
// in proto stream chunks (e.g. terminal-error messages) where the wire type
// is *string. Replaces the per-adapter `func ptr(s string) *string` copies.
func Ptr[T any](v T) *T { return &v }
