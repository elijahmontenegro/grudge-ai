package httpc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/emontenegr/grudge/core"
)

const (
	TimeoutDefault   = 30 * time.Second
	TimeoutStreaming = 120 * time.Second

	// TimeoutTEI bounds a single TEI /rerank or /embed HTTP call.
	// The default 30s was too tight — healthy reranks are sub-second,
	// but a legitimately-queued batch on a shared GPU can take 30-90s
	// to drain. 120s tolerates realistic queue depth without masking
	// a true wedge (which appears as indefinite silence; we let the
	// docker-compose healthcheck's canary /rerank probe detect that
	// and restart the container). The resilience-layer retry above
	// this composes cleanly: one wedge-then-restart cycle fits
	// inside the retry budget.
	TimeoutTEI = 120 * time.Second

	// StreamingHeaderTimeout bounds how long we wait for the upstream
	// LLM to start producing bytes (TTFB). Past this, the model is
	// effectively wedged. The body itself is not time-limited — a
	// long completion streaming for minutes is normal.
	StreamingHeaderTimeout = 120 * time.Second
)

// Client is a shared HTTP client used by all provider adapters. Auth is
// injected at construction via a callback — each adapter provides its own
// (Anthropic sets x-api-key, OpenAI sets Authorization: Bearer, etc.).
type Client struct {
	http   *http.Client
	authFn func(req *http.Request)
}

// New creates a Client with the given timeout and auth callback. Pass nil
// for authFn if the provider requires no authentication (e.g. local Ollama).
// The timeout is a whole-request deadline — appropriate for non-streaming
// JSON calls, inappropriate for streaming (see NewStreaming).
func New(timeout time.Duration, authFn func(*http.Request)) *Client {
	return &Client{
		http: &http.Client{
			Timeout: timeout,
		},
		authFn: authFn,
	}
}

// NewStreaming builds a client for streaming LLM calls. Critically it
// does NOT set http.Client.Timeout, which is a whole-request deadline
// that includes reading the response body — a 120s cap there killed
// autonomous loops mid-stream once a response took longer than two
// minutes to arrive or finish. Instead we bound the TTFB via
// Transport.ResponseHeaderTimeout; once headers arrive, the body can
// stream indefinitely. Overall cancellation is the caller's
// responsibility via context.
func NewStreaming(authFn func(*http.Request)) *Client {
	return &Client{
		http: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: StreamingHeaderTimeout,
				// Explicit defaults copied from http.DefaultTransport
				// so we don't accidentally drop connection pooling.
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   10,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
		authFn: authFn,
	}
}

// Do executes an HTTP request with auth applied. It maps HTTP
// errors to typed StatusError values: 401/403/429 are recognized
// here so authentication and rate-limit handling don't depend on
// every adapter checking the same status codes. Other non-2xx
// responses pass through unchanged — adapters typically need to
// read the body for diagnostic output and produce their own
// StatusError after that.
//
// Connection-level failures wrap core.ErrProviderUnavailable.
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	req = req.WithContext(ctx)
	if c.authFn != nil {
		c.authFn(req)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", core.ErrProviderUnavailable, err)
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		resp.Body.Close()
		return nil, &StatusError{StatusCode: resp.StatusCode}
	case http.StatusTooManyRequests:
		resp.Body.Close()
		return nil, &StatusError{StatusCode: resp.StatusCode}
	}

	return resp, nil
}

// DoJSON executes a request and returns the response body bytes. The caller
// is responsible for checking resp status codes beyond auth/rate-limit.
func (c *Client) DoJSON(ctx context.Context, req *http.Request) ([]byte, int, error) {
	resp, err := c.Do(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%w: reading response: %v", core.ErrProviderUnavailable, err)
	}

	return body, resp.StatusCode, nil
}
