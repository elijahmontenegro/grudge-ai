// Package secrets owns OS-keyring-backed secret storage. Separate
// from service/config because secret storage is a security concern,
// not a configuration concern — config is JSON on disk; secrets live
// behind the OS credential store. The Store interface lets tests
// substitute an in-memory implementation.
package secrets

import "github.com/zalando/go-keyring"

const keyringService = "grudge"

// Store reads and writes secrets keyed by name. Backed by the OS
// keyring in production; tests substitute MemoryStore.
type Store interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
}

// keyringStore is the OS-keyring implementation. Methods delegate to
// github.com/zalando/go-keyring under a single service prefix so all
// of grudge's secrets live in one credential-store namespace.
type keyringStore struct{}

// NewKeyringStore returns a Store backed by the OS credential store.
func NewKeyringStore() Store { return &keyringStore{} }

func (k *keyringStore) Get(name string) (string, error) { return keyring.Get(keyringService, name) }
func (k *keyringStore) Set(name, value string) error    { return keyring.Set(keyringService, name, value) }
func (k *keyringStore) Delete(name string) error        { return keyring.Delete(keyringService, name) }

// MemoryStore is an in-memory Store for tests. Not goroutine-safe;
// tests run serially.
type MemoryStore struct{ data map[string]string }

// NewMemoryStore returns an empty in-memory Store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{data: map[string]string{}} }

func (m *MemoryStore) Get(name string) (string, error) {
	if v, ok := m.data[name]; ok {
		return v, nil
	}
	return "", keyring.ErrNotFound
}
func (m *MemoryStore) Set(name, value string) error { m.data[name] = value; return nil }
func (m *MemoryStore) Delete(name string) error     { delete(m.data, name); return nil }

// Default is the package-level Store used by callers who don't
// thread a specific instance through. Set at boot via SetDefault;
// tests override with NewMemoryStore.
var defaultStore Store = NewKeyringStore()

// SetDefault overrides the package-level store. Boot wiring (or
// tests) call this before any consumer reads a secret.
func SetDefault(s Store) { defaultStore = s }

// Get reads from the default Store. Convenience for callers that
// don't need a custom Store instance.
func Get(name string) (string, error) { return defaultStore.Get(name) }

// Set writes to the default Store.
func Set(name, value string) error { return defaultStore.Set(name, value) }

// Delete removes from the default Store.
func Delete(name string) error { return defaultStore.Delete(name) }
