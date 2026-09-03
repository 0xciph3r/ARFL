// Thin typed wrapper over the generated Wails bindings.
//
// Wails rejects a Go error by rejecting the promise with a bare string, which
// renders as "[object Object]" if handed straight to a template. Everything
// funnels through call() so the UI always receives a real Error.
import {
  AwaitPurchase,
  Balance,
  Connect,
  ConnectHub,
  Disconnect,
  ListNodes,
  Locked,
  Purchase,
  Session,
  Status,
  Unlock,
} from '../../wailsjs/go/main/Bridge'
import type { app, main, types } from '../../wailsjs/go/models'

async function call<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn()
  } catch (err) {
    throw new Error(typeof err === 'string' ? err : String(err))
  }
}

export type StatusView = main.StatusView
export type HubStatus = app.HubStatus
export type Invoice = app.Invoice
export type NodeInfo = types.NodeInfo
export type Session = app.Session

export const api = {
  locked: () => call(() => Locked()),
  unlock: (passphrase: string) => call(() => Unlock(passphrase)),
  status: () => call(() => Status()),
  connectHub: (url: string) => call(() => ConnectHub(url)),
  balance: () => call(() => Balance()),
  purchase: (amountSats: number) => call(() => Purchase(amountSats)),
  awaitPurchase: (quoteId: string) => call(() => AwaitPurchase(quoteId)),
  listNodes: () => call(() => ListNodes()),
  connect: (perHopSats: number) => call(() => Connect(perHopSats)),
  session: () => call(() => Session()),
  disconnect: () => call(() => Disconnect()),
}
