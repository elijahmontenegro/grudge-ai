package googleai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/elijahmontenegro/grudge/core"
)

// New constructs a working provider against a scripted endpoint via
// BaseURL (the API-key backend honors HTTPOptions.BaseURL, same as
// the SDK's Vertex path) and satisfies both provider roles.
func TestNew_ConstructsWithBaseURLAndSatisfiesRoles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	p, err := New(core.ProviderConfig{APIKey: "test-key", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := p.(interface {
		Completer(string) (core.Completer, error)
	}).Completer("gemini-test"); err != nil {
		t.Fatalf("Completer: %v", err)
	}
	if _, err := p.(interface {
		Embedder(string) (core.Embedder, error)
	}).Embedder("gemini-embed-test"); err != nil {
		t.Fatalf("Embedder: %v", err)
	}
}
