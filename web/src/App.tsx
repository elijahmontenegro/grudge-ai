import { lazy, Suspense, useCallback, useEffect, useState } from 'react'
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router'
import { ThreadSidebar } from '@/components/organisms/ThreadSidebar'
import { ArtifactsPanel } from '@/components/organisms/ArtifactsPanel'
import { CommandPalette } from '@/components/organisms/CommandPalette'
import type {
  AutonomousDuration,
  ComposerMode,
  ComposerScope,
} from '@/components/organisms/Composer'
import { Topbar } from '@/components/molecules/Topbar'
import { Home } from '@/pages/Home'
import { ThreadPage } from '@/pages/Thread'
import { useThreads } from '@/hooks/useThreads'
import { useThreadMessages } from '@/hooks/useThreadMessages'
import { useCreateThread } from '@/hooks/useCreateThread'
import { useNeedsSetup } from '@/hooks/useNeedsSetup'
import { useAgentState } from '@/hooks/useAgentState'
import { useLocalStorage } from '@/hooks/useLocalStorage'
import type { Message } from '@/data/types'

// FirstRun and Settings are config-flow pages — lazy-loaded so they don't
// weigh on the thread-view initial paint.
const FirstRun = lazy(() => import('@/pages/FirstRun').then((m) => ({ default: m.FirstRun })))
const Settings = lazy(() => import('@/pages/Settings').then((m) => ({ default: m.Settings })))
const Chats = lazy(() => import('@/pages/Chats').then((m) => ({ default: m.Chats })))

type Theme = 'light' | 'dark'

interface PersistedUI {
  sidebarCollapsed: boolean
  artifactsCollapsed: boolean
  theme: Theme
  composerMode: ComposerMode
  composerScope: ComposerScope
  composerDuration: AutonomousDuration
}

const DEFAULT_UI: PersistedUI = {
  sidebarCollapsed: false,
  artifactsCollapsed: true,
  theme: 'light',
  composerMode: 'normal',
  composerScope: 'thread',
  composerDuration: '1h',
}

type View = 'home' | 'thread' | 'settings' | 'firstrun' | 'chats'

function useRouteView(): { view: View; threadId?: string } {
  const { pathname } = useLocation()
  if (pathname.startsWith('/thread/')) {
    const id = pathname.slice('/thread/'.length)
    return { view: 'thread', threadId: id }
  }
  if (pathname.startsWith('/settings')) return { view: 'settings' }
  if (pathname.startsWith('/firstrun')) return { view: 'firstrun' }
  if (pathname.startsWith('/threads')) return { view: 'chats' }
  return { view: 'home' }
}

export default function App() {
  const navigate = useNavigate()
  const { view, threadId } = useRouteView()
  const { threads } = useThreads()
  const { create: createThread } = useCreateThread()
  const { needsSetup } = useNeedsSetup()

  // Single object keeps the persisted UI surface in one localStorage key
  // — six per-key effects firing on every UI tick would be wasteful for
  // state that only changes on explicit user action.
  const [ui, setUi] = useLocalStorage<PersistedUI>('spidey.ui', DEFAULT_UI)
  const setSidebarCollapsed = useCallback(
    (next: boolean | ((prev: boolean) => boolean)) =>
      setUi((p) => ({
        ...p,
        sidebarCollapsed:
          typeof next === 'function' ? next(p.sidebarCollapsed) : next,
      })),
    [setUi],
  )
  const setArtifactsCollapsed = useCallback(
    (next: boolean | ((prev: boolean) => boolean)) =>
      setUi((p) => ({
        ...p,
        artifactsCollapsed:
          typeof next === 'function' ? next(p.artifactsCollapsed) : next,
      })),
    [setUi],
  )
  const setTheme = useCallback(
    (next: Theme | ((prev: Theme) => Theme)) =>
      setUi((p) => ({
        ...p,
        theme: typeof next === 'function' ? next(p.theme) : next,
      })),
    [setUi],
  )
  const setComposerMode = useCallback(
    (next: ComposerMode | ((prev: ComposerMode) => ComposerMode)) =>
      setUi((p) => ({
        ...p,
        composerMode: typeof next === 'function' ? next(p.composerMode) : next,
      })),
    [setUi],
  )
  const setComposerScope = useCallback(
    (next: ComposerScope | ((prev: ComposerScope) => ComposerScope)) =>
      setUi((p) => ({
        ...p,
        composerScope:
          typeof next === 'function' ? next(p.composerScope) : next,
      })),
    [setUi],
  )
  const setComposerDuration = useCallback(
    (next: AutonomousDuration | ((prev: AutonomousDuration) => AutonomousDuration)) =>
      setUi((p) => ({
        ...p,
        composerDuration:
          typeof next === 'function' ? next(p.composerDuration) : next,
      })),
    [setUi],
  )
  const sidebarCollapsed = ui.sidebarCollapsed
  const artifactsCollapsed = ui.artifactsCollapsed
  const theme = ui.theme
  const composerMode = ui.composerMode
  const composerScope = ui.composerScope
  const composerDuration = ui.composerDuration

  const [paletteOpen, setPaletteOpen] = useState(false)

  // Active-thread messages hoisted to app level so the ArtifactsPanel
  // (sibling of the topbar in the main grid) can share the same
  // Apollo-cached corpus with ThreadPage. Apollo dedupes the query, so
  // this double-hook is free.
  const { messages: activeMessages } = useThreadMessages(threadId || '')
  const activeAgent = useAgentState(threadId || '')

  const openThread = useCallback(
    (id: string) => {
      navigate(`/thread/${id}`)
    },
    [navigate],
  )

  // "New" and the spidey brand ("Home") resolve to the same place —
  // Home IS the new-thread composer. "New" additionally stamps a
  // `freshAt` into location state so Home can reset its draft and
  // replay its entrance animation even when already on "/" (a plain
  // repeat navigation wouldn't trigger a re-render). No DB write
  // happens until first send, so clicking New twenty times costs
  // nothing.
  const newThread = useCallback(() => {
    navigate('/', { state: { freshAt: Date.now() } })
  }, [navigate])

  useEffect(() => {
    const root = document.documentElement
    root.classList.remove('light', 'dark')
    root.classList.add(theme)
  }, [theme])

  // Browser tab title reflects where the user is — helps with multi-window /
  // back-button navigation.
  useEffect(() => {
    let title = 'Spidey'
    const active = threadId ? threads.find((t) => t.id === threadId) : undefined
    if (view === 'thread' && active) title = `${active.name} · Spidey`
    else if (view === 'settings') title = 'Settings · Spidey'
    else if (view === 'firstrun') title = 'First run · Spidey'
    document.title = title
  }, [view, threadId, threads])

  // First-open redirect: push empty-config users into /firstrun.
  useEffect(() => {
    if (needsSetup !== true) return
    if (view === 'firstrun' || view === 'settings') return
    navigate('/firstrun', { replace: true })
  }, [needsSetup, view, navigate])

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const mod = e.metaKey || e.ctrlKey
      if (!mod) return
      const tgt = e.target as HTMLElement | null
      const inField =
        tgt?.tagName === 'TEXTAREA' ||
        tgt?.tagName === 'INPUT' ||
        tgt?.isContentEditable === true
      const k = e.key.toLowerCase()
      if (k === 'k') {
        e.preventDefault()
        setPaletteOpen(true)
      } else if (k === 'p' && !inField) {
        if (view === 'thread') {
          e.preventDefault()
          setComposerMode((m) => (m === 'plan' ? 'normal' : 'plan'))
        }
      } else if (e.shiftKey && k === 'a' && !inField) {
        if (view === 'thread') {
          e.preventDefault()
          setComposerScope('all')
        }
      }
      // ⌘⇧↵ is exclusively the Composer's — no window-level handling.
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [view])

  const activeThread = threadId ? threads.find((t) => t.id === threadId) : undefined
  const corpus: Message[] = activeMessages
  const chromeless = view === 'firstrun'

  return (
    <div
      className="app"
      data-chromeless={chromeless}
      data-sidebar-collapsed={sidebarCollapsed ? 'true' : undefined}
    >
      {!chromeless && (
        <ThreadSidebar
          threads={threads}
          activeId={threadId || ''}
          view={view}
          onSelect={openThread}
          onGoHome={() => navigate('/')}
          onOpenChats={() => navigate('/threads')}
          onNew={newThread}
          onOpenPalette={() => setPaletteOpen(true)}
          onAfterDelete={(deletedId) => {
            if (threadId === deletedId) navigate('/')
          }}
          collapsed={sidebarCollapsed}
          onToggleCollapsed={() => setSidebarCollapsed((v) => !v)}
          theme={theme}
          onToggleTheme={() => setTheme((t) => (t === 'dark' ? 'light' : 'dark'))}
          onOpenFirstRun={() => navigate('/firstrun')}
          onOpenSettings={() => navigate('/settings')}
        />
      )}

      <main className="main">
        {!chromeless && (
          <Topbar
            view={view}
            thread={activeThread}
            artifactsCollapsed={view === 'thread' ? artifactsCollapsed : undefined}
            onToggleArtifacts={
              view === 'thread' ? () => setArtifactsCollapsed((v) => !v) : undefined
            }
          />
        )}

        <div className="main-canvas">
        <Routes>
          <Route
            path="/"
            element={
              <Home
                onQuickStart={async (draft, workingDirs, sandboxed) => {
                  const id = await createThread(undefined, workingDirs, sandboxed)
                  if (id) navigate(`/thread/${id}`, { state: { initialMessage: draft } })
                }}
                onStartAutonomous={async (draft, duration, workingDirs, sandboxed) => {
                  const id = await createThread(undefined, workingDirs, sandboxed)
                  if (id) {
                    navigate(`/thread/${id}`, {
                      state: { startAutonomous: { text: draft, duration } },
                    })
                  }
                }}
                mode={composerMode}
                setMode={setComposerMode}
                scope={composerScope}
                setScope={setComposerScope}
                duration={composerDuration}
                setDuration={setComposerDuration}
              />
            }
          />
          {/* /thread/new used to be a draft-only route that rendered
              its own composer. Now "New" routes to "/" (Home), so
              /thread/new is just a synonym and redirects. Keeps any
              old bookmark / external link working. */}
          <Route path="/thread/new" element={<Navigate to="/" replace />} />
          <Route
            path="/thread/:id"
            element={
              <ThreadPage
                mode={composerMode}
                setMode={setComposerMode}
                scope={composerScope}
                setScope={setComposerScope}
                duration={composerDuration}
                setDuration={setComposerDuration}
                artifactsCollapsed={artifactsCollapsed}
                onToggleArtifacts={() => setArtifactsCollapsed((v) => !v)}
              />
            }
          />
          <Route
            path="/threads"
            element={
              <Suspense fallback={<LazyFallback label="threads" />}>
                <Chats onOpenThread={openThread} />
              </Suspense>
            }
          />
          <Route
            path="/settings"
            element={
              <Suspense fallback={<LazyFallback label="settings" />}>
                <Settings />
              </Suspense>
            }
          />
          <Route
            path="/firstrun"
            element={
              <Suspense fallback={<LazyFallback label="first run" />}>
                <FirstRun onComplete={() => navigate('/')} />
              </Suspense>
            }
          />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
        </div>

        {view === 'thread' && (
          <ArtifactsPanel
            corpus={corpus}
            livePlanContent={activeAgent.planContent}
            collapsed={artifactsCollapsed}
            onClose={() => setArtifactsCollapsed(true)}
          />
        )}
      </main>

      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        onRun={(item) => {
          switch (item.label) {
            case 'New thread':
              void newThread()
              return
            case 'Configure providers':
              navigate('/settings')
              return
            case 'Switch scope to all-threads':
              setComposerScope('all')
              if (view !== 'thread' && threadId) navigate(`/thread/${threadId}`)
              return
            case 'Enter plan mode':
              setComposerMode('plan')
              if (view !== 'thread' && threadId) navigate(`/thread/${threadId}`)
              return
            case 'Start autonomous run':
              setComposerMode('autonomous')
              if (view !== 'thread' && threadId) navigate(`/thread/${threadId}`)
              return
          }
        }}
        threads={threads}
        onOpenThread={(id, messageId) => {
          navigate(`/thread/${id}`, messageId ? { state: { focusMessageId: messageId } } : undefined)
        }}
        onNewThreadWithPrompt={async (prompt) => {
          const id = await createThread()
          if (!id) return
          navigate(`/thread/${id}`, { state: { initialMessage: prompt } })
        }}
      />
    </div>
  )
}

function LazyFallback({ label }: { label: string }) {
  return (
    <div className="empty-thread">
      <p className="empty-thread-hint">loading {label}…</p>
    </div>
  )
}
