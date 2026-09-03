<script lang="ts">
  import { api, type HubStatus, type StatusView } from './lib/api'
  import Unlock from './components/Unlock.svelte'
  import HubConnect from './components/HubConnect.svelte'
  import Buy from './components/Buy.svelte'
  import Nodes from './components/Nodes.svelte'
  import Connection from './components/Connection.svelte'

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
      <div class="hub">
        <span class="label">Hub</span>
        <span class="url">{hub?.name || status.hub_url}</span>
      </div>
      <div class="balance">
        <span class="sats">{status.balance_sats.toLocaleString()}</span>
        <span class="unit">sats</span>
      </div>
    </header>

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

  .hub {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }

  .label {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 1px;
    color: var(--muted);
  }

  .url {
    font-size: 13px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 200px;
  }

  .balance {
    display: flex;
    align-items: baseline;
    gap: 4px;
  }

  .sats {
    font-size: 20px;
    font-weight: 600;
    color: var(--accent);
  }

  .unit {
    font-size: 11px;
    color: var(--muted);
  }

  nav {
    display: flex;
    gap: 4px;
    padding: 10px 24px 0;
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
