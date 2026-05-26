package tools

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/emontenegr/spidey/service/adoc"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Plan tools — EnterPlanMode flips agent.mode to "plan" and seeds
// the plan directory; ExitPlanMode compiles the plan adoc artifact
// and surfaces its content to the UI. Mode does NOT auto-flip back
// to Normal here — the user explicitly approves/rejects via
// approvePlan/rejectPlan mutations, so "no action = keep iterating"
// remains a valid state.

func registerPlanTools(c *buildCtx) error {
	enterPlan, err := functiontool.New(
		functiontool.Config{Name: "EnterPlanMode", Description: "Enter plan mode. Write tools disabled, read tools available. Explore codebase and design implementation approach."},
		func(ctx tool.Context, args PlanModeArgs) (PlanModeResult, error) {
			if err := c.requireApproval(ctx, "EnterPlanMode", marshalArgs(args)); err != nil {
				return PlanModeResult{}, err
			}
			if err := c.deps.Mode.SetMode(ctx, "plan"); err != nil {
				return PlanModeResult{}, err
			}
			if c.deps.PlanDir != "" {
				slug := fmt.Sprintf("plan-%s", c.deps.ThreadID)
				planPath := filepath.Join(c.deps.PlanDir, slug)
				os.MkdirAll(planPath, 0o755)
			}
			return PlanModeResult{Success: true}, nil
		},
	)
	if err := c.addTool("EnterPlan", enterPlan, err); err != nil {
		return err
	}

	exitPlan, err := functiontool.New(
		functiontool.Config{Name: "ExitPlanMode", Description: "Signal that the plan is ready for user review. Do NOT paraphrase or re-summarize the plan — the user sees it surfaced in the artifacts panel. A brief one-line acknowledgment is enough."},
		func(ctx tool.Context, args PlanModeArgs) (PlanModeResult, error) {
			if err := c.requireApproval(ctx, "ExitPlanMode", marshalArgs(args)); err != nil {
				return PlanModeResult{}, err
			}
			var planContent string
			if c.deps.PlanDir != "" {
				slug := fmt.Sprintf("plan-%s", c.deps.ThreadID)
				planEntry := filepath.Join(c.deps.PlanDir, slug, "plan.adoc")
				if compiled, err := adoc.Compile(planEntry); err == nil && compiled != "" {
					planContent = compiled
				}
			}
			if planContent != "" {
				c.deps.Mode.OnPlanContent(planContent)
			}
			return PlanModeResult{Success: true}, nil
		},
	)
	return c.addTool("ExitPlan", exitPlan, err)
}

type PlanModeArgs struct {
	ThreadID string `json:"thread_id"`
}
type PlanModeResult struct {
	Success bool `json:"success"`
}
