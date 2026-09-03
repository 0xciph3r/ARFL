<script lang="ts">
  import { api, type Session, type StatusView } from '../lib/api'

  let { status, onChanged }: {
    status: StatusView
    onChanged: () => void
  } = $props()

  // Each hop is paid separately, so a connection costs twice this. The amount
  // is shown before spending because tokens handed to a node are gone whether
  // or not the tunnel comes up.
  const PER_HOP_SATS = 32

  let session = $state<Session | null>(null)
  let busy = $state(false)
  let error = $state('')

  const total = PER_HOP_SATS * 2
  const connected = $derived(status.state === 'connected')
  const affordable = $derived(status.balance_sats >= total)

  async function connect() {
    busy = true
    error = ''
    try {
      session = await api.connect(PER_HOP_SATS)
      onChanged()
    } catch (err) {
      error = (err as Error).message
      // The balance moves even on a failed attempt: proofs already handed to a
      // node cannot be reclaimed, so re-read rather than assuming it is intact.
      onChanged()
    } finally {
      busy = false
    }
  }

  async function disconnect() {
    busy = true
    error = ''
    try {
      await api.disconnect()
      session = null
    } catch (err) {
      // The session is cleared server-side even when teardown fails, so the
      // error is shown but the UI must not stay stuck on "connected".
      error = (err as Error).message
      session = null
    } finally {
      busy = false
      onChanged()
    }
  }
</script>

<footer>
  <div class="status" class:live={connected}>
    <span class="dot"></span>
    {connected ? 'Protected · two-hop tunnel' : 'Not protected · traffic is direct'}
  </div>

  {#if !status.tunnel_ready}
    <button disabled title={status.tunnel_error}>Connect</button>
    <p class="note warn">
      {status.tunnel_error || 'Privileged networking unavailable.'}
    </p>
    <p class="note">Run ARFL with administrator rights to enable the tunnel.</p>
  {:else if connected}
    <button class="danger" onclick={disconnect} disabled={busy}>
      {busy ? 'Disconnecting…' : 'Disconnect'}
    </button>
    {#if session}
      <p class="note">
        Two hops · {session.spent_sats.toLocaleString()} sats spent
      </p>
    {/if}
  {:else}
    <button onclick={connect} disabled={busy || !affordable}>
      {busy ? 'Connecting…' : 'Connect'}
    </button>
    {#if affordable}
      <p class="note">{total} sats · {PER_HOP_SATS} per hop, spent on connect</p>
    {:else}
      <p class="note warn">Need {total} sats to connect. Buy more first.</p>
    {/if}
  {/if}

  {#if error}
    <p class="note warn" role="alert">{error}</p>
  {/if}
</footer>

<style>
  footer {
    padding: 14px 24px 20px;
    border-top: 1px solid var(--border);
  }

  button {
    width: 100%;
  }

  .danger {
    background: var(--err);
    /* The base button carries a purple glow; on a destructive control that
     * reads as unfinished rather than deliberate. */
    box-shadow: none;
  }

  /* Whether traffic is actually protected must never be inferred from which
   * button happens to be showing. */
  .status {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 7px;
    margin-bottom: 12px;
    font-size: 11px;
    letter-spacing: 0.4px;
    text-transform: uppercase;
    color: var(--muted);
  }

  .status .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--muted);
  }

  .status.live {
    color: var(--ok);
  }

  .status.live .dot {
    background: var(--ok);
    box-shadow: 0 0 8px var(--ok);
  }

  .note {
    margin: 8px 0 0;
    text-align: center;
    font-size: 11px;
    color: var(--muted);
  }

  .warn {
    color: var(--err);
  }
</style>
