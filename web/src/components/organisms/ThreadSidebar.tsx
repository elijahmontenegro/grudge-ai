import { Fragment, useMemo, useRef, useState } from 'react'
import { IconChats, IconChevron, IconPlus, IconSearch, IconX, IconPanel } from '@/components/atoms/icons'
import { kbdLabel } from '@/components/atoms/platform'
import { ThreadRow } from '@/components/molecules/ThreadRow'
import { SidebarSection } from '@/components/molecules/SidebarSection'
import { UserMenu } from '@/components/molecules/UserMenu'
import { useThreadMutations } from '@/hooks/useThreadMutations'
import { useMe } from '@/hooks/useMe'
import type { Thread } from '@/data/types'

/** Number of most-recently-active non-pinned threads shown in the sidebar.
 *  Everything beyond lives on the Home "all chats" view. Keeps the sidebar
 *  calm rather than an ever-growing list. */
const RECENT_CAP = 10

interface GroupedSection {
  key: string
  label: string
  threads: Thread[]
  childrenByParent: Record<string, Thread[]>
  defaultCollapsed?: boolean
}

/**
 * Two-section groupThreads:
 *   - Starred (all pinned, non-archived)
 *   - Recent (non-pinned, non-archived, sorted by lastActive, capped)
 * Search collapses both into a single "results" section.
 * Archived is removed from the sidebar entirely — accessible via the
 * "View all chats" button at the bottom, which goes to Home.
 */
function groupThreads(threads: Thread[], query: string): GroupedSection[] {
  const q = query.trim().toLowerCase()
  const matches = threads.filter(
    (t) => !q || t.name.toLowerCase().includes(q) || t.id.toLowerCase().includes(q),
  )

  const roots = matches.filter((t) => !t.parentId)
  const childrenByParent: Record<string, Thread[]> = {}
  matches
    .filter((t) => t.parentId)
    .forEach((c) => {
      const key = c.parentId as string
      ;(childrenByParent[key] ||= []).push(c)
    })

  if (q) {
    return [{ key: 'results', label: 'results', threads: roots, childrenByParent }]
  }

  const active = roots.filter((t) => !t.archived)
  const starred = active.filter((t) => t.pinned)
  const recent = active.filter((t) => !t.pinned).slice(0, RECENT_CAP)

  const sections: GroupedSection[] = []
  if (starred.length) sections.push({ key: 'starred', label: 'starred', threads: starred, childrenByParent })
  sections.push({ key: 'recent', label: 'recent', threads: recent, childrenByParent })
  return sections
}

interface SidebarProps {
  threads: Thread[]
  activeId: string
  view: string
  onSelect: (id: string) => void
  onNew: () => void
  /** Brand click — takes the user to Home (the composer-first landing). */
  onGoHome: () => void
  /** "View all chats" / rail Chats icon — goes to the /chats browser. */
  onOpenChats: () => void
  onOpenPalette: () => void
  onAfterDelete?: (id: string) => void
  collapsed: boolean
  onToggleCollapsed: () => void
  theme: 'light' | 'dark'
  onToggleTheme: () => void
  onOpenFirstRun: () => void
  onOpenSettings: () => void
}

export function ThreadSidebar({
  threads,
  activeId,
  view,
  onSelect,
  onNew,
  onGoHome,
  onOpenChats,
  onOpenPalette,
  onAfterDelete,
  collapsed,
  onToggleCollapsed,
  theme,
  onToggleTheme,
  onOpenFirstRun,
  onOpenSettings,
}: SidebarProps) {
  const [query, setQuery] = useState('')
  const [expandedBranches, setExpandedBranches] = useState<Record<string, boolean>>({})
  const [menuOpen, setMenuOpen] = useState(false)
  const userAnchorRef = useRef<HTMLDivElement>(null)
  const railAnchorRef = useRef<HTMLButtonElement>(null)
  const threadMuts = useThreadMutations()
  const me = useMe()

  const sections = useMemo(() => groupThreads(threads, query), [threads, query])
  const visibleThreadCount = threads.filter((t) => !t.archived).length

  function toggleArchive(t: Thread) {
    if (t.archived) void threadMuts.unarchive(t.id)
    else void threadMuts.archive(t.id)
  }
  async function rename(id: string, name: string) {
    await threadMuts.rename(id, name)
  }
  async function remove(id: string) {
    await threadMuts.remove(id)
    onAfterDelete?.(id)
  }

  if (collapsed) {
    // Collapsed rail: single primary nav — the Chats button — analogous
    // to the "View all chats" button in the expanded list. No per-thread
    // avatars; specific threads are reached via the expanded sidebar,
    // the palette (⌘K), or Home. Keeps the rail minimal and forces all
    // thread navigation through consistent entry points.
    return (
      <aside className="sidebar sidebar-rail">
        {/* Panel toggle is layout chrome (expand sidebar) — separated
            from the action/nav cluster below by breathing room so the
            rail reads as two zones: "toggle the sidebar" on its own,
            "everything you can do" underneath. `.rail-nav-gap` on the
            next button supplies the extra margin-top. */}
        <button className="rail-btn" onClick={onToggleCollapsed} title="Expand sidebar">
          <IconPanel size={18} />
        </button>
        <button className="rail-btn rail-nav-gap" onClick={onNew} title="New thread">
          <IconPlus size={18} />
        </button>
        <button
          className="rail-btn"
          onClick={onOpenPalette}
          title={`Jump to anything (${kbdLabel('K')})`}
        >
          <IconSearch size={18} />
        </button>
        <button
          className="rail-btn rail-chats rail-nav-gap"
          onClick={onOpenChats}
          data-active={view === 'chats' || undefined}
          title={`Threads · ${visibleThreadCount}`}
        >
          <IconChats size={18} />
          {visibleThreadCount > 0 && (
            <span className="rail-chats-count">{visibleThreadCount}</span>
          )}
        </button>
        <div style={{ flex: 1 }} />
        <button
          ref={railAnchorRef}
          className="rail-btn"
          onClick={() => setMenuOpen((o) => !o)}
          title={me.name}
        >
          <span className="rail-avatar">{me.initials}</span>
        </button>
        <UserMenu
          open={menuOpen}
          anchorRef={railAnchorRef}
          placement="right"
          onClose={() => setMenuOpen(false)}
          theme={theme}
          onToggleTheme={onToggleTheme}
          onOpenFirstRun={onOpenFirstRun}
          onOpenSettings={onOpenSettings}
        />
      </aside>
    )
  }

  return (
    <aside className="sidebar">
      <div className="sb-head">
        {/* Collapse toggle sits at the far-left of the header so it
            lines up with the same button at the top of the collapsed
            rail — same 32×32 frame at the same viewport x, so the
            control reads as "one button that doesn't move" across
            the two sidebar states. Brand is pushed to the opposite
            side by the spacer. */}
        <button className="sb-icon-btn" onClick={onToggleCollapsed} title="Collapse sidebar">
          <IconPanel size={18} />
        </button>
        <span className="sb-head-spacer" />
        <button
          className="sb-brand"
          onClick={onGoHome}
          title="Home"
          type="button"
        >
          <span className="sb-brand-text">spidey</span>
        </button>
      </div>

      <div className="sb-search-row">
        <div className="sb-search">
          <IconSearch size={13} />
          <input
            type="text"
            placeholder="Filter threads"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {query && (
            <button className="sb-search-clear" onClick={() => setQuery('')} title="Clear">
              <IconX size={10} />
            </button>
          )}
        </div>
        <button className="sb-new-btn" onClick={onNew} title={`New thread (${kbdLabel('N')})`}>
          <IconPlus size={13} />
          <span>New</span>
        </button>
      </div>

      {/* "Jump to…" lives in the sidebar, under New, so the palette is
          reachable from every route (not just when there's a composer
          on screen). Uses the search glyph because the palette is
          fundamentally a search across threads/messages/skills. */}
      <button
        className="sb-jump-btn"
        onClick={onOpenPalette}
        title={`Jump to anything (${kbdLabel('K')})`}
      >
        <IconSearch size={13} />
        <span>Jump to…</span>
        <span className="sb-jump-kbd">{kbdLabel('K')}</span>
      </button>

      <div className="sb-list scroll">
        {sections.map((section) => (
          <SidebarSection
            key={section.key}
            label={section.label}
            count={section.threads.length}
            defaultCollapsed={section.defaultCollapsed}
          >
            {section.threads.map((t) => {
              const kids = section.childrenByParent[t.id] || []
              const expanded = !!expandedBranches[t.id]
              return (
                <Fragment key={t.id}>
                  <ThreadRow
                    thread={t}
                    active={view === 'thread' && activeId === t.id}
                    onClick={() => onSelect(t.id)}
                    hasBranches={kids.length > 0}
                    showBranches={expanded}
                    onToggleBranches={() => setExpandedBranches((e) => ({ ...e, [t.id]: !e[t.id] }))}
                    onArchiveToggle={() => toggleArchive(t)}
                    onDelete={() => void remove(t.id)}
                    onRename={(name) => void rename(t.id, name)}
                  />
                  {expanded &&
                    kids.map((c) => (
                      <ThreadRow
                        key={c.id}
                        thread={c}
                        child
                        active={view === 'thread' && activeId === c.id}
                        onClick={() => onSelect(c.id)}
                        onArchiveToggle={() => toggleArchive(c)}
                        onDelete={() => void remove(c.id)}
                        onRename={(name) => void rename(c.id, name)}
                      />
                    ))}
                </Fragment>
              )
            })}
            {section.threads.length === 0 && section.key === 'recent' && !query && (
              <div className="sb-empty">no threads yet — hit New</div>
            )}
            {section.threads.length === 0 && query && <div className="sb-empty">no matches</div>}
          </SidebarSection>
        ))}
        <button
          type="button"
          className="sb-view-all"
          onClick={onOpenChats}
          data-active={view === 'chats' || undefined}
          title="See every thread, including archived"
        >
          <span>View all threads</span>
          {visibleThreadCount > RECENT_CAP && (
            <span className="sb-view-all-count">{visibleThreadCount}</span>
          )}
        </button>
      </div>

      <div className="sb-footer">
        <div ref={userAnchorRef} className="sb-user" onClick={() => setMenuOpen((o) => !o)}>
          <span className="sb-avatar">{me.initials}</span>
          <div className="sb-user-text">
            <div className="sb-user-name">{me.name}</div>
            <div className="sb-user-handle">@{me.handle}</div>
          </div>
          <IconChevron size={10} />
        </div>
        <UserMenu
          open={menuOpen}
          anchorRef={userAnchorRef}
          placement="top"
          onClose={() => setMenuOpen(false)}
          theme={theme}
          onToggleTheme={onToggleTheme}
          onOpenFirstRun={onOpenFirstRun}
          onOpenSettings={onOpenSettings}
        />
      </div>
    </aside>
  )
}
