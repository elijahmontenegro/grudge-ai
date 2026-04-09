import { useEffect, useState, useRef } from 'react'
import { useNavigate } from 'react-router'
import { gql } from '@apollo/client'
import { useQuery, useMutation } from '@apollo/client/react'
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

const CREATE_THREAD = gql`
  mutation PaletteCreateThread($name: String) {
    createThread(name: $name) { id name }
  }
`

export function CommandPalette() {
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [debouncedSearch, setDebouncedSearch] = useState('')
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  const navigate = useNavigate()
  const { data: threadsData } = useQuery<{threads: {id: string; name: string}[]}>(THREADS_QUERY)
  const { data: searchData } = useQuery<{search: {messageId: string; threadId: string; threadName: string; snippet: string; score: number}[]}>(SEARCH_QUERY, {
    variables: { query: debouncedSearch, limit: 5 },
    skip: debouncedSearch.length < 2,
  })

  const handleSearch = (value: string) => {
    setSearch(value)
    clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => setDebouncedSearch(value), 300)
  }
  const [createThread] = useMutation<{createThread: {id: string; name: string}}>(CREATE_THREAD)

  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.key === 'k' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        setOpen((o) => !o)
      }
      // Ctrl+N — new thread
      if (e.key === 'n' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        handleNewThread()
      }
    }
    document.addEventListener('keydown', down)
    return () => document.removeEventListener('keydown', down)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const handleNewThread = async () => {
    const result = await createThread({ variables: { name: null } })
    if (result.data?.createThread) {
      navigate(`/thread/${result.data.createThread.id}`)
      setOpen(false)
    }
  }

  const threads = threadsData?.threads ?? []

  return (
    <CommandDialog open={open} onOpenChange={setOpen}>
      <CommandInput
        placeholder="Search threads and messages..."
        value={search}
        onValueChange={handleSearch}
      />
      <CommandList>
        <CommandEmpty>No results found.</CommandEmpty>

        {/* Actions */}
        <CommandGroup heading="Actions">
          <CommandItem onSelect={handleNewThread}>
            <span className="text-muted-foreground mr-2">+</span>
            New thread
            <span className="ml-auto text-[10px] text-muted-foreground/40 font-mono">Ctrl+N</span>
          </CommandItem>
          <CommandItem onSelect={() => { navigate('/settings'); setOpen(false) }}>
            <span className="text-muted-foreground mr-2">&#9881;</span>
            Settings
          </CommandItem>
        </CommandGroup>

        {/* Threads */}
        {threads.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Threads">
              {threads.slice(0, 10).map((thread) => (
                <CommandItem
                  key={thread.id}
                  onSelect={() => {
                    navigate(`/thread/${thread.id}`)
                    setOpen(false)
                  }}
                >
                  {thread.name && thread.name !== 'New Thread' ? thread.name : 'New conversation'}
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        {/* Search results */}
        {searchData?.search && searchData.search.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Search Results">
              {searchData.search.map((result) => (
                <CommandItem
                  key={result.messageId}
                  onSelect={() => {
                    navigate(`/thread/${result.threadId}`)
                    setOpen(false)
                  }}
                >
                  <div className="flex flex-col gap-0.5">
                    <span className="text-sm">{result.threadName}</span>
                    <span className="text-xs text-muted-foreground line-clamp-1">{result.snippet}</span>
                  </div>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}
      </CommandList>
    </CommandDialog>
  )
}
