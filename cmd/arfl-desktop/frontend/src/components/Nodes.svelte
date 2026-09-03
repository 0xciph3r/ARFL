<script lang="ts">
  // Node list is informational. The entry/exit pair is chosen randomly on the
  // client at connect time, so this screen deliberately offers no way to pick
  // one — letting users hand-select would weaken the unlinkability the random
  // pairing provides.
  import { api, type NodeInfo } from '../lib/api'

  let nodes = $state<NodeInfo[]>([])
  let loading = $state(true)
  let error = $state('')

  async function load() {
    loading = true
    error = ''
    try {
      nodes = await api.listNodes()
    } catch (err) {
      error = (err as Error).message
    } finally {
      loading = false
    }
  }

  $effect(() => {
    load()
  })
</script>

<div class="nodes">
  <div class="head">
    <h2>Nodes</h2>
    <button class="secondary" onclick={load} disabled={loading}>Refresh</button>
  </div>

  {#if loading}
    <p class="muted">Loading…</p>
  {:else if error}
    <p class="error">{error}</p>
  {:else if nodes.length === 0}
    <p class="muted">This hub has no online nodes right now.</p>
  {:else}
    <p class="muted">
      {nodes.length} online. Your entry and exit are picked at random on this device —
      the hub never learns the pair.
    </p>
    <ul>
      {#each nodes as node (node.nostr_pubkey)}
        <li>
          <div class="row">
            <span class="id">{node.id || node.nostr_pubkey.slice(0, 12)}</span>
            <span class="role">{node.role}</span>
          </div>
          <div class="meta">
            {node.download_mbps} / {node.upload_mbps} Mbps
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .nodes {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 24px;
    overflow-y: auto;
  }

  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }

  .head button {
    padding: 6px 10px;
    font-size: 12px;
  }

  h2 {
    margin: 0;
    font-size: 17px;
  }

  .muted {
    margin: 0;
    font-size: 12px;
    line-height: 1.55;
    color: var(--muted);
  }

  .error {
    margin: 0;
    font-size: 12px;
    color: var(--err);
    word-break: break-word;
  }

  ul {
    list-style: none;
    margin: 4px 0 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  li {
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 10px 12px;
  }

  .row {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }

  .id {
    font-family: "SF Mono", ui-monospace, Menlo, monospace;
    font-size: 12px;
  }

  .role {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 1px;
    color: var(--accent);
  }

  .meta {
    margin-top: 4px;
    font-size: 11px;
    color: var(--muted);
  }
</style>
