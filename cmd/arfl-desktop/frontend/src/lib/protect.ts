import { api, type Session } from './api'

export type Protected = { session: Session }

// Tunnel bring-up installs the outbound IPv6 block before a paid session starts.
export async function connectProtected(perHopSats: number): Promise<Protected> {
  const session = await api.connect(perHopSats)
  return { session }
}
