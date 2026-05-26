package tools

import (
	"github.com/emontenegr/spidey/service/sandbox"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Bash — execute a shell command. Sandboxed threads route through
// sandbox.Sandbox; non-sandboxed threads run on the host with the
// destructive-command safety net (the sandbox boundary already
// scopes destruction inside a container, so the detector would
// only reject legitimate sandbox-internal cleanups).

func registerBashTool(c *buildCtx) error {
	bash, err := functiontool.New(
		functiontool.Config{Name: "Bash", Description: "Execute a shell command. Returns stdout/stderr and exit code."},
		func(ctx tool.Context, args BashArgs) (BashResult, error) {
			if err := c.planGuard(""); err != nil {
				return BashResult{Output: err.Error(), ExitCode: 1}, nil
			}
			if err := c.fireHook(ctx, "PreToolUse", "Bash"); err != nil {
				return BashResult{Output: err.Error(), ExitCode: 1}, nil
			}
			if err := c.requireApproval(ctx, "Bash", marshalArgs(args)); err != nil {
				return BashResult{Output: err.Error(), ExitCode: 1}, nil
			}
			if !c.deps.Sandboxed && sandbox.DetectDestructive(args.Command) {
				return BashResult{Output: "destructive command blocked by safety net (rm / git push / git reset --hard / git checkout --). This is not recoverable from approval — reshape the command (e.g. use a more surgical git operation, or remove files via a safer tool).", ExitCode: 1}, nil
			}
			out, code := c.execCmd(ctx, args.Command, c.baseDir)
			c.fireHook(ctx, "PostToolUse", "Bash")
			return BashResult{Output: out, ExitCode: code}, nil
		},
	)
	return c.addTool("Bash", bash, err)
}
