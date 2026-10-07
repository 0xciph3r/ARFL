import { api, type Session } from './api'
import { prefs } from './prefs.svelte'

// Whether IPv6 can leave this machine outside the IPv4-only tunnel. If the
// check itself fails the answer is unknown, which must not read as safe.
export async function ipv6Exposed(): Promise<boolean> {
  try {
    return await api.ipv6Exposed()
  } catch {
    return true
  }
}

export type Protected = { session: Session; ipv6Exposed: boolean; ipv6Off: boolean; ipv6Error?: string }

// connectProtected is the one way to connect, from the main window or the
// menu bar: it brings the tunnel up and then applies the IPv6 preference
// before the session is reported as connected.
export async function connectProtected(perHopSats: number): Promise<Protected> {
  const session = await api.connect(perHopSats)
  const exposed = await ipv6Exposed()
  if (!exposed || !prefs.ipv6Auto) return { session, ipv6Exposed: exposed, ipv6Off: false }
  try {
    await api.disableIPv6()
    return { session, ipv6Exposed: exposed, ipv6Off: true }
  } catch (err) {
    return { session, ipv6Exposed: exposed, ipv6Off: false, ipv6Error: (err as Error).message }
  }
}
