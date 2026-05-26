package tools

// Tool argument and result types — split out of tools.go so the shape
// of every tool's wire-format is navigable in one file. Function
// implementations live in tools.go alongside BuildTools; a future
// move splits those by family into tools/ subpackages.

type BashArgs struct {
	Command string `json:"command"`
}
type BashResult struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

type FileReadArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type FileReadResult struct {
	Output string `json:"output"`
}

type FileEditArgs struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}
type FileEditResult struct {
	Success bool `json:"success"`
}

type FileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type FileWriteResult struct {
	Success bool   `json:"success"`
	Path    string `json:"path,omitempty"`
}

type GlobArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}
type GlobResult struct {
	Files []string `json:"files"`
}

type GrepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}
type GrepResult struct {
	Matches []string `json:"matches"`
}

type NotebookEditArgs struct {
	Path    string `json:"path"`
	CellIdx int    `json:"cell_index"`
	Content string `json:"content"`
}
type NotebookEditResult struct {
	Success bool `json:"success"`
}

type WebSearchArgs struct {
	Query string `json:"query"`
}
type WebSearchResult struct {
	Results []string `json:"results"`
}

type WebFetchArgs struct {
	URL string `json:"url"`
}
type WebFetchResult struct {
	Output     string `json:"output"`
	StatusCode int    `json:"status_code"`
}

// AskUserOption is one choice offered for a single question. Label is
// what's shown; Description is an optional clarifier beneath the label.
type AskUserOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// AskUserQuestion carries one prompt. Structure mirrors Claude Code's
// AskUserQuestion tool so the same prompting conventions carry over:
// a short `header` (like a setting name), the question itself, optional
// `options` (2–4 concise choices), and `multiSelect` when multiple
// answers are allowed at once.
type AskUserQuestion struct {
	Question    string          `json:"question"`
	Header      string          `json:"header,omitempty"`
	Options     []AskUserOption `json:"options,omitempty"`
	MultiSelect bool            `json:"multiSelect,omitempty"`
}

// AskUserArgs accepts 1–4 questions in a single tool call. The UI walks
// through them sequentially and batches answers back as one map.
type AskUserArgs struct {
	Questions []AskUserQuestion `json:"questions"`
}

// AskUserResult returns the user's answers keyed by question text so the
// model can correlate each answer with the question it corresponds to
// without positional matching.
type AskUserResult struct {
	Answers map[string]string `json:"answers"`
}

type SkillArgs struct {
	Name string `json:"name"`
	Args string `json:"args,omitempty"`
}
type SkillResult struct {
	Output string `json:"output"`
}

type TodoWriteArgs struct {
	Tasks []string `json:"tasks"`
}
type TodoWriteResult struct {
	Success bool `json:"success"`
}

type PlanModeArgs struct {
	ThreadID string `json:"thread_id"`
}
type PlanModeResult struct {
	Success bool `json:"success"`
}

type AgentToolArgs struct {
	Task        string `json:"task"`
	Description string `json:"description,omitempty"`
}
type AgentToolResult struct {
	Result string `json:"result"`
}

type SendMessageArgs struct {
	To      string `json:"to"`
	Message string `json:"message"`
}
type SendMessageResult struct {
	Response string `json:"response"`
}

type TaskCreateArgs struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
}
type TaskCreateResult struct {
	TaskID string `json:"task_id"`
}

type TaskGetArgs struct {
	TaskID string `json:"task_id"`
}
type TaskGetResult struct {
	Subject string `json:"subject"`
	Status  string `json:"status"`
}

type TaskUpdateArgs struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}
type TaskUpdateResult struct {
	Success bool `json:"success"`
}

type TaskListArgs struct{}
type TaskListResult struct {
	Tasks []string `json:"tasks"`
}

type TaskStopArgs struct {
	TaskID string `json:"task_id"`
}
type TaskStopResult struct {
	Success bool `json:"success"`
}

type TaskOutputArgs struct {
	TaskID string `json:"task_id"`
}
type TaskOutputResult struct {
	Output string `json:"output"`
}
