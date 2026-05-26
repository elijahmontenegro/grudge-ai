package storage

import (
	"fmt"
	"regexp"
)

// ThreadIDPattern enforces the server-generated thread ID format
// (CreateThread emits `thread-{unixnano}`). Defense-in-depth for
// every callsite that interpolates a client-supplied thread ID into
// a filesystem path: refusing anything else at the boundary prevents
// hard traversal (../etc/foo) and soft traversal whose Clean'd
// result stays inside the root directory but outside the
// thread-{id} convention.
var ThreadIDPattern = regexp.MustCompile(`^thread-\d+$`)

// ValidateThreadID returns nil iff id matches the server-generated
// thread ID format. Returns a descriptive error otherwise. Use this
// at every callsite that interpolates a thread ID into a filesystem
// path.
func ValidateThreadID(id string) error {
	if id == "" {
		return fmt.Errorf("empty threadID")
	}
	if !ThreadIDPattern.MatchString(id) {
		return fmt.Errorf("invalid threadID format: %q", id)
	}
	return nil
}
