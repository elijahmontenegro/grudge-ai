import { useCallback } from 'react'
import { useMutation } from '@apollo/client/react'
import {
  APPROVE_PLAN,
  ENTER_PLAN_MODE,
  REJECT_PLAN,
  SEND_MESSAGE,
  UPDATE_PLAN_SOURCE,
} from '@/graphql/operations'
import type {
  ApprovePlanMutation,
  ApprovePlanMutationVariables,
  EnterPlanModeMutation,
  EnterPlanModeMutationVariables,
  RejectPlanMutation,
  RejectPlanMutationVariables,
  SendMessageMutation,
  SendMessageMutationVariables,
  UpdatePlanSourceMutation,
  UpdatePlanSourceMutationVariables,
} from '@/graphql/generated/types'
import { ExecutionMode, SelectionScope } from '@/graphql/generated/types'

export interface PlanMode {
  enterPlan: (threadId: string) => Promise<void>
  approvePlan: (threadId: string, autonomous: boolean) => Promise<void>
  /** Reject and request a revision. Clears backend plan state, re-enters
   *  plan mode, and (if feedback is non-empty) sends it as a user message so
   *  the next round starts with the critique. */
  rejectPlan: (threadId: string, feedback: string) => Promise<void>
  /** Overwrite plan.adoc with user-edited content before approval. Backend
   *  updates the file + cached plan and re-publishes agentState so the panel
   *  re-renders. Model reads the edited version from disk on implementation. */
  editPlan: (threadId: string, content: string) => Promise<void>
  entering: boolean
  approving: boolean
  rejecting: boolean
  editing: boolean
  error: string | null
}

export function usePlanMode(): PlanMode {
  const [enter, enterRes] = useMutation<EnterPlanModeMutation, EnterPlanModeMutationVariables>(
    ENTER_PLAN_MODE,
    { refetchQueries: ['GetAgentState'] },
  )
  const [approve, approveRes] = useMutation<ApprovePlanMutation, ApprovePlanMutationVariables>(
    APPROVE_PLAN,
    { refetchQueries: ['GetAgentState'] },
  )
  const [reject, rejectRes] = useMutation<RejectPlanMutation, RejectPlanMutationVariables>(
    REJECT_PLAN,
    { refetchQueries: ['GetAgentState'] },
  )
  const [send] = useMutation<SendMessageMutation, SendMessageMutationVariables>(SEND_MESSAGE)
  const [editSrc, editRes] = useMutation<
    UpdatePlanSourceMutation,
    UpdatePlanSourceMutationVariables
  >(UPDATE_PLAN_SOURCE, { refetchQueries: ['GetAgentState'] })

  const enterPlan = useCallback(
    async (threadId: string) => {
      await enter({ variables: { threadId } })
    },
    [enter],
  )

  const approvePlan = useCallback(
    async (threadId: string, autonomous: boolean) => {
      await approve({
        variables: {
          threadId,
          executionMode: autonomous ? ExecutionMode.Autonomous : ExecutionMode.Manual,
        },
      })
    },
    [approve],
  )

  const rejectPlan = useCallback(
    async (threadId: string, feedback: string) => {
      const trimmed = feedback.trim()
      // Clear backend plan + restart runner in plan mode so the next round
      // sees a clean slate with the plan-mode system prompt re-applied.
      await reject({ variables: { threadId, feedback: trimmed || null } })
      // Kick the round with the critique as a user message — without this,
      // the agent would sit idle after rejectPlan returns.
      if (trimmed) {
        const text = `I rejected the plan. Please revise plan.adoc based on this feedback, then call ExitPlanMode:\n\n${trimmed}`
        await send({
          variables: { threadId, content: text, scope: SelectionScope.Thread },
        })
      }
    },
    [reject, send],
  )

  const editPlan = useCallback(
    async (threadId: string, content: string) => {
      await editSrc({ variables: { threadId, content } })
    },
    [editSrc],
  )

  return {
    enterPlan,
    approvePlan,
    rejectPlan,
    editPlan,
    entering: enterRes.loading,
    approving: approveRes.loading,
    rejecting: rejectRes.loading,
    editing: editRes.loading,
    error:
      enterRes.error?.message ??
      approveRes.error?.message ??
      rejectRes.error?.message ??
      editRes.error?.message ??
      null,
  }
}
