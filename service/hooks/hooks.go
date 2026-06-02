package hooks

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/emontenegr/grudge/service/config"
)

// Event types for lifecycle hooks.
const (
	PreToolUse       = "PreToolUse"
	PostToolUse      = "PostToolUse"
	ThreadStart      = "ThreadStart"
	ThreadEnd        = "ThreadEnd"
	PreCarryForward  = "PreCarryForward"
	PostCarryForward = "PostCarryForward"
	UserPromptSubmit = "UserPromptSubmit"
)

// Result from hook execution.
type Result struct {
	Blocked bool   // true if the hook denied the action
	Output  string // stdout/stderr from the hook
}

// Dispatcher executes lifecycle hooks. Fail-closed: hook failure blocks the action.
type Dispatcher struct {
	hooks []config.HookConfig
}

// NewDispatcher creates a hook dispatcher from configuration.
func NewDispatcher(hooks []config.HookConfig) *Dispatcher {
	return &Dispatcher{hooks: hooks}
}

// Fire executes all hooks matching the event and optional tool name.
// Returns error if any hook fails (fail-closed).
func (d *Dispatcher) Fire(ctx context.Context, event, toolName string) (*Result, error) {
	for _, h := range d.hooks {
		if h.Event != event {
			continue
		}

		if h.Match != "" && !matchGlob(h.Match, toolName) {
			continue
		}

		timeout := 10 * time.Second
		if h.Timeout != "" {
			if t, err := time.ParseDuration(h.Timeout); err == nil {
				timeout = t
			}
		}

		hookCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(hookCtx, "sh", "-c", h.Command)
		output, err := cmd.CombinedOutput()
		cancel()

		if err != nil {
			return &Result{
				Blocked: true,
				Output:  string(output),
			}, fmt.Errorf("hook %s (%s) failed: %w\nOutput: %s", event, h.Command, err, output)
		}

		// Check if hook output indicates denial
		outStr := strings.TrimSpace(string(output))
		if strings.HasPrefix(outStr, "DENY") {
			return &Result{Blocked: true, Output: outStr}, nil
		}
	}

	return &Result{}, nil
}

func matchGlob(pattern, name string) bool {
	matched, _ := filepath.Match(pattern, name)
	return matched
}
