package graph

import (
	"context"
	"errors"

	"github.com/99designs/gqlgen/graphql"
	"github.com/emontenegr/spidey/core"
	"github.com/emontenegr/spidey/rrc"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Error type constants for GraphQL error extensions.
const (
	ErrTypeProviderUnavailable  = "PROVIDER_UNAVAILABLE"
	ErrTypeClassifierUnavailable = "CLASSIFIER_UNAVAILABLE"
	ErrTypeContextLength        = "CONTEXT_LENGTH"
	ErrTypeToolDenied           = "TOOL_DENIED"
	ErrTypeAgentError           = "AGENT_ERROR"
)

// ErrorPresenter maps Go errors to typed GraphQL errors with extensions.
func ErrorPresenter(ctx context.Context, err error) *gqlerror.Error {
	gqlErr := graphql.DefaultErrorPresenter(ctx, err)

	var errType string
	switch {
	case errors.Is(err, core.ErrProviderUnavailable):
		errType = ErrTypeProviderUnavailable
	case errors.Is(err, core.ErrAuth):
		errType = ErrTypeProviderUnavailable
	case errors.Is(err, core.ErrRateLimited):
		errType = ErrTypeProviderUnavailable
	case errors.Is(err, rrc.ErrClassifierFailed):
		errType = ErrTypeClassifierUnavailable
	case errors.Is(err, rrc.ErrClassifierUnavailable):
		errType = ErrTypeClassifierUnavailable
	case errors.Is(err, rrc.ErrMessageNotFound), errors.Is(err, rrc.ErrThreadNotFound):
		errType = ErrTypeAgentError
	case isContextLengthError(err):
		errType = ErrTypeContextLength
	}

	if errType != "" {
		if gqlErr.Extensions == nil {
			gqlErr.Extensions = make(map[string]any)
		}
		gqlErr.Extensions["type"] = errType
	}

	return gqlErr
}

func isContextLengthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "context_length") ||
		contains(msg, "maximum context") ||
		contains(msg, "too many tokens")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsAt(s, substr)
}

func containsAt(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
