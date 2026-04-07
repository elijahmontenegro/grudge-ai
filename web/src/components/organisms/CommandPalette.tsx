import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { gql } from '@apollo/client'
import { useQuery } from '@apollo/client/react'
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command'

const THREADS_QUERY = gql`
  query CommandPaletteThreads {
    threads {
      id
      name
    }
  }
`

const SEARCH_QUERY = gql`
  query Search($query: String!, $limit: Int) {
    search(query: $query, limit: $limit) {
      messageId
      threadId
      threadName
      snippet
      score
    }
  }
`

export function CommandPalette() {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const navigate = useNavigate()
  const { data: threadsData } = useQuery<any>(THREADS_QUERY)
  const { data: searchData } = useQuery<any>(SEARCH_QUERY, {
    variables: { query: search, limit: 5 },
    skip: search.length < 2,
  })

  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.key === 'k' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        setOpen((open) => !open)
      }
    }
    document.addEventListener('keydown', down)
    return () => document.removeEventListener('keydown', down)
  }, [])

  return (
    <CommandDialog open={open} onOpenChange={setOpen}>
      <CommandInput
        placeholder="Search threads, messages, skills..."
        value={search}
        onValueChange={setSearch}
      />
      <CommandList>
        <CommandEmpty>No results found.</CommandEmpty>

        <CommandGroup heading="Threads">
          {threadsData?.threads?.map((thread: { id: string; name: string }) => (
            <CommandItem
              key={thread.id}
              onSelect={() => {
                navigate(`/thread/${thread.id}`)
                setOpen(false)
              }}
            >
              {thread.name || 'Untitled'}
            </CommandItem>
          ))}
        </CommandGroup>

        {searchData?.search && searchData.search.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Search Results">
              {searchData.search.map((result: { messageId: string; threadId: string; threadName: string; snippet: string; score: number }) => (
                <CommandItem
                  key={result.messageId}
                  onSelect={() => {
                    navigate(`/thread/${result.threadId}`)
                    setOpen(false)
                  }}
                >
                  <div className="flex flex-col gap-1">
                    <span className="text-sm font-medium">{result.threadName}</span>
                    <span className="text-xs text-muted line-clamp-2">{result.snippet}</span>
                  </div>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        <CommandSeparator />
        <CommandGroup heading="Actions">
          <CommandItem onSelect={() => { navigate('/settings'); setOpen(false) }}>
            Settings
          </CommandItem>
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  )
}
