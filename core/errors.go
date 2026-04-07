package core

import "errors"

var (
	ErrUnsupported         = errors.New("capability not supported by provider")
	ErrAuth                = errors.New("authentication failed")
	ErrProviderUnavailable = errors.New("provider endpoint unreachable")
	ErrRateLimited         = errors.New("provider rate limit exceeded")
)
