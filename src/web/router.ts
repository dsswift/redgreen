import { useEffect, useState } from 'react'

export type Route = { page: 'board' } | { page: 'quiet' } | { page: 'settings' } | { page: 'repo'; id: string }

export function parseRoute(pathname: string): Route {
  const parts = pathname.split('/').filter(Boolean)
  if (parts[0] === 'quiet') return { page: 'quiet' }
  if (parts[0] === 'settings') return { page: 'settings' }
  if (parts[0] === 'repos' && parts[1]) return { page: 'repo', id: decodeURIComponent(parts[1]) }
  return { page: 'board' }
}

export function href(route: Route): string {
  switch (route.page) {
    case 'board':
      return '/'
    case 'quiet':
      return '/quiet'
    case 'settings':
      return '/settings'
    case 'repo':
      return `/repos/${encodeURIComponent(route.id)}`
  }
}

export function navigate(route: Route): void {
  history.pushState(null, '', href(route))
  dispatchEvent(new PopStateEvent('popstate'))
}

export function useRoute(): Route {
  const [route, setRoute] = useState(() => parseRoute(location.pathname))
  useEffect(() => {
    const onChange = () => setRoute(parseRoute(location.pathname))
    addEventListener('popstate', onChange)
    return () => removeEventListener('popstate', onChange)
  }, [])
  return route
}

/** Lets plain anchors use the in-app router. */
export function onLinkClick(event: React.MouseEvent<HTMLAnchorElement>): void {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return
  event.preventDefault()
  navigate(parseRoute(new URL(event.currentTarget.href).pathname))
}
