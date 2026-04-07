package httpc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/emontenegr/spidey/core"
)

const (
	TimeoutDefault   = 30 * time.Second
	TimeoutStreaming = 120 * time.Second
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
func New(timeout time.Duration, authFn func(*http.Request)) *Client {
	return &Client{
		http: &http.Client{
			Timeout: timeout,
		},
		authFn: authFn,
	}
}

// Do executes an HTTP request with auth applied. It maps HTTP errors to
// core sentinel errors: 401/403 → ErrAuth, 429 → ErrRateLimited,
// connection failures → ErrProviderUnavailable.
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
		return nil, core.ErrAuth
	case http.StatusTooManyRequests:
		resp.Body.Close()
		return nil, core.ErrRateLimited
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
