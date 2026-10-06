// Thin typed wrapper over the generated Wails v3 bindings.
//
// Every call funnels through call() so the UI always receives a real Error
// with a readable message, whatever shape the runtime rejected with.
import { Bridge } from '../../bindings/github.com/Radi-Labs/ARFL/cmd/arfl-desktop'
import type {
  HubPreview as HubPreviewModel,
  KnownHub as KnownHubModel,
  RestoredHub as RestoredHubModel,
  SetupView as SetupViewModel,
  StatusView as StatusViewModel,
  UsageView as UsageViewModel,
  VaultStateView as VaultStateViewModel,
} from '../../bindings/github.com/Radi-Labs/ARFL/cmd/arfl-desktop/models'
import type {
  HubStatus as HubStatusModel,
  Invoice as InvoiceModel,
  PinnedPair as PinnedPairModel,
  Session as SessionModel,
} from '../../bindings/github.com/Radi-Labs/ARFL/internal/app/models'
import type { NodeInfo as NodeInfoModel } from '../../bindings/github.com/Radi-Labs/ARFL/pkg/types/models'

export type StatusView = StatusViewModel
export type HubStatus = HubStatusModel
export type Invoice = InvoiceModel
export type NodeInfo = NodeInfoModel
export type Session = SessionModel
export type VaultStateView = VaultStateViewModel
export type Pinned = PinnedPairModel
export type SetupView = SetupViewModel
export type HubPreview = HubPreviewModel
export type RestoredHub = RestoredHubModel
export type KnownHub = KnownHubModel
export type UsageView = UsageViewModel

async function call<T>(fn: () => Promise<T>): Promise<T> {
  try {
    return await fn()
  } catch (err) {
    if (err instanceof Error) throw err
    const msg = typeof err === 'string' ? err : (err as { message?: string })?.message
    throw new Error(msg || String(err))
  }
}

// Go returns pointers, which the bindings type as nullable. A nil from these
// calls is a bug on the Go side, so the UI treats the value as present.
async function value<T>(fn: () => Promise<T | null>): Promise<T> {
  return (await call(fn)) as T
}

// An empty Go slice arrives as null; the UI always works with arrays.
async function list<T>(fn: () => Promise<T[] | null>): Promise<T[]> {
  return (await call(fn)) ?? []
}

export const api = {
  locked: () => call(() => Bridge.Locked()),
  status: () => value(() => Bridge.Status()),
  connectHub: (url: string) => value(() => Bridge.ConnectHub(url)),
  balance: () => call(() => Bridge.Balance()),
  purchase: (amountSats: number) => value(() => Bridge.Purchase(amountSats)),
  awaitPurchase: (quoteId: string) => call(() => Bridge.AwaitPurchase(quoteId)),
  listNodes: () => list(() => Bridge.ListNodes()),
  vaultState: () => value(() => Bridge.VaultState()),
  connect: (perHopSats: number) => value(() => Bridge.Connect(perHopSats)),
  session: () => call(() => Bridge.Session()),
  disconnect: () => call(() => Bridge.Disconnect()),
  pinPair: (entryId: string, exitId: string) => call(() => Bridge.PinPair(entryId, exitId)),
  unpinPair: () => call(() => Bridge.UnpinPair()),
  pinnedPair: () => call(() => Bridge.PinnedPair()),
  setup: () => value(() => Bridge.Setup()),
  knownHubs: () => list(() => Bridge.KnownHubs()),
  usage: () => value(() => Bridge.Usage()),
  ipv6Exposed: () => call(() => Bridge.IPv6Exposed()),
  disableIPv6: () => call(() => Bridge.DisableIPv6()),
  heldSats: () => call(() => Bridge.HeldSats()),
  keyTransfer: () => call(() => Bridge.KeyTransfer()),
  openAtLogin: () => call(() => Bridge.OpenAtLogin()),
  setOpenAtLogin: (on: boolean) => call(() => Bridge.SetOpenAtLogin(on)),
  createKey: () => call(() => Bridge.CreateKey()),
  openWallet: () => value(() => Bridge.OpenWallet()),
  upgradeLegacy: (passphrase: string) => value(() => Bridge.UpgradeLegacy(passphrase)),
  fingerprint: () => call(() => Bridge.Fingerprint()),
  recommendedHubs: () => list(() => Bridge.RecommendedHubs()),
  previewHub: (url: string) => value(() => Bridge.PreviewHub(url)),
  exportBackup: (passphrase: string) => call(() => Bridge.ExportBackup(passphrase)),
  chooseBackupFile: () => call(() => Bridge.ChooseBackupFile()),
  restoreBackup: (path: string, passphrase: string) => list(() => Bridge.RestoreBackup(path, passphrase)),
  showMain: (overlay = '') => call(() => Bridge.ShowMain(overlay)),
}
