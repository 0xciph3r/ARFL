<script lang="ts">
  import { api, type HubStatus, type StatusView } from './lib/api'
  import Unlock from './components/Unlock.svelte'
  import HubConnect from './components/HubConnect.svelte'
  import Buy from './components/Buy.svelte'
  import Nodes from './components/Nodes.svelte'
  import Connection from './components/Connection.svelte'
  import Mark from './components/Mark.svelte'

  type Tab = 'buy' | 'nodes'

  let status = $state<StatusView | null>(null)
  let hub = $state<HubStatus | null>(null)
  let tab = $state<Tab>('buy')

  async function refresh() {
    status = await api.status()
  }

  $effect(() => {
    refresh()
  })

  function onConnected(connected: HubStatus) {
    hub = connected
    refresh()
  }

  function onPurchased(balance: number) {
    if (status) status = { ...status, balance_sats: balance }
  }
</script>

<main>
  {#if !status?.unlocked}
    <Unlock onUnlocked={refresh} />
  {:else if !status.hub_url}
    <HubConnect {onConnected} />
  {:else}
    <header>
      <div class="identity">
        <div class="lockup">
          <Mark size={20} />
          <div class="wordmark" style="--wordmark-h: 14px" role="img" aria-label="ARFL"></div>
        </div>
        <div class="hub" title={status.hub_url}>
          <span class="label">Hub</span>
          <span class="url">{hub?.name || status.hub_url}</span>
        </div>
      </div>
      <div class="balance">
        <span class="sats">{status.balance_sats.toLocaleString()}</span>
        <span class="unit">sats</span>
      </div>
    </header>

    <div class="decorative-hero" aria-hidden="true"></div>

    <nav>
      <button
        class="tab"
        class:active={tab === 'buy'}
        onclick={() => (tab = 'buy')}>Buy</button
      >
      <button
        class="tab"
        class:active={tab === 'nodes'}
        onclick={() => (tab = 'nodes')}>Nodes</button
      >
    </nav>

    <section>
      {#if tab === 'buy'}
        <Buy {onPurchased} />
      {:else}
        <Nodes />
      {/if}
    </section>

    <Connection {status} onChanged={refresh} />
  {/if}
</main>

<style>
  main {
    display: flex;
    flex-direction: column;
    height: 100%;
    width: 100%;
    max-width: 448px;
    margin: 0 auto;
    overflow: hidden;
  }

  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 30px 24px 14px;
    border-bottom: 1px solid var(--border);
    -webkit-app-region: drag;
  }

  .identity {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }

  .lockup {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .hub {
    display: flex;
    align-items: baseline;
    gap: 8px;
    min-width: 0;
  }

  .label {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 1px;
    color: var(--muted);
  }

  .url {
    font-size: 12px;
    color: var(--muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 180px;
  }

  .balance {
    display: flex;
    align-items: baseline;
    gap: 4px;
    flex-shrink: 0;
  }

  .sats {
    font-size: 20px;
    font-weight: 600;
    /* Orange means money everywhere in this UI; see style.css. */
    color: var(--sats);
    font-variant-numeric: tabular-nums;
  }

  .unit {
    font-size: 11px;
    color: var(--muted);
  }

  nav {
    display: flex;
    gap: 4px;
    padding: 4px 24px 0;
  }

  .decorative-hero {
    width: calc(100% - 60px);
    max-width: 360px;
    height: 110px;
    margin: 12px auto 6px;
    border-radius: 12px;
    background-image:
      linear-gradient(180deg, rgba(20, 15, 28, 0.10), rgba(20, 15, 28, 0.34)),
      url(./assets/eyes.png);
    background-size: cover;
    background-position: center;
    filter: grayscale(1) blur(0.7px) contrast(0.92);
    opacity: 0.12;
    pointer-events: none;
  }

  .tab {
    background: none;
    color: var(--muted);
    font-weight: 500;
    font-size: 13px;
    padding: 6px 10px;
    border-bottom: 2px solid transparent;
    border-radius: 0;
  }

  .tab.active {
    color: var(--text);
    border-bottom-color: var(--accent);
  }

  section {
    flex: 1;
    overflow-y: auto;
  }
</style>
