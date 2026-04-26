package agent

import (
	"fmt"
	"strings"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Skill / TodoWrite — both consume from the agent's deps and surface
// either skill content (Skill) or a structured task list (TodoWrite).
// Skill substitutes `$ARGUMENTS` in the skill body the same way
// claude-code's slash-command skills do.

func registerSkillTools(c *buildCtx) error {
	skill, err := functiontool.New(
		functiontool.Config{Name: "Skill", Description: "Invoke a skill by name with optional arguments. Skill content is injected as context."},
		func(ctx tool.Context, args SkillArgs) (SkillResult, error) {
			if err := c.requireApproval(ctx, "Skill", marshalArgs(args)); err != nil {
				return SkillResult{}, err
			}
			for _, s := range c.deps.Skills {
				if s.Name == args.Name {
					content := s.Content
					if args.Args != "" {
						content = strings.Replace(content, "$ARGUMENTS", args.Args, -1)
					}
					return SkillResult{Output: content}, nil
				}
			}
			return SkillResult{}, fmt.Errorf("skill '%s' not found", args.Name)
		},
	)
	if err := c.addTool("Skill", skill, err); err != nil {
		return err
	}

	todoWrite, err := functiontool.New(
		functiontool.Config{Name: "TodoWrite", Description: "Create a structured task list for tracking work."},
		func(ctx tool.Context, args TodoWriteArgs) (TodoWriteResult, error) {
			if c.deps.Tasks == nil {
				return TodoWriteResult{}, fmt.Errorf("task store not available")
			}
			for _, desc := range args.Tasks {
				c.deps.Tasks.Create(desc, desc, "todo")
			}
			return TodoWriteResult{Success: true}, nil
		},
	)
	return c.addTool("TodoWrite", todoWrite, err)
}
