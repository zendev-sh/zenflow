import DefaultTheme from 'vitepress/theme'
import type { Theme } from 'vitepress'

import Asciinema from './components/Asciinema.vue'
import './style.css'

// Static HTML pages in public/ (e.g. /agent-orchestration.html) are not
// part of the VitePress SPA route table. The default router intercepts
// internal link clicks and 404s on those paths until the user
// hard-refreshes. Hook the router's beforeRouteChange to detect those
// paths and force a full-page navigation so Vite serves the static file
// directly. Same-tab UX preserved (no target=_blank needed).
const STATIC_HTML_PATHS = ['/agent-orchestration.html']

function isStaticHtmlPath(to: string): boolean {
  if (typeof window === 'undefined') return false
  const path = to.split(/[?#]/, 1)[0]
  return STATIC_HTML_PATHS.some((p) => path === p || path.endsWith(p))
}

// Google Analytics 4 page-view tracking on SPA route changes.
// VitePress is a single-page app so the initial gtag('config', ...) call
// in config.ts only records the first page load. Each subsequent
// router.onAfterRouteChange must fire its own page_view event for GA to
// see internal navigation. No-ops when gtag is absent (GA disabled in
// config.ts, or blocked by an extension).
function trackPageView(path: string): void {
  if (typeof window === 'undefined') return
  const gtag = (window as unknown as { gtag?: (...args: unknown[]) => void }).gtag
  if (typeof gtag !== 'function') return
  gtag('event', 'page_view', { page_path: path })
}

export default {
  extends: DefaultTheme,
  enhanceApp({ app, router }) {
    app.component('Asciinema', Asciinema)
    if (typeof window !== 'undefined' && router) {
      const previousHook = router.onBeforeRouteChange
      router.onBeforeRouteChange = (to: string) => {
        if (isStaticHtmlPath(to)) {
          window.location.assign(to)
          return false
        }
        return previousHook ? previousHook(to) : undefined
      }
      const previousAfter = router.onAfterRouteChange
      router.onAfterRouteChange = (to: string) => {
        trackPageView(to)
        if (previousAfter) previousAfter(to)
      }
    }
  },
} satisfies Theme
