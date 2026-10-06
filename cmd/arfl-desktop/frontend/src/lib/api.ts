// Thin typed wrapper over the generated Wails bindings.
//
// Wails rejects a Go error by rejecting the promise with a bare string, which
// renders as "[object Object]" if handed straight to a template. Everything
// funnels through call() so the UI always receives a real Error.
import {
  AwaitPurchase,
  ChooseBackupFile,
  DisableIPv6,
  HeldSats,
  KeyTransfer,
  OpenAtLogin,
  SetOpenAtLogin,
  IPv6Exposed,
  KnownHubs,
  Usage,
  CreateKey,
  ExportBackup,
  Fingerprint,
  OpenWallet,
  PreviewHub,
  RecommendedHubs,
  RestoreBackup,
  Setup,
  UpgradeLegacy,
  Balance,
  Connect,
  ConnectHub,
  Disconnect,
  ListNodes,
  Locked,
  PinPair,
  PinnedPair,
  UnpinPair,
  Purchase,
  ResetVault,
  Session,
  Status,
  Unlock,
  VaultState,
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
export type VaultStateView = main.VaultStateView
export type Pinned = app.PinnedPair
export type SetupView = main.SetupView
export type HubPreview = main.HubPreview
export type RestoredHub = main.RestoredHub
export type KnownHub = main.KnownHub
export type UsageView = main.UsageView

export const api = {
  locked: () => call(() => Locked()),
  unlock: (passphrase: string) => call(() => Unlock(passphrase)),
  status: () => call(() => Status()),
  connectHub: (url: string) => call(() => ConnectHub(url)),
  balance: () => call(() => Balance()),
  purchase: (amountSats: number) => call(() => Purchase(amountSats)),
  awaitPurchase: (quoteId: string) => call(() => AwaitPurchase(quoteId)),
  listNodes: () => call(() => ListNodes()),
  vaultState: () => call(() => VaultState()),
  resetVault: () => call(() => ResetVault()),
  connect: (perHopSats: number) => call(() => Connect(perHopSats)),
  session: () => call(() => Session()),
  disconnect: () => call(() => Disconnect()),
  pinPair: (entryId: string, exitId: string) => call(() => PinPair(entryId, exitId)),
  unpinPair: () => call(() => UnpinPair()),
  pinnedPair: () => call(() => PinnedPair()),
  setup: () => call(() => Setup()),
  knownHubs: () => call(() => KnownHubs()),
  usage: () => call(() => Usage()),
  ipv6Exposed: () => call(() => IPv6Exposed()),
  disableIPv6: () => call(() => DisableIPv6()),
  heldSats: () => call(() => HeldSats()),
  keyTransfer: () => call(() => KeyTransfer()),
  openAtLogin: () => call(() => OpenAtLogin()),
  setOpenAtLogin: (on: boolean) => call(() => SetOpenAtLogin(on)),
  createKey: () => call(() => CreateKey()),
  openWallet: () => call(() => OpenWallet()),
  upgradeLegacy: (passphrase: string) => call(() => UpgradeLegacy(passphrase)),
  fingerprint: () => call(() => Fingerprint()),
  recommendedHubs: () => call(() => RecommendedHubs()),
  previewHub: (url: string) => call(() => PreviewHub(url)),
  exportBackup: (passphrase: string) => call(() => ExportBackup(passphrase)),
  chooseBackupFile: () => call(() => ChooseBackupFile()),
  restoreBackup: (path: string, passphrase: string) => call(() => RestoreBackup(path, passphrase)),
}
