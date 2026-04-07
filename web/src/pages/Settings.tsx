import { useQuery, useMutation, gql } from '@apollo/client'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Separator } from '@/components/ui/separator'
import { ScrollArea } from '@/components/ui/scroll-area'

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

const UPDATE_SETTINGS = gql`
  mutation UpdateSettings($input: SettingsInput!) {
    updateSettings(input: $input) {
      providers
      permissions
      preferences
    }
  }
`

export function SettingsPage() {
  const { data, loading, refetch } = useQuery(SETTINGS_QUERY)
  const [updateSettings] = useMutation(UPDATE_SETTINGS)

  if (loading) return <div className="p-8 text-muted">Loading settings...</div>

  const settings = data?.settings
  const providers = settings?.providers ? JSON.parse(settings.providers) : {}
  const permissions = settings?.permissions ? JSON.parse(settings.permissions) : {}

  return (
    <ScrollArea className="h-screen">
      <div className="max-w-2xl mx-auto p-8 space-y-8">
        <h1 className="text-2xl font-bold tracking-tight">Settings</h1>

        <section className="space-y-4">
          <h2 className="text-lg font-semibold">Providers</h2>
          <Separator />
          {Object.entries(providers).map(([role, config]: [string, any]) => (
            <div key={role} className="space-y-2">
              <h3 className="text-sm font-medium capitalize">{role}</h3>
              <div className="grid grid-cols-3 gap-2">
                <Input value={config.adapter} placeholder="Adapter" readOnly />
                <Input value={config.model} placeholder="Model" readOnly />
                <Input value={config.base_url} placeholder="Base URL" readOnly />
              </div>
            </div>
          ))}
        </section>

        <section className="space-y-4">
          <h2 className="text-lg font-semibold">Permissions</h2>
          <Separator />
          <div className="grid grid-cols-2 gap-2">
            {Object.entries(permissions).map(([tool, level]: [string, any]) => (
              <div key={tool} className="flex items-center justify-between p-2 rounded border border-border">
                <span className="text-sm font-mono">{tool}</span>
                <span className="text-sm text-muted">{level as string}</span>
              </div>
            ))}
          </div>
        </section>

        <Button variant="outline" onClick={() => refetch()}>Refresh</Button>
      </div>
    </ScrollArea>
  )
}
