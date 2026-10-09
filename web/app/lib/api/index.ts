import { OneGateClient, ApiError } from "./client"
import type * as T from "./types"

export { ApiError }
export type { T }

/**
 * Singleton API client for the gateway management API. Same-origin in
 * production (embedded dashboard); the Vite dev server proxies /api to
 * 127.0.0.1:7420.
 */
export const api = new OneGateClient()

let csrfToken: string | null = null

/**
 * Refresh the session from the gateway and cache the CSRF token for
 * subsequent mutations. Returns the SessionInfo, or null when
 * unauthenticated (401).
 */
export async function refreshSession(): Promise<T.SessionInfo | null> {
  try {
    const info = await api.getSession()
    csrfToken = info.csrf_token
    api.setCsrfToken(info.csrf_token)
    return info
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      csrfToken = null
      api.setCsrfToken("")
      return null
    }
    throw err
  }
}

/** True when a CSRF token is cached (i.e. a session exists). */
export function hasSession(): boolean {
  return csrfToken !== null
}

/** Format micro-USD as a display string (never floats in transport;
 * presentation may round). */
export function formatMicroUSD(micros: number | null | undefined): string {
  const usd = (micros ?? 0) / 1_000_000
  if (usd === 0) return "$0.00"
  if (usd < 0.01) return `$${usd.toFixed(5)}`
  if (usd < 1000) return `$${usd.toFixed(2)}`
  return `$${usd.toLocaleString("en-US", { maximumFractionDigits: 0 })}`
}

/** Format a unix-ms timestamp in the viewer's locale. */
export function formatMS(ms: number | null | undefined): string {
  if (!ms) return "—"
  return new Date(ms).toLocaleString()
}

/** Relative time for log streams. */
export function timeOnly(ms: number | null | undefined): string {
  if (!ms) return "—"
  return new Date(ms).toLocaleTimeString()
}
