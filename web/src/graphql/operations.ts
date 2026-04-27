import { gql } from '@apollo/client'

// --- Queries ---

export const LIST_THREADS = gql`
  query ListThreads($includeArchived: Boolean) {
    threads(includeArchived: $includeArchived) {
      id
      name
      createdAt
      parentThreadId
      branchPointPosition
      archivedAt
      messageCount
      workingDirs
      sandboxed
      status
      mode
    }
  }
`

export const GET_THREAD_MESSAGES = gql`
  query GetThreadMessages($threadId: ID!, $limit: Int, $offset: Int) {
    thread(id: $threadId) {
      id
      name
      parentThreadId
      branchPointPosition
      archivedAt
      messageCount
      workingDirs
      sandboxed
    }
    messages(threadId: $threadId, limit: $limit, offset: $offset) {
      id
      role
      content
      thinking
      position
      createdAt
      toolCalls {
        id
        name
        arguments
      }
      toolResults {
        toolCallId
        content
      }
      attachments {
        id
        filename
        mimeType
        sizeBytes
        path
      }
    }
  }
`

export const GET_AGENT_STATE = gql`
  query GetAgentState($threadId: ID!) {
    agentState(threadId: $threadId) {
      threadId
      status
      mode
      elapsedTime
      planContent
      retry {
        attempt
        maxAttempts
        error
        nextDelayMs
        final
      }
    }
  }
`

export const GET_SKILLS = gql`
  query GetSkills {
    skills {
      name
      description
    }
  }
`

export const GET_RECENT_ACTIVITY = gql`
  query GetRecentActivity($limit: Int) {
    recentActivity(limit: $limit) {
      type
      threadId
      threadName
      summary
      timestamp
    }
  }
`

export const SEARCH = gql`
  query Search($query: String!, $limit: Int) {
    search(query: $query, limit: $limit) {
      messageId
      threadId
      threadName
      snippet
    }
  }
`

export const GET_SETTINGS = gql`
  query GetSettings {
    settings {
      providers
      preferences
      permissions
      mcpServers
      hooks
      engine
    }
  }
`

// --- Mutations ---

export const CREATE_THREAD = gql`
  mutation CreateThread($name: String, $workingDirs: [String!], $sandboxed: Boolean) {
    createThread(name: $name, workingDirs: $workingDirs, sandboxed: $sandboxed) {
      id
      name
    }
  }
`

export const SEND_MESSAGE = gql`
  mutation SendMessage(
    $threadId: ID!
    $content: String!
    $scope: SelectionScope
    $attachments: [AttachmentInput!]
  ) {
    sendMessage(
      threadId: $threadId
      content: $content
      scope: $scope
      attachments: $attachments
    ) {
      id
      position
      role
      content
    }
  }
`

export const START_AUTONOMOUS = gql`
  mutation StartAutonomous(
    $threadId: ID!
    $prompt: String!
    $duration: String!
    $attachments: [AttachmentInput!]
  ) {
    startAutonomous(
      threadId: $threadId
      prompt: $prompt
      duration: $duration
      attachments: $attachments
    )
  }
`

export const ENTER_PLAN_MODE = gql`
  mutation EnterPlanMode($threadId: ID!) {
    enterPlanMode(threadId: $threadId)
  }
`

export const APPROVE_PLAN = gql`
  mutation ApprovePlan($threadId: ID!, $executionMode: ExecutionMode!) {
    approvePlan(threadId: $threadId, executionMode: $executionMode)
  }
`

export const REJECT_PLAN = gql`
  mutation RejectPlan($threadId: ID!, $feedback: String) {
    rejectPlan(threadId: $threadId, feedback: $feedback)
  }
`

export const UPDATE_PLAN_SOURCE = gql`
  mutation UpdatePlanSource($threadId: ID!, $content: String!) {
    updatePlanSource(threadId: $threadId, content: $content)
  }
`

export const STOP_AGENT = gql`
  mutation StopAgent($threadId: ID!) {
    stopAgent(threadId: $threadId)
  }
`

export const PAUSE_AGENT = gql`
  mutation PauseAgent($threadId: ID!) {
    pauseAgent(threadId: $threadId)
  }
`

export const RESUME_AGENT = gql`
  mutation ResumeAgent($threadId: ID!, $correction: String) {
    resumeAgent(threadId: $threadId, correction: $correction)
  }
`

export const APPROVE_TOOL_CALL = gql`
  mutation ApproveToolCall($callId: ID!) {
    approveToolCall(callId: $callId)
  }
`

export const DENY_TOOL_CALL = gql`
  mutation DenyToolCall($callId: ID!, $reason: String) {
    denyToolCall(callId: $callId, reason: $reason)
  }
`

export const ANSWER_QUESTION = gql`
  mutation AnswerQuestion($callId: ID!, $answer: String!) {
    answerQuestion(callId: $callId, answer: $answer)
  }
`

export const ARCHIVE_THREAD = gql`
  mutation ArchiveThread($id: ID!) {
    archiveThread(id: $id)
  }
`

export const UNARCHIVE_THREAD = gql`
  mutation UnarchiveThread($id: ID!) {
    unarchiveThread(id: $id)
  }
`

export const DELETE_THREAD = gql`
  mutation DeleteThread($id: ID!) {
    deleteThread(id: $id)
  }
`

export const EDIT_MESSAGE = gql`
  mutation EditMessage($threadId: ID!, $messagePosition: Int!, $newContent: String!) {
    editMessage(threadId: $threadId, messagePosition: $messagePosition, newContent: $newContent) {
      id
      messageCount
    }
  }
`

export const UPDATE_THREAD = gql`
  mutation UpdateThread($id: ID!, $name: String, $workingDirs: [String!], $sandboxed: Boolean) {
    updateThread(id: $id, name: $name, workingDirs: $workingDirs, sandboxed: $sandboxed) {
      id
      name
      sandboxed
      workingDirs
    }
  }
`

export const UPDATE_SETTINGS = gql`
  mutation UpdateSettings($input: SettingsInput!) {
    updateSettings(input: $input) {
      providers
      preferences
      permissions
      mcpServers
      hooks
      engine
    }
  }
`

// --- Subscriptions ---

export const MESSAGE_STREAM = gql`
  subscription MessageStream($threadId: ID!) {
    messageStream(threadId: $threadId) {
      messageId
      delta
      thinking
      toolCall {
        id
        name
        arguments
      }
      done
      error
    }
  }
`

export const AGENT_STATE_SUB = gql`
  subscription AgentStateSub($threadId: ID!) {
    agentState(threadId: $threadId) {
      threadId
      status
      mode
      elapsedTime
      planContent
      retry {
        attempt
        maxAttempts
        error
        nextDelayMs
        final
      }
    }
  }
`

export const TOOL_EXECUTION_SUB = gql`
  subscription ToolExecutionSub($threadId: ID!) {
    toolExecution(threadId: $threadId) {
      threadId
      callId
      toolName
      arguments
      status
      result
      isError
    }
  }
`

export const SUBAGENT_PROGRESS_SUB = gql`
  subscription SubagentProgressSub($threadId: ID!) {
    subagentProgress(threadId: $threadId) {
      threadId
      forkThreadId
      task
      status
      roundCount
    }
  }
`

export const THREAD_STATE_CHANGES = gql`
  subscription ThreadStateChanges {
    threadStateChanges {
      threadId
      status
      mode
      warmth
      name
    }
  }
`
