import { Routes, Route, Navigate } from 'react-router'
import { gql } from '@apollo/client'
import { useQuery } from '@apollo/client/react'
import { HomePage } from '@/pages/Home'
import { ThreadPage } from '@/pages/Thread'
import { SettingsPage } from '@/pages/Settings'
import { CommandPalette } from '@/components/organisms/CommandPalette'

const SETTINGS_CHECK = gql`
  query SettingsCheck {
    settings {
      providers
    }
  }
`

function useNeedsSetup(): boolean | null {
  const { data, loading } = useQuery<{ settings: { providers: string } }>(SETTINGS_CHECK)
  if (loading || !data) return null // still loading — don't redirect yet
  if (!data.settings?.providers) return true
  try {
    const providers = JSON.parse(data.settings.providers)
    return !providers.main?.adapter || !providers.main?.model
  } catch {
    return true
  }
}

export default function App() {
  const needsSetup = useNeedsSetup()

  return (
    <>
      <CommandPalette />
      <Routes>
        <Route path="/" element={
          needsSetup === null ? null :
          needsSetup === true ? <Navigate to="/settings" replace /> :
          <HomePage />
        } />
        <Route path="/thread/:threadId" element={<ThreadPage />} />
        <Route path="/settings" element={<SettingsPage />} />
      </Routes>
    </>
  )
}
