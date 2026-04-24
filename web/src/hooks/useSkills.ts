import { useQuery } from '@apollo/client/react'
import { GET_SKILLS } from '@/graphql/operations'
import type { GetSkillsQuery } from '@/graphql/generated/types'

export interface Skill {
  name: string
  description: string
}

export interface SkillsResult {
  skills: Skill[]
  loading: boolean
  error: string | null
}

export function useSkills(): SkillsResult {
  const { data, loading, error } = useQuery<GetSkillsQuery>(GET_SKILLS)
  return {
    skills: data?.skills ?? [],
    loading,
    error: error?.message ?? null,
  }
}
