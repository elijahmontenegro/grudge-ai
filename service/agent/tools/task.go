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
		functiontool.Config{Name: "TaskCreate", Description: "Create a task to track work progress. Returns the task ID."},
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
		functiontool.Config{Name: "TaskGet", Description: "Get details of a specific task by ID."},
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
		functiontool.Config{Name: "TaskUpdate", Description: "Update a task's status (pending, in_progress, completed) or delete it (status=deleted)."},
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
		functiontool.Config{Name: "TaskList", Description: "List all tasks with their status."},
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
		functiontool.Config{Name: "TaskStop", Description: "Stop a running task by marking it completed."},
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
		functiontool.Config{Name: "TaskOutput", Description: "Get the description and status of a task."},
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
