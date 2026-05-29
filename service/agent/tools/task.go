package tools

import (
	"fmt"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Task tools — TaskCreate / TaskGet / TaskUpdate / TaskList /
// TaskStop / TaskOutput. All operate over deps.Tasks; the store
// itself is a thin wrapper around an atomic-ID-keyed map of
// Task records.

func registerTaskTools(c *buildCtx) error {
	taskCreate, err := functiontool.New(
		functiontool.Config{Name: "TaskCreate", Description: descriptionFor("TaskCreate")},
		func(ctx tool.Context, args TaskCreateArgs) (TaskCreateResult, error) {
			if c.deps.Tasks == nil {
				return TaskCreateResult{}, fmt.Errorf("task store not initialized")
			}
			t := c.deps.Tasks.Create(args.Subject, args.Description, "")
			return TaskCreateResult{TaskID: t.ID}, nil
		},
	)
	if err := c.addTool("TaskCreate", taskCreate, err); err != nil {
		return err
	}

	taskGet, err := functiontool.New(
		functiontool.Config{Name: "TaskGet", Description: descriptionFor("TaskGet")},
		func(ctx tool.Context, args TaskGetArgs) (TaskGetResult, error) {
			if c.deps.Tasks == nil {
				return TaskGetResult{}, fmt.Errorf("task store not initialized")
			}
			t := c.deps.Tasks.Get(args.TaskID)
			if t == nil {
				return TaskGetResult{}, fmt.Errorf("task %s not found", args.TaskID)
			}
			return TaskGetResult{Subject: t.Subject, Status: t.Status}, nil
		},
	)
	if err := c.addTool("TaskGet", taskGet, err); err != nil {
		return err
	}

	taskUpdate, err := functiontool.New(
		functiontool.Config{Name: "TaskUpdate", Description: descriptionFor("TaskUpdate")},
		func(ctx tool.Context, args TaskUpdateArgs) (TaskUpdateResult, error) {
			if c.deps.Tasks == nil {
				return TaskUpdateResult{}, fmt.Errorf("task store not initialized")
			}
			if args.Status == "deleted" {
				c.deps.Tasks.Delete(args.TaskID)
				return TaskUpdateResult{Success: true}, nil
			}
			ok := c.deps.Tasks.Update(args.TaskID, args.Status, "", "", "")
			if !ok {
				return TaskUpdateResult{}, fmt.Errorf("task %s not found", args.TaskID)
			}
			return TaskUpdateResult{Success: true}, nil
		},
	)
	if err := c.addTool("TaskUpdate", taskUpdate, err); err != nil {
		return err
	}

	taskList, err := functiontool.New(
		functiontool.Config{Name: "TaskList", Description: descriptionFor("TaskList")},
		func(ctx tool.Context, args TaskListArgs) (TaskListResult, error) {
			if c.deps.Tasks == nil {
				return TaskListResult{Tasks: []string{}}, nil
			}
			tasks := c.deps.Tasks.List()
			lines := make([]string, len(tasks))
			for i, t := range tasks {
				lines[i] = fmt.Sprintf("#%s [%s] %s", t.ID, t.Status, t.Subject)
			}
			return TaskListResult{Tasks: lines}, nil
		},
	)
	if err := c.addTool("TaskList", taskList, err); err != nil {
		return err
	}

	taskStop, err := functiontool.New(
		functiontool.Config{Name: "TaskStop", Description: descriptionFor("TaskStop")},
		func(ctx tool.Context, args TaskStopArgs) (TaskStopResult, error) {
			if c.deps.Tasks == nil {
				return TaskStopResult{}, fmt.Errorf("task store not initialized")
			}
			ok := c.deps.Tasks.Update(args.TaskID, "completed", "", "", "")
			return TaskStopResult{Success: ok}, nil
		},
	)
	if err := c.addTool("TaskStop", taskStop, err); err != nil {
		return err
	}

	taskOutput, err := functiontool.New(
		functiontool.Config{Name: "TaskOutput", Description: descriptionFor("TaskOutput")},
		func(ctx tool.Context, args TaskOutputArgs) (TaskOutputResult, error) {
			if c.deps.Tasks == nil {
				return TaskOutputResult{}, fmt.Errorf("task store not initialized")
			}
			t := c.deps.Tasks.Get(args.TaskID)
			if t == nil {
				return TaskOutputResult{}, fmt.Errorf("task %s not found", args.TaskID)
			}
			return TaskOutputResult{Output: fmt.Sprintf("[%s] %s: %s", t.Status, t.Subject, t.Description)}, nil
		},
	)
	return c.addTool("TaskOutput", taskOutput, err)
}

type TodoWriteArgs struct {
	Tasks []string `json:"tasks"`
}
type TodoWriteResult struct {
	Success bool `json:"success"`
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
