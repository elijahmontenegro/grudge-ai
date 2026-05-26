package tools

import (
	"fmt"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// Subagent tools — Agent spawns a forked runner; SendMessage routes
// a message into a running subagent thread. forkID is the new
// thread's id; the runner factory mounts a forked engine snapshot
// and runs to completion.

func registerAgentTools(c *buildCtx) error {
	agentTool, err := functiontool.New(
		functiontool.Config{Name: "Agent", Description: "Spawn a subagent to handle a complex task autonomously. The subagent runs in an ephemeral thread fork with its own RRC state."},
		func(ctx tool.Context, args AgentToolArgs) (AgentToolResult, error) {
			if err := c.requireApproval(ctx, "Agent", marshalArgs(args)); err != nil {
				return AgentToolResult{}, err
			}
			forkID := fmt.Sprintf("fork-%s-%d", c.deps.ThreadID, c.deps.Tasks.seq.Load())
			result, err := c.deps.Agent.SpawnAgent(ctx, args.Task, forkID)
			if err != nil {
				return AgentToolResult{}, err
			}
			return AgentToolResult{Result: result}, nil
		},
	)
	if err := c.addTool("Agent", agentTool, err); err != nil {
		return err
	}

	sendMsg, err := functiontool.New(
		functiontool.Config{Name: "SendMessage", Description: "Send a message to a running subagent. The subagent receives it as a new user message in its forked thread."},
		func(ctx tool.Context, args SendMessageArgs) (SendMessageResult, error) {
			if err := c.requireApproval(ctx, "SendMessage", marshalArgs(args)); err != nil {
				return SendMessageResult{}, err
			}
			resp, err := c.deps.Agent.SendToAgent(ctx, args.To, args.Message)
			if err != nil {
				return SendMessageResult{}, err
			}
			return SendMessageResult{Response: resp}, nil
		},
	)
	return c.addTool("SendMessage", sendMsg, err)
}
