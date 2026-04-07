import { useQuery, gql } from '@apollo/client'

const SETTINGS_QUERY = gql`
  query Settings {
    settings {
      providers
      permissions
      mcpServers
      preferences
    }
  }
`

export function SettingsPage() {
  const { data, loading } = useQuery(SETTINGS_QUERY)

  if (loading) return <div className="p-8 text-muted">Loading settings...</div>

  return (
    <div className="max-w-2xl mx-auto p-8">
      <h1 className="text-2xl font-bold mb-6">Settings</h1>
      <pre className="bg-secondary p-4 rounded-lg overflow-auto text-sm">
        {JSON.stringify(data?.settings, null, 2)}
      </pre>
    </div>
  )
}
