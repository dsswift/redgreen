import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'
import type { EstateResponse, RepoDetailResponse, ServerEvent, SettingsResponse } from '../shared/api.ts'
import type { Account } from '../shared/model.ts'
import type { RepoSettings, RuleSeverities } from '../shared/rules.ts'

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, { ...init, headers: { 'content-type': 'application/json', ...init?.headers } })
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as { error?: string } | null
    throw new Error(body?.error ?? `${response.status} ${response.statusText}`)
  }
  return (await response.json()) as T
}

export const keys = {
  estate: ['estate'] as const,
  repo: (id: string) => ['repo', id] as const,
  settings: ['settings'] as const,
}

export function useEstate() {
  return useQuery({ queryKey: keys.estate, queryFn: () => call<EstateResponse>('/api/estate'), staleTime: 10_000 })
}

export function useRepo(id: string) {
  return useQuery({ queryKey: keys.repo(id), queryFn: () => call<RepoDetailResponse>(`/api/repos/${encodeURIComponent(id)}`) })
}

export function useSettings() {
  return useQuery({ queryKey: keys.settings, queryFn: () => call<SettingsResponse>('/api/settings') })
}

export function useUpdateRepoSettings(id: string) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (patch: Partial<RepoSettings>) =>
      call<RepoDetailResponse>(`/api/repos/${encodeURIComponent(id)}/settings`, { method: 'PUT', body: JSON.stringify(patch) }),
    onSuccess: (detail) => {
      client.setQueryData(keys.repo(id), detail)
      void client.invalidateQueries({ queryKey: keys.estate })
    },
  })
}

export function useSyncRepo(id: string) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: () => call<RepoDetailResponse>(`/api/repos/${encodeURIComponent(id)}/sync`, { method: 'POST' }),
    onSuccess: (detail) => {
      client.setQueryData(keys.repo(id), detail)
      void client.invalidateQueries({ queryKey: keys.estate })
    },
  })
}

export function useSweep() {
  return useMutation({ mutationFn: () => call<unknown>('/api/sync', { method: 'POST' }) })
}

export function useUpdateRules() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (rules: RuleSeverities) => call<SettingsResponse>('/api/settings/rules', { method: 'PUT', body: JSON.stringify(rules) }),
    onSuccess: (settings) => {
      client.setQueryData(keys.settings, settings)
      void client.invalidateQueries({ queryKey: keys.estate })
    },
  })
}

export function useUpdateAccount() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: ({ id, status }: { id: string; status: Account['status'] }) =>
      call<SettingsResponse>(`/api/accounts/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ status }) }),
    onSuccess: (settings) => client.setQueryData(keys.settings, settings),
  })
}

export async function fetchManifest(name: string): Promise<{ action: string; manifest: Record<string, unknown> }> {
  return call(`/api/github/manifest?name=${encodeURIComponent(name)}`)
}

export function useSaveGitlabApp() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (app: { clientId: string; clientSecret: string }) => call<SettingsResponse>('/api/gitlab/app', { method: 'POST', body: JSON.stringify(app) }),
    onSuccess: (settings) => client.setQueryData(keys.settings, settings),
  })
}

/** Keeps queries fresh from the server's event stream. */
export function useLiveUpdates(client: QueryClient): void {
  useEffect(() => {
    const source = new EventSource('/api/events')
    const invalidateEstate = debounce(() => void client.invalidateQueries({ queryKey: keys.estate }), 500)
    const handle = (event: MessageEvent<string>) => {
      const parsed = JSON.parse(event.data) as ServerEvent
      if (parsed.type === 'repo') {
        void client.invalidateQueries({ queryKey: keys.repo(parsed.id) })
        invalidateEstate()
      } else if (parsed.type === 'estate') {
        invalidateEstate()
        void client.invalidateQueries({ queryKey: keys.settings })
      } else if (parsed.type === 'sync') {
        client.setQueryData<EstateResponse>(keys.estate, (old) => (old ? { ...old, sync: parsed.status } : old))
      }
    }
    source.addEventListener('repo', handle)
    source.addEventListener('estate', handle)
    source.addEventListener('sync', handle)
    return () => source.close()
  }, [client])
}

function debounce(fn: () => void, ms: number): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null
  return () => {
    if (timer) clearTimeout(timer)
    timer = setTimeout(fn, ms)
  }
}
