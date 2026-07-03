export type Maybe<T> = T | null;
export type InputMaybe<T> = Maybe<T>;
export type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
export type MakeOptional<T, K extends keyof T> = Omit<T, K> & { [SubKey in K]?: Maybe<T[SubKey]> };
export type MakeMaybe<T, K extends keyof T> = Omit<T, K> & { [SubKey in K]: Maybe<T[SubKey]> };
export type MakeEmpty<T extends { [key: string]: unknown }, K extends keyof T> = { [_ in K]?: never };
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
/** All built-in and custom scalars, mapped to their actual values */
export type Scalars = {
  ID: { input: string; output: string; }
  String: { input: string; output: string; }
  Boolean: { input: boolean; output: boolean; }
  Int: { input: number; output: number; }
  Float: { input: number; output: number; }
  DateTime: { input: string; output: string; }
};

export type ActivityItem = {
  __typename?: 'ActivityItem';
  summary: Scalars['String']['output'];
  threadId: Scalars['ID']['output'];
  threadName: Scalars['String']['output'];
  timestamp: Scalars['DateTime']['output'];
  type: Scalars['String']['output'];
};

export enum AgentMode {
  Autonomous = 'AUTONOMOUS',
  Normal = 'NORMAL',
  Plan = 'PLAN'
}

export type AgentState = {
  __typename?: 'AgentState';
  durationLimit?: Maybe<Scalars['String']['output']>;
  elapsedTime?: Maybe<Scalars['String']['output']>;
  mode: AgentMode;
  planContent?: Maybe<Scalars['String']['output']>;
  /** Retry status of the underlying provider call. Null when not retrying. */
  retry?: Maybe<RetryStatus>;
  roundCount: Scalars['Int']['output'];
  startedAt?: Maybe<Scalars['DateTime']['output']>;
  status: AgentStatus;
  threadId: Scalars['ID']['output'];
};

export enum AgentStatus {
  Idle = 'IDLE',
  Paused = 'PAUSED',
  Running = 'RUNNING'
}

/**
 * Attachment metadata on a message. Files live in the thread's sandbox
 * workspace under `_attachments/{id}/{filename}`; `path` is the
 * sandbox-relative location the agent resolves via FileRead. Download
 * via GET /api/attachments/{threadId}/{id}/{filename}.
 */
export type AttachmentBlock = {
  __typename?: 'AttachmentBlock';
  filename: Scalars['String']['output'];
  id: Scalars['ID']['output'];
  mimeType: Scalars['String']['output'];
  path: Scalars['String']['output'];
  sizeBytes: Scalars['Int']['output'];
};

/**
 * AttachmentInput is the shape the client sends after a successful
 * POST /api/attachments/{threadId} — echoing what that endpoint
 * returned. The server reconstructs AttachmentContent blocks from
 * these and stamps them into the message at send time.
 */
export type AttachmentInput = {
  filename: Scalars['String']['input'];
  id: Scalars['ID']['input'];
  inlinedText?: InputMaybe<Scalars['String']['input']>;
  mimeType: Scalars['String']['input'];
  path: Scalars['String']['input'];
  sizeBytes: Scalars['Int']['input'];
};

export type Edge = {
  __typename?: 'Edge';
  crossEncoderScore: Scalars['Float']['output'];
  fromMessageId: Scalars['ID']['output'];
  score: Scalars['Float']['output'];
  source: Scalars['String']['output'];
  toMessageId: Scalars['ID']['output'];
};

export type ExcludedMessage = {
  __typename?: 'ExcludedMessage';
  messageId: Scalars['ID']['output'];
  reason: Scalars['String']['output'];
  score: Scalars['Float']['output'];
};

export enum ExecutionMode {
  Autonomous = 'AUTONOMOUS',
  Manual = 'MANUAL'
}

export type Message = {
  __typename?: 'Message';
  attachments: Array<AttachmentBlock>;
  citedByCount: Scalars['Int']['output'];
  content: Scalars['String']['output'];
  createdAt: Scalars['DateTime']['output'];
  id: Scalars['ID']['output'];
  position: Scalars['Int']['output'];
  role: Scalars['String']['output'];
  thinking?: Maybe<Scalars['String']['output']>;
  threadId: Scalars['ID']['output'];
  toolCalls: Array<ToolCallBlock>;
  toolResults: Array<ToolResultBlock>;
};

export type Mutation = {
  __typename?: 'Mutation';
  answerQuestion: Scalars['Boolean']['output'];
  approvePlan: Scalars['Boolean']['output'];
  approveToolCall: Scalars['Boolean']['output'];
  archiveThread: Scalars['Boolean']['output'];
  compileAdoc: Scalars['String']['output'];
  createThread: Thread;
  deleteThread: Scalars['Boolean']['output'];
  denyToolCall: Scalars['Boolean']['output'];
  editMessage: Thread;
  enterPlanMode: Scalars['Boolean']['output'];
  pauseAgent: Scalars['Boolean']['output'];
  /**
   * Reject the current plan and ask the agent to revise it. Clears the stored
   * plan content, re-enters plan mode, and queues a user-visible feedback
   * message so the next round starts with the critique. The caller typically
   * follows up with sendMessage(threadId, feedback) to actually kick the round.
   */
  rejectPlan: Scalars['Boolean']['output'];
  resumeAgent: Scalars['Boolean']['output'];
  saveViewState: ViewState;
  /**
   * Send a user message. `attachments` references files already uploaded
   * via POST /api/attachments/{threadId} — the client uploads first, gets
   * back a list of AttachmentMeta, then passes those IDs here so the
   * server stamps AttachmentContent blocks into the stored message.
   */
  sendMessage: Message;
  /**
   * Start an autonomous run. `attachments` attach to the initial kickoff
   * message only — subsequent rounds are empty user turns per the RRC
   * reflective-retrieval protocol.
   */
  startAutonomous: Scalars['Boolean']['output'];
  stopAgent: Scalars['Boolean']['output'];
  unarchiveThread: Scalars['Boolean']['output'];
  /**
   * Overwrite the plan artifact (plan.adoc) with user-edited content before
   * approval. Backend writes the file, replaces the cached plan content,
   * re-publishes agentState so the panel re-renders with the edit. The model
   * will see the edited version when it reads plan.adoc during implementation.
   */
  updatePlanSource: Scalars['Boolean']['output'];
  updateSettings: Settings;
  updateThread: Thread;
};


export type MutationAnswerQuestionArgs = {
  answer: Scalars['String']['input'];
  callId: Scalars['ID']['input'];
};


export type MutationApprovePlanArgs = {
  executionMode: ExecutionMode;
  threadId: Scalars['ID']['input'];
};


export type MutationApproveToolCallArgs = {
  callId: Scalars['ID']['input'];
};


export type MutationArchiveThreadArgs = {
  id: Scalars['ID']['input'];
};


export type MutationCompileAdocArgs = {
  path: Scalars['String']['input'];
};


export type MutationCreateThreadArgs = {
  name?: InputMaybe<Scalars['String']['input']>;
  sandboxed?: InputMaybe<Scalars['Boolean']['input']>;
  workingDirs?: InputMaybe<Array<Scalars['String']['input']>>;
};


export type MutationDeleteThreadArgs = {
  id: Scalars['ID']['input'];
};


export type MutationDenyToolCallArgs = {
  callId: Scalars['ID']['input'];
  reason?: InputMaybe<Scalars['String']['input']>;
};


export type MutationEditMessageArgs = {
  messagePosition: Scalars['Int']['input'];
  newContent: Scalars['String']['input'];
  threadId: Scalars['ID']['input'];
};


export type MutationEnterPlanModeArgs = {
  threadId: Scalars['ID']['input'];
};


export type MutationPauseAgentArgs = {
  threadId: Scalars['ID']['input'];
};


export type MutationRejectPlanArgs = {
  feedback?: InputMaybe<Scalars['String']['input']>;
  threadId: Scalars['ID']['input'];
};


export type MutationResumeAgentArgs = {
  correction?: InputMaybe<Scalars['String']['input']>;
  threadId: Scalars['ID']['input'];
};


export type MutationSaveViewStateArgs = {
  state: ViewStateInput;
  threadId: Scalars['ID']['input'];
};


export type MutationSendMessageArgs = {
  attachments?: InputMaybe<Array<AttachmentInput>>;
  content: Scalars['String']['input'];
  scope?: InputMaybe<SelectionScope>;
  threadId: Scalars['ID']['input'];
};


export type MutationStartAutonomousArgs = {
  attachments?: InputMaybe<Array<AttachmentInput>>;
  duration: Scalars['String']['input'];
  prompt: Scalars['String']['input'];
  threadId: Scalars['ID']['input'];
};


export type MutationStopAgentArgs = {
  threadId: Scalars['ID']['input'];
};


export type MutationUnarchiveThreadArgs = {
  id: Scalars['ID']['input'];
};


export type MutationUpdatePlanSourceArgs = {
  content: Scalars['String']['input'];
  threadId: Scalars['ID']['input'];
};


export type MutationUpdateSettingsArgs = {
  input: SettingsInput;
};


export type MutationUpdateThreadArgs = {
  id: Scalars['ID']['input'];
  name?: InputMaybe<Scalars['String']['input']>;
  sandboxed?: InputMaybe<Scalars['Boolean']['input']>;
  workingDirs?: InputMaybe<Array<Scalars['String']['input']>>;
};

export type Query = {
  __typename?: 'Query';
  agentState?: Maybe<AgentState>;
  messages: Array<Message>;
  recentActivity: Array<ActivityItem>;
  search: Array<SearchResult>;
  selectionForMessage?: Maybe<SelectionResult>;
  settings: Settings;
  skills: Array<SkillInfo>;
  thread?: Maybe<Thread>;
  threads: Array<Thread>;
  viewState?: Maybe<ViewState>;
};


export type QueryAgentStateArgs = {
  threadId: Scalars['ID']['input'];
};


export type QueryMessagesArgs = {
  limit?: InputMaybe<Scalars['Int']['input']>;
  offset?: InputMaybe<Scalars['Int']['input']>;
  threadId: Scalars['ID']['input'];
};


export type QueryRecentActivityArgs = {
  limit?: InputMaybe<Scalars['Int']['input']>;
};


export type QuerySearchArgs = {
  limit?: InputMaybe<Scalars['Int']['input']>;
  query: Scalars['String']['input'];
};


export type QuerySelectionForMessageArgs = {
  messageId: Scalars['ID']['input'];
};


export type QueryThreadArgs = {
  id: Scalars['ID']['input'];
};


export type QueryThreadsArgs = {
  includeArchived?: InputMaybe<Scalars['Boolean']['input']>;
};


export type QueryViewStateArgs = {
  threadId: Scalars['ID']['input'];
};

/**
 * Transient-failure retry progress for the current LLM request.
 * Surfaced to the UI so the user sees "retrying 2/10, next attempt in
 * 8s" instead of a silent pause.
 */
export type RetryStatus = {
  __typename?: 'RetryStatus';
  attempt: Scalars['Int']['output'];
  /** Last error message that triggered the retry (null on success). */
  error?: Maybe<Scalars['String']['output']>;
  /** True when no more attempts will be made (success or exhaustion). */
  final: Scalars['Boolean']['output'];
  maxAttempts: Scalars['Int']['output'];
  /** Milliseconds until the next attempt. 0 when this is the final event. */
  nextDelayMs: Scalars['Int']['output'];
};

export type SearchResult = {
  __typename?: 'SearchResult';
  messageId: Scalars['ID']['output'];
  score: Scalars['Float']['output'];
  snippet: Scalars['String']['output'];
  threadId: Scalars['ID']['output'];
  threadName: Scalars['String']['output'];
};

export type SelectedMessage = {
  __typename?: 'SelectedMessage';
  crossEncoderScore: Scalars['Float']['output'];
  crossThread: Scalars['Boolean']['output'];
  effectiveScore: Scalars['Float']['output'];
  hopDepth: Scalars['Int']['output'];
  messageId: Scalars['ID']['output'];
  threadId: Scalars['ID']['output'];
};

export type SelectionResult = {
  __typename?: 'SelectionResult';
  eventId: Scalars['ID']['output'];
  excluded: Array<ExcludedMessage>;
  scope: SelectionScope;
  selected: Array<SelectedMessage>;
  threadId: Scalars['ID']['output'];
};

export enum SelectionScope {
  AllThreads = 'ALL_THREADS',
  Thread = 'THREAD'
}

export type Settings = {
  __typename?: 'Settings';
  /**
   * Engine config as JSON, including thresholds, Local Context size, top-K, diversity, and budget controls.
   * Live-tunable — the RRC engine re-projects stored edges under the new config
   * at walk time, so saving here changes Selection behavior on the next turn
   * without a restart. Raw reranker scores are preserved; only the fused
   * projection shifts.
   */
  engine: Scalars['String']['output'];
  hooks: Scalars['String']['output'];
  mcpServers: Scalars['String']['output'];
  permissions: Scalars['String']['output'];
  preferences: Scalars['String']['output'];
  providers: Scalars['String']['output'];
};

export type SettingsInput = {
  engine?: InputMaybe<Scalars['String']['input']>;
  hooks?: InputMaybe<Scalars['String']['input']>;
  mcpServers?: InputMaybe<Scalars['String']['input']>;
  permissions?: InputMaybe<Scalars['String']['input']>;
  preferences?: InputMaybe<Scalars['String']['input']>;
  providers?: InputMaybe<Scalars['String']['input']>;
};

export type SkillInfo = {
  __typename?: 'SkillInfo';
  description: Scalars['String']['output'];
  name: Scalars['String']['output'];
};

export type StreamEvent = {
  __typename?: 'StreamEvent';
  delta?: Maybe<Scalars['String']['output']>;
  done: Scalars['Boolean']['output'];
  error?: Maybe<Scalars['String']['output']>;
  messageId: Scalars['ID']['output'];
  thinking?: Maybe<Scalars['String']['output']>;
};

export type SubagentProgress = {
  __typename?: 'SubagentProgress';
  forkThreadId: Scalars['ID']['output'];
  roundCount: Scalars['Int']['output'];
  status: Scalars['String']['output'];
  task: Scalars['String']['output'];
  threadId: Scalars['ID']['output'];
};

export type Subscription = {
  __typename?: 'Subscription';
  agentState: AgentState;
  messageStream: StreamEvent;
  subagentProgress: SubagentProgress;
  threadStateChanges: ThreadStateEvent;
  toolExecution: ToolExecution;
};


export type SubscriptionAgentStateArgs = {
  threadId: Scalars['ID']['input'];
};


export type SubscriptionMessageStreamArgs = {
  threadId: Scalars['ID']['input'];
};


export type SubscriptionSubagentProgressArgs = {
  threadId: Scalars['ID']['input'];
};


export type SubscriptionToolExecutionArgs = {
  threadId: Scalars['ID']['input'];
};

export type Thread = {
  __typename?: 'Thread';
  archivedAt?: Maybe<Scalars['DateTime']['output']>;
  branchPointPosition?: Maybe<Scalars['Int']['output']>;
  createdAt: Scalars['DateTime']['output'];
  id: Scalars['ID']['output'];
  messageCount: Scalars['Int']['output'];
  mode: AgentMode;
  name: Scalars['String']['output'];
  parentThreadId?: Maybe<Scalars['ID']['output']>;
  sandboxed: Scalars['Boolean']['output'];
  /**
   * Live agent state for this thread. Resolved from the agent_state
   * table on demand. Surfacing these on Thread (rather than only via
   * the separate agentState query) lets the LIST_THREADS Apollo cache
   * carry sidebar-status info without a parallel module-scope state map.
   */
  status: AgentStatus;
  workingDirs: Array<Scalars['String']['output']>;
};

export type ThreadStateEvent = {
  __typename?: 'ThreadStateEvent';
  mode: AgentMode;
  name: Scalars['String']['output'];
  status: AgentStatus;
  threadId: Scalars['ID']['output'];
  warmth: Scalars['Int']['output'];
};

export type ToolCallBlock = {
  __typename?: 'ToolCallBlock';
  arguments: Scalars['String']['output'];
  id: Scalars['ID']['output'];
  name: Scalars['String']['output'];
};

export type ToolExecution = {
  __typename?: 'ToolExecution';
  arguments: Scalars['String']['output'];
  callId: Scalars['ID']['output'];
  isError?: Maybe<Scalars['Boolean']['output']>;
  result?: Maybe<Scalars['String']['output']>;
  status: Scalars['String']['output'];
  threadId: Scalars['ID']['output'];
  toolName: Scalars['String']['output'];
};

export type ToolResultBlock = {
  __typename?: 'ToolResultBlock';
  content: Scalars['String']['output'];
  toolCallId: Scalars['ID']['output'];
};

export type ViewState = {
  __typename?: 'ViewState';
  citationExpansionState: Scalars['String']['output'];
  expandedMessageIds: Array<Scalars['ID']['output']>;
  inputDraft: Scalars['String']['output'];
  scrollPosition: Scalars['Float']['output'];
  threadId: Scalars['ID']['output'];
};

export type ViewStateInput = {
  citationExpansionState: Scalars['String']['input'];
  expandedMessageIds: Array<Scalars['ID']['input']>;
  inputDraft: Scalars['String']['input'];
  scrollPosition: Scalars['Float']['input'];
};

export type ListThreadsQueryVariables = Exact<{
  includeArchived?: InputMaybe<Scalars['Boolean']['input']>;
}>;


export type ListThreadsQuery = { __typename?: 'Query', threads: Array<{ __typename?: 'Thread', id: string, name: string, createdAt: string, parentThreadId?: string | null, branchPointPosition?: number | null, archivedAt?: string | null, messageCount: number, workingDirs: Array<string>, sandboxed: boolean, status: AgentStatus, mode: AgentMode }> };

export type GetThreadMessagesQueryVariables = Exact<{
  threadId: Scalars['ID']['input'];
  limit?: InputMaybe<Scalars['Int']['input']>;
  offset?: InputMaybe<Scalars['Int']['input']>;
}>;


export type GetThreadMessagesQuery = { __typename?: 'Query', thread?: { __typename?: 'Thread', id: string, name: string, parentThreadId?: string | null, branchPointPosition?: number | null, archivedAt?: string | null, messageCount: number, workingDirs: Array<string>, sandboxed: boolean } | null, messages: Array<{ __typename?: 'Message', id: string, role: string, content: string, thinking?: string | null, position: number, createdAt: string, toolCalls: Array<{ __typename?: 'ToolCallBlock', id: string, name: string, arguments: string }>, toolResults: Array<{ __typename?: 'ToolResultBlock', toolCallId: string, content: string }>, attachments: Array<{ __typename?: 'AttachmentBlock', id: string, filename: string, mimeType: string, sizeBytes: number, path: string }> }> };

export type GetAgentStateQueryVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type GetAgentStateQuery = { __typename?: 'Query', agentState?: { __typename?: 'AgentState', threadId: string, status: AgentStatus, mode: AgentMode, elapsedTime?: string | null, planContent?: string | null, retry?: { __typename?: 'RetryStatus', attempt: number, maxAttempts: number, error?: string | null, nextDelayMs: number, final: boolean } | null } | null };

export type GetSkillsQueryVariables = Exact<{ [key: string]: never; }>;


export type GetSkillsQuery = { __typename?: 'Query', skills: Array<{ __typename?: 'SkillInfo', name: string, description: string }> };

export type GetRecentActivityQueryVariables = Exact<{
  limit?: InputMaybe<Scalars['Int']['input']>;
}>;


export type GetRecentActivityQuery = { __typename?: 'Query', recentActivity: Array<{ __typename?: 'ActivityItem', type: string, threadId: string, threadName: string, summary: string, timestamp: string }> };

export type SearchQueryVariables = Exact<{
  query: Scalars['String']['input'];
  limit?: InputMaybe<Scalars['Int']['input']>;
}>;


export type SearchQuery = { __typename?: 'Query', search: Array<{ __typename?: 'SearchResult', messageId: string, threadId: string, threadName: string, snippet: string }> };

export type GetSettingsQueryVariables = Exact<{ [key: string]: never; }>;


export type GetSettingsQuery = { __typename?: 'Query', settings: { __typename?: 'Settings', providers: string, preferences: string, permissions: string, mcpServers: string, hooks: string, engine: string } };

export type CreateThreadMutationVariables = Exact<{
  name?: InputMaybe<Scalars['String']['input']>;
  workingDirs?: InputMaybe<Array<Scalars['String']['input']> | Scalars['String']['input']>;
  sandboxed?: InputMaybe<Scalars['Boolean']['input']>;
}>;


export type CreateThreadMutation = { __typename?: 'Mutation', createThread: { __typename?: 'Thread', id: string, name: string } };

export type SendMessageMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  content: Scalars['String']['input'];
  scope?: InputMaybe<SelectionScope>;
  attachments?: InputMaybe<Array<AttachmentInput> | AttachmentInput>;
}>;


export type SendMessageMutation = { __typename?: 'Mutation', sendMessage: { __typename?: 'Message', id: string, position: number, role: string, content: string } };

export type StartAutonomousMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  prompt: Scalars['String']['input'];
  duration: Scalars['String']['input'];
  attachments?: InputMaybe<Array<AttachmentInput> | AttachmentInput>;
}>;


export type StartAutonomousMutation = { __typename?: 'Mutation', startAutonomous: boolean };

export type EnterPlanModeMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type EnterPlanModeMutation = { __typename?: 'Mutation', enterPlanMode: boolean };

export type ApprovePlanMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  executionMode: ExecutionMode;
}>;


export type ApprovePlanMutation = { __typename?: 'Mutation', approvePlan: boolean };

export type RejectPlanMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  feedback?: InputMaybe<Scalars['String']['input']>;
}>;


export type RejectPlanMutation = { __typename?: 'Mutation', rejectPlan: boolean };

export type UpdatePlanSourceMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  content: Scalars['String']['input'];
}>;


export type UpdatePlanSourceMutation = { __typename?: 'Mutation', updatePlanSource: boolean };

export type StopAgentMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type StopAgentMutation = { __typename?: 'Mutation', stopAgent: boolean };

export type PauseAgentMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type PauseAgentMutation = { __typename?: 'Mutation', pauseAgent: boolean };

export type ResumeAgentMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  correction?: InputMaybe<Scalars['String']['input']>;
}>;


export type ResumeAgentMutation = { __typename?: 'Mutation', resumeAgent: boolean };

export type ApproveToolCallMutationVariables = Exact<{
  callId: Scalars['ID']['input'];
}>;


export type ApproveToolCallMutation = { __typename?: 'Mutation', approveToolCall: boolean };

export type DenyToolCallMutationVariables = Exact<{
  callId: Scalars['ID']['input'];
  reason?: InputMaybe<Scalars['String']['input']>;
}>;


export type DenyToolCallMutation = { __typename?: 'Mutation', denyToolCall: boolean };

export type AnswerQuestionMutationVariables = Exact<{
  callId: Scalars['ID']['input'];
  answer: Scalars['String']['input'];
}>;


export type AnswerQuestionMutation = { __typename?: 'Mutation', answerQuestion: boolean };

export type ArchiveThreadMutationVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type ArchiveThreadMutation = { __typename?: 'Mutation', archiveThread: boolean };

export type UnarchiveThreadMutationVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type UnarchiveThreadMutation = { __typename?: 'Mutation', unarchiveThread: boolean };

export type DeleteThreadMutationVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type DeleteThreadMutation = { __typename?: 'Mutation', deleteThread: boolean };

export type EditMessageMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  messagePosition: Scalars['Int']['input'];
  newContent: Scalars['String']['input'];
}>;


export type EditMessageMutation = { __typename?: 'Mutation', editMessage: { __typename?: 'Thread', id: string, messageCount: number } };

export type UpdateThreadMutationVariables = Exact<{
  id: Scalars['ID']['input'];
  name?: InputMaybe<Scalars['String']['input']>;
  workingDirs?: InputMaybe<Array<Scalars['String']['input']> | Scalars['String']['input']>;
  sandboxed?: InputMaybe<Scalars['Boolean']['input']>;
}>;


export type UpdateThreadMutation = { __typename?: 'Mutation', updateThread: { __typename?: 'Thread', id: string, name: string, sandboxed: boolean, workingDirs: Array<string> } };

export type UpdateSettingsMutationVariables = Exact<{
  input: SettingsInput;
}>;


export type UpdateSettingsMutation = { __typename?: 'Mutation', updateSettings: { __typename?: 'Settings', providers: string, preferences: string, permissions: string, mcpServers: string, hooks: string, engine: string } };

export type MessageStreamSubscriptionVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type MessageStreamSubscription = { __typename?: 'Subscription', messageStream: { __typename?: 'StreamEvent', messageId: string, delta?: string | null, thinking?: string | null, done: boolean, error?: string | null } };

export type AgentStateSubSubscriptionVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type AgentStateSubSubscription = { __typename?: 'Subscription', agentState: { __typename?: 'AgentState', threadId: string, status: AgentStatus, mode: AgentMode, elapsedTime?: string | null, planContent?: string | null, retry?: { __typename?: 'RetryStatus', attempt: number, maxAttempts: number, error?: string | null, nextDelayMs: number, final: boolean } | null } };

export type ToolExecutionSubSubscriptionVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type ToolExecutionSubSubscription = { __typename?: 'Subscription', toolExecution: { __typename?: 'ToolExecution', threadId: string, callId: string, toolName: string, arguments: string, status: string, result?: string | null, isError?: boolean | null } };

export type SubagentProgressSubSubscriptionVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type SubagentProgressSubSubscription = { __typename?: 'Subscription', subagentProgress: { __typename?: 'SubagentProgress', threadId: string, forkThreadId: string, task: string, status: string, roundCount: number } };

export type ThreadStateChangesSubscriptionVariables = Exact<{ [key: string]: never; }>;


export type ThreadStateChangesSubscription = { __typename?: 'Subscription', threadStateChanges: { __typename?: 'ThreadStateEvent', threadId: string, status: AgentStatus, mode: AgentMode, warmth: number, name: string } };
