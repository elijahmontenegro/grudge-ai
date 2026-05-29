package tools

import (
	"fmt"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// AskUserQuestion — synchronous user prompt. The runner forwards
// the request through deps.AskCh; the UI renders the question(s)
// and posts the answers back through respCh. Validation runs here
// because ADK's FunctionTool can't carry Zod-style constraints
// through JSON schema — the validator is the contract.

// askUserExample is an inline valid payload the validator pastes
// into error messages. Smaller Ollama models frequently emit
// `{"questions": null}` or skip required sub-fields on the first
// try; seeing a fully-formed example on the error path lets them
// copy-adapt the structure on retry instead of looping on the same
// malformed call.
const askUserExample = `Valid example:
{"questions":[
  {"header":"Coffee","question":"How do you take your coffee?","multiSelect":false,"options":[
    {"label":"Black","description":"No milk, no sugar."},
    {"label":"With milk","description":"Splash of milk, no sugar."},
    {"label":"Sweetened","description":"Sugar or syrup added."}
  ]}
]}`

// validateAskUserArgs enforces the Claude Code AskUserQuestion
// schema at runtime. ADK's FunctionTool doesn't carry Zod-style
// constraints through the JSON schema — validation happens here.
// Every error includes the full valid example so the model can
// self-correct on the very next call instead of re-emitting the
// same malformed args.
func validateAskUserArgs(args AskUserArgs) error {
	n := len(args.Questions)
	if n == 0 {
		return fmt.Errorf(
			"AskUserQuestion: `questions` is required and must contain 1–4 items. "+
				"Do NOT pass `null` or omit the field — always include a fully-populated array.\n\n%s",
			askUserExample)
	}
	if n > 4 {
		return fmt.Errorf("AskUserQuestion: too many questions (%d) — cap is 4.\n\n%s", n, askUserExample)
	}
	seenQ := make(map[string]struct{}, n)
	for i, q := range args.Questions {
		if q.Question == "" {
			return fmt.Errorf("AskUserQuestion: questions[%d].question is required.\n\n%s", i, askUserExample)
		}
		if _, dup := seenQ[q.Question]; dup {
			return fmt.Errorf("AskUserQuestion: questions[%d] duplicates a previous question text.\n\n%s", i, askUserExample)
		}
		seenQ[q.Question] = struct{}{}
		if q.Header == "" {
			return fmt.Errorf(
				"AskUserQuestion: questions[%d].header is required (short chip label ≤20 chars, like \"Coffee\" or \"Library\").\n\n%s",
				i, askUserExample)
		}
		if len(q.Options) < 2 || len(q.Options) > 4 {
			return fmt.Errorf(
				"AskUserQuestion: questions[%d] must have 2–4 options (got %d). Each option needs a `label` and a `description`.\n\n%s",
				i, len(q.Options), askUserExample)
		}
		seenLabel := make(map[string]struct{}, len(q.Options))
		for j, opt := range q.Options {
			if opt.Label == "" {
				return fmt.Errorf("AskUserQuestion: questions[%d].options[%d].label is required.\n\n%s", i, j, askUserExample)
			}
			if _, dup := seenLabel[opt.Label]; dup {
				return fmt.Errorf("AskUserQuestion: questions[%d].options[%d] duplicates a previous label.\n\n%s", i, j, askUserExample)
			}
			seenLabel[opt.Label] = struct{}{}
		}
	}
	return nil
}

func registerAskUserTool(c *buildCtx) error {
	askUser, err := functiontool.New(
		functiontool.Config{
			Name:        "AskUserQuestion",
			Description: descriptionFor("AskUserQuestion"),
		},
		func(ctx tool.Context, args AskUserArgs) (AskUserResult, error) {
			if err := validateAskUserArgs(args); err != nil {
				return AskUserResult{Answers: map[string]string{}}, err
			}
			answers, err := c.deps.Asker.AskUser(ctx, args)
			if err != nil {
				return AskUserResult{}, err
			}
			return AskUserResult{Answers: answers}, nil
		},
	)
	return c.addTool("AskUser", askUser, err)
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
