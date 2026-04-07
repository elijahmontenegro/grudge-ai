package agent

// Tool categories and their default permission levels.
const (
	PermAllow = "allow"
	PermAsk   = "ask"
	PermDeny  = "deny"
)

// ToolCategory groups tools by their default permission level.
type ToolCategory struct {
	Name    string
	Tools   []string
	Default string
}

// DefaultCategories returns the spec-defined tool categories.
func DefaultCategories() []ToolCategory {
	return []ToolCategory{
		{Name: "read", Tools: []string{"FileRead", "Glob", "Grep", "WebSearch", "WebFetch", "AskUserQuestion"}, Default: PermAllow},
		{Name: "write", Tools: []string{"FileEdit", "FileWrite", "Bash", "NotebookEdit"}, Default: PermAsk},
		{Name: "agent", Tools: []string{"Agent", "SendMessage", "TaskCreate", "TaskGet", "TaskUpdate", "TaskList", "TaskStop", "TaskOutput"}, Default: PermAsk},
		{Name: "plan", Tools: []string{"EnterPlanMode", "ExitPlanMode"}, Default: PermAsk},
		{Name: "skill", Tools: []string{"Skill", "TodoWrite"}, Default: PermAsk},
	}
}

// DefaultPermission returns the default permission for a tool.
func DefaultPermission(toolName string) string {
	for _, cat := range DefaultCategories() {
		for _, t := range cat.Tools {
			if t == toolName {
				return cat.Default
			}
		}
	}
	return PermAsk
}
