package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/emontenegr/grudge/service/sandbox"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Filesystem search tools — Glob and Grep. On sandboxed threads
// these route through the container; the absolute-path guard in
// Grep prevents the model from sneaking host paths past the
// workspace boundary.

func registerSearchTools(c *buildCtx) error {
	glob, err := functiontool.New(
		functiontool.Config{Name: "Glob", Description: descriptionFor("Glob")},
		func(ctx tool.Context, args GlobArgs) (GlobResult, error) {
			dir, err := c.resolvePath(args.Path)
			if err != nil {
				return GlobResult{}, err
			}
			if dir == "" {
				dir = c.baseDir
			}
			matches, _ := filepath.Glob(filepath.Join(dir, args.Pattern))
			return GlobResult{Files: matches}, nil
		},
	)
	if err := c.addTool("Glob", glob, err); err != nil {
		return err
	}

	grep, err := functiontool.New(
		functiontool.Config{Name: "Grep", Description: descriptionFor("Grep")},
		func(ctx tool.Context, args GrepArgs) (GrepResult, error) {
			var dir string
			if c.deps.Sandboxed {
				rel := strings.TrimPrefix(args.Path, sandbox.ContainerWorkspace)
				rel = strings.TrimPrefix(rel, "/")
				if filepath.IsAbs(args.Path) && !strings.HasPrefix(args.Path, sandbox.ContainerWorkspace) {
					return GrepResult{}, fmt.Errorf("path %q outside workspace", args.Path)
				}
				if rel == "" {
					dir = sandbox.ContainerWorkspace
				} else {
					dir = sandbox.ContainerWorkspace + "/" + rel
				}
			} else {
				dir = args.Path
				if dir == "" {
					dir = c.baseDir
				}
			}
			out, _ := c.execCmd(ctx, fmt.Sprintf("grep -rn %q %s", args.Pattern, dir), "")
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) == 1 && lines[0] == "" {
				lines = nil
			}
			return GrepResult{Matches: lines}, nil
		},
	)
	return c.addTool("Grep", grep, err)
}
