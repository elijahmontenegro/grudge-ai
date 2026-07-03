// Package gcpauth centralizes Application Default Credentials wiring shared
// by the GCP adapters (vertex, gcpranking). Internal to core/adapter so only
// adapters depend on it.
package gcpauth

import (
	"context"
	"net/http"
	"os"

	"golang.org/x/oauth2/google"
)

// CloudPlatformScope is the broad scope both Vertex AI and Discovery Engine
// accept.
const CloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// BearerAuthFn returns an httpc auth callback that stamps a fresh ADC bearer
// token on each request. The underlying oauth2 TokenSource is self-caching
// and self-refreshing, so calling Token() per request is cheap. A token
// error is swallowed (the request proceeds unauthenticated and the server's
// 401 surfaces through the normal error path) rather than panicking in a
// callback that cannot return an error.
func BearerAuthFn(ctx context.Context) (func(*http.Request), error) {
	ts, err := google.DefaultTokenSource(ctx, CloudPlatformScope)
	if err != nil {
		return nil, err
	}
	return func(req *http.Request) {
		if tok, err := ts.Token(); err == nil {
			req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		}
	}, nil
}

// Project resolves the GCP project id from an explicit value, then the
// standard environment variable. Returns "" if unset.
func Project(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return os.Getenv("GOOGLE_CLOUD_PROJECT")
}

// Location resolves a GCP location from an explicit value, then the standard
// environment variables, then the provided default.
func Location(explicit, fallback string) string {
	for _, v := range []string{explicit, os.Getenv("GOOGLE_CLOUD_LOCATION"), os.Getenv("GOOGLE_CLOUD_REGION")} {
		if v != "" {
			return v
		}
	}
	return fallback
}
