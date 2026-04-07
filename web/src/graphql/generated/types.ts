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
  DateTime: { input: any; output: any; }
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

export type Edge = {
  __typename?: 'Edge';
  crossEncoderScore: Scalars['Float']['output'];
  fromMessageId: Scalars['ID']['output'];
  qudWeight: Scalars['Float']['output'];
  score: Scalars['Float']['output'];
  source: Scalars['String']['output'];
  temporalProximity: Scalars['Float']['output'];
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
  content: Scalars['String']['output'];
  createdAt: Scalars['DateTime']['output'];
  id: Scalars['ID']['output'];
  position: Scalars['Int']['output'];
  role: Scalars['String']['output'];
  threadId: Scalars['ID']['output'];
};

export type Mutation = {
  __typename?: 'Mutation';
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
  resumeAgent: Scalars['Boolean']['output'];
  saveViewState: ViewState;
  sendMessage: Message;
  startAutonomous: Scalars['Boolean']['output'];
  stopAgent: Scalars['Boolean']['output'];
  unarchiveThread: Scalars['Boolean']['output'];
  updateSettings: Settings;
  updateThread: Thread;
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


export type MutationResumeAgentArgs = {
  correction?: InputMaybe<Scalars['String']['input']>;
  threadId: Scalars['ID']['input'];
};


export type MutationSaveViewStateArgs = {
  state: ViewStateInput;
  threadId: Scalars['ID']['input'];
};


export type MutationSendMessageArgs = {
  content: Scalars['String']['input'];
  scope?: InputMaybe<SelectionScope>;
  threadId: Scalars['ID']['input'];
};


export type MutationStartAutonomousArgs = {
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


export type MutationUpdateSettingsArgs = {
  input: SettingsInput;
};


export type MutationUpdateThreadArgs = {
  id: Scalars['ID']['input'];
  name?: InputMaybe<Scalars['String']['input']>;
  sandboxed?: InputMaybe<Scalars['Boolean']['input']>;
  workingDirs?: InputMaybe<Array<Scalars['String']['input']>>;
};

export type Qud = {
  __typename?: 'QUD';
  addressedBy: Array<Scalars['ID']['output']>;
  establishedBy: Scalars['ID']['output'];
  id: Scalars['ID']['output'];
  parentQudId?: Maybe<Scalars['ID']['output']>;
  question: Scalars['String']['output'];
  status: Scalars['String']['output'];
};

export type QudGraph = {
  __typename?: 'QUDGraph';
  activeStack: Array<Scalars['ID']['output']>;
  quds: Array<Qud>;
};

export type Query = {
  __typename?: 'Query';
  messages: Array<Message>;
  qudGraph?: Maybe<QudGraph>;
  search: Array<SearchResult>;
  selectionResult?: Maybe<SelectionResult>;
  settings: Settings;
  thread?: Maybe<Thread>;
  threads: Array<Thread>;
  viewState?: Maybe<ViewState>;
};


export type QueryMessagesArgs = {
  limit?: InputMaybe<Scalars['Int']['input']>;
  offset?: InputMaybe<Scalars['Int']['input']>;
  threadId: Scalars['ID']['input'];
};


export type QueryQudGraphArgs = {
  threadId: Scalars['ID']['input'];
};


export type QuerySearchArgs = {
  limit?: InputMaybe<Scalars['Int']['input']>;
  query: Scalars['String']['input'];
};


export type QuerySelectionResultArgs = {
  eventId: Scalars['ID']['input'];
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
  hooks: Scalars['String']['output'];
  mcpServers: Scalars['String']['output'];
  permissions: Scalars['String']['output'];
  preferences: Scalars['String']['output'];
  providers: Scalars['String']['output'];
};

export type SettingsInput = {
  mcpServers?: InputMaybe<Scalars['String']['input']>;
  permissions?: InputMaybe<Scalars['String']['input']>;
  preferences?: InputMaybe<Scalars['String']['input']>;
  providers?: InputMaybe<Scalars['String']['input']>;
};

export type StreamEvent = {
  __typename?: 'StreamEvent';
  delta?: Maybe<Scalars['String']['output']>;
  done: Scalars['Boolean']['output'];
  error?: Maybe<Scalars['String']['output']>;
  messageId: Scalars['ID']['output'];
  thinking?: Maybe<Scalars['String']['output']>;
  toolCall?: Maybe<ToolCallDelta>;
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
  name: Scalars['String']['output'];
  parentThreadId?: Maybe<Scalars['ID']['output']>;
  sandboxed: Scalars['Boolean']['output'];
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

export type ToolCallDelta = {
  __typename?: 'ToolCallDelta';
  arguments?: Maybe<Scalars['String']['output']>;
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

export type ThreadBranchesQueryVariables = Exact<{
  includeArchived?: InputMaybe<Scalars['Boolean']['input']>;
}>;


export type ThreadBranchesQuery = { __typename?: 'Query', threads: Array<{ __typename?: 'Thread', id: string, name: string, parentThreadId?: string | null, branchPointPosition?: number | null }> };

export type StartAutonomousMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  prompt: Scalars['String']['input'];
  duration: Scalars['String']['input'];
}>;


export type StartAutonomousMutation = { __typename?: 'Mutation', startAutonomous: boolean };

export type PauseAgentMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type PauseAgentMutation = { __typename?: 'Mutation', pauseAgent: boolean };

export type ResumeAgentMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  correction?: InputMaybe<Scalars['String']['input']>;
}>;


export type ResumeAgentMutation = { __typename?: 'Mutation', resumeAgent: boolean };

export type StopAgentMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type StopAgentMutation = { __typename?: 'Mutation', stopAgent: boolean };

export type CommandPaletteThreadsQueryVariables = Exact<{ [key: string]: never; }>;


export type CommandPaletteThreadsQuery = { __typename?: 'Query', threads: Array<{ __typename?: 'Thread', id: string, name: string }> };

export type SearchQueryVariables = Exact<{
  query: Scalars['String']['input'];
  limit?: InputMaybe<Scalars['Int']['input']>;
}>;


export type SearchQuery = { __typename?: 'Query', search: Array<{ __typename?: 'SearchResult', messageId: string, threadId: string, threadName: string, snippet: string, score: number }> };

export type SelectionResultQueryVariables = Exact<{
  eventId: Scalars['ID']['input'];
}>;


export type SelectionResultQuery = { __typename?: 'Query', selectionResult?: { __typename?: 'SelectionResult', eventId: string, scope: SelectionScope, threadId: string, selected: Array<{ __typename?: 'SelectedMessage', messageId: string, effectiveScore: number, hopDepth: number, threadId: string, crossThread: boolean }>, excluded: Array<{ __typename?: 'ExcludedMessage', messageId: string, reason: string, score: number }> } | null };

export type IntrospectionMessagesQueryVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type IntrospectionMessagesQuery = { __typename?: 'Query', messages: Array<{ __typename?: 'Message', id: string, role: string, content: string, position: number }> };

export type QudGraphQueryVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type QudGraphQuery = { __typename?: 'Query', qudGraph?: { __typename?: 'QUDGraph', activeStack: Array<string>, quds: Array<{ __typename?: 'QUD', id: string, question: string, establishedBy: string, parentQudId?: string | null, status: string, addressedBy: Array<string> }> } | null };

export type EnterPlanModeMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type EnterPlanModeMutation = { __typename?: 'Mutation', enterPlanMode: boolean };

export type ApprovePlanMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  executionMode: ExecutionMode;
}>;


export type ApprovePlanMutation = { __typename?: 'Mutation', approvePlan: boolean };

export type SubagentProgressSubscriptionVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type SubagentProgressSubscription = { __typename?: 'Subscription', subagentProgress: { __typename?: 'SubagentProgress', threadId: string, forkThreadId: string, task: string, status: string, roundCount: number } };

export type SidebarThreadsQueryVariables = Exact<{
  includeArchived?: InputMaybe<Scalars['Boolean']['input']>;
}>;


export type SidebarThreadsQuery = { __typename?: 'Query', threads: Array<{ __typename?: 'Thread', id: string, name: string, createdAt: any, archivedAt?: any | null, parentThreadId?: string | null }> };

export type CreateThreadMutationVariables = Exact<{
  name?: InputMaybe<Scalars['String']['input']>;
}>;


export type CreateThreadMutation = { __typename?: 'Mutation', createThread: { __typename?: 'Thread', id: string, name: string } };

export type ApproveToolCallMutationVariables = Exact<{
  callId: Scalars['ID']['input'];
}>;


export type ApproveToolCallMutation = { __typename?: 'Mutation', approveToolCall: boolean };

export type DenyToolCallMutationVariables = Exact<{
  callId: Scalars['ID']['input'];
  reason?: InputMaybe<Scalars['String']['input']>;
}>;


export type DenyToolCallMutation = { __typename?: 'Mutation', denyToolCall: boolean };

export type AgentStateSubscriptionVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type AgentStateSubscription = { __typename?: 'Subscription', agentState: { __typename?: 'AgentState', threadId: string, status: AgentStatus, mode: AgentMode, roundCount: number, startedAt?: any | null, durationLimit?: string | null, elapsedTime?: string | null } };

export type MessageStreamSubscriptionVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type MessageStreamSubscription = { __typename?: 'Subscription', messageStream: { __typename?: 'StreamEvent', messageId: string, delta?: string | null, thinking?: string | null, done: boolean, error?: string | null, toolCall?: { __typename?: 'ToolCallDelta', id: string, name: string, arguments?: string | null } | null } };

export type ThreadStateChangesSubscriptionVariables = Exact<{ [key: string]: never; }>;


export type ThreadStateChangesSubscription = { __typename?: 'Subscription', threadStateChanges: { __typename?: 'ThreadStateEvent', threadId: string, status: AgentStatus, mode: AgentMode, warmth: number, name: string } };

export type ToolExecutionSubscriptionVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type ToolExecutionSubscription = { __typename?: 'Subscription', toolExecution: { __typename?: 'ToolExecution', threadId: string, callId: string, toolName: string, arguments: string, status: string, result?: string | null, isError?: boolean | null } };

export type ViewStateQueryVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type ViewStateQuery = { __typename?: 'Query', viewState?: { __typename?: 'ViewState', threadId: string, scrollPosition: number, expandedMessageIds: Array<string>, inputDraft: string, citationExpansionState: string } | null };

export type SaveViewStateMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  state: ViewStateInput;
}>;


export type SaveViewStateMutation = { __typename?: 'Mutation', saveViewState: { __typename?: 'ViewState', threadId: string } };

export type SettingsQueryVariables = Exact<{ [key: string]: never; }>;


export type SettingsQuery = { __typename?: 'Query', settings: { __typename?: 'Settings', providers: string, permissions: string, preferences: string } };

export type UpdateSettingsMutationVariables = Exact<{
  input: SettingsInput;
}>;


export type UpdateSettingsMutation = { __typename?: 'Mutation', updateSettings: { __typename?: 'Settings', providers: string, permissions: string, preferences: string } };

export type ThreadMessagesQueryVariables = Exact<{
  threadId: Scalars['ID']['input'];
}>;


export type ThreadMessagesQuery = { __typename?: 'Query', messages: Array<{ __typename?: 'Message', id: string, role: string, content: string, position: number, createdAt: any }>, thread?: { __typename?: 'Thread', id: string, name: string } | null };

export type SendMessageMutationVariables = Exact<{
  threadId: Scalars['ID']['input'];
  content: Scalars['String']['input'];
  scope?: InputMaybe<SelectionScope>;
}>;


export type SendMessageMutation = { __typename?: 'Mutation', sendMessage: { __typename?: 'Message', id: string, role: string, content: string, position: number } };
