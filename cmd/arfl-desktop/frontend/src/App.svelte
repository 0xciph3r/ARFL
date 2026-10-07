<script lang="ts">
  import { api, type HubStatus, type SetupView, type StatusView } from './lib/api'
  import Onboarding from './components/Onboarding.svelte'
  import Home from './components/Home.svelte'
  import Tray from './components/Tray.svelte'

  // The menu bar popover loads this same app at #tray.
  const isTray = location.hash === '#tray'

  let setup = $state<SetupView | null>(null)
  let status = $state<StatusView | null>(null)
  let hub = $state<HubStatus | null>(null)
  let onboarding = $state(false)
  let openTopup = $state(false)
  let error = $state('')

  async function refresh() {
    status = await api.status()
    if (status.hub_url && !hub) {
      try {
        hub = await api.connectHub(status.hub_url)
      } catch {
        // The name is cosmetic; the URL still identifies the hub.
      }
    }
  }

  // A device with a key opens straight to the main window: the key in the OS
  // keychain unlocks the vault, so there is no password at launch.
  async function boot() {
    try {
      setup = await api.setup()
      if (setup.has_key) {
        status = await api.openWallet()
        onboarding = !status.hub_url
      } else {
        onboarding = true
      }
    } catch (err) {
      error = (err as Error).message
    }
  }

  $effect(() => {
    if (!isTray) boot()
  })

  async function switchHub(url: string) {
    hub = await api.connectHub(url)
    await refresh()
  }

  async function finish(buy: boolean) {
    openTopup = buy
    await refresh()
    // A wallet without a hub has nothing to show; keep choosing one.
    onboarding = !status?.hub_url
  }
</script>

<main>
  {#if isTray}
    <Tray />
  {:else if error}
    <div class="fatal" role="alert">{error}</div>
  {:else if setup && onboarding}
    <Onboarding {setup} startStep={setup.has_key ? 2 : 0} onDone={finish} />
  {:else if status?.unlocked && !status.hub_url && setup}
    <Onboarding setup={{ ...setup, has_key: true }} startStep={2} onDone={finish} />
  {:else if status?.unlocked && status.hub_url}
    <Home {status} {hub} initialOverlay={openTopup ? 'topup' : null} onChanged={refresh} onHubSwitched={switchHub} />
  {/if}
</main>

<style>
  main {
    height: 100%;
    width: 100%;
    overflow: hidden;
  }

  .fatal {
    margin: 80px auto;
    max-width: 480px;
    color: var(--red);
    line-height: 1.5;
    padding: 0 24px;
  }
</style>
