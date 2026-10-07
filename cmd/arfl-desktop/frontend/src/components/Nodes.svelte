<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type NodeInfo, type Pinned, type Session } from '../lib/api'

  let { hubName, session, connected }: {
    hubName: string
    session: Session | null
    connected: boolean
  } = $props()

  type Slot = 'entry' | 'exit'

  let nodes = $state<NodeInfo[]>([])
  let pinned = $state<Pinned | null>(null)
  let error = $state('')
  let loadError = $state('')
  let loading = $state(false)
  let adv = $state(false)
  let slot = $state<Slot>('entry')
  let query = $state('')
  // A pick made in the list; becomes the pin once both ends are chosen.
  let draft = $state<{ entry: string; exit: string }>({ entry: '', exit: '' })

  async function load() {
    if (loading) return
    loading = true
    loadError = ''
    try {
      ;[nodes, pinned] = await Promise.all([api.listNodes(), api.pinnedPair()])
      if (pinned) draft = { entry: pinned.entry_id, exit: pinned.exit_id }
    } catch (err) {
      nodes = []
      loadError = (err as Error).message
    } finally {
      loading = false
    }
  }

  onMount(() => { void load() })

  const nodeLabel = (n: NodeInfo | undefined) => (n ? 'Node ' + n.nostr_pubkey.slice(0, 4) : '')
  const shortKey = (k: string) => (k.length > 14 ? k.slice(0, 8) + '…' + k.slice(-4) : k)
  const byId = (id: string | undefined) => nodes.find((n) => n.id === id)
  const meta = (n: NodeInfo | undefined) =>
    n ? `${n.upload_mbps} Mbps · ${n.capacity ? Math.round((n.load / n.capacity) * 100) : n.load}% load` : ''
  const canEntry = (n: NodeInfo) => n.role === 'entry' || n.role === 'both'
  const canExit = (n: NodeInfo) => n.role === 'exit' || n.role === 'both'

  // What the top section describes: the live route, else the user's pin.
  const shownEntry = $derived(connected ? byId(session?.config?.entry?.node_id) : byId(pinned?.entry_id))
  const shownExit = $derived(connected ? byId(session?.config?.exit?.node_id) : byId(pinned?.exit_id))
  const caption = $derived(
    pinned
      ? `You picked these. The hub's operator IDs are checked when available, but separate ownership is not guaranteed.${connected ? ' A change applies the next time you connect.' : ''}`
      : connected
        ? 'Picked at random on this device from this hub’s approved nodes. Separate operators are not guaranteed.'
        : 'A pair is picked at random on this device each time you connect, from the nodes this hub has approved.',
  )

  const otherNode = $derived(byId(slot === 'entry' ? draft.exit : draft.entry))
  const sameHubOperator = (a: NodeInfo, b: NodeInfo | undefined) =>
    !!b && (a.operator_id && b.operator_id
      ? a.operator_id === b.operator_id
      : a.nostr_pubkey === b.nostr_pubkey)

  const list = $derived.by(() => {
    const q = query.trim().toLowerCase()
    return nodes
      .filter((n) => (slot === 'entry' ? canEntry(n) : canExit(n)))
      .filter((n) => !q || nodeLabel(n).toLowerCase().includes(q) || n.nostr_pubkey.toLowerCase().includes(q))
  })

  async function pick(n: NodeInfo) {
    draft = slot === 'entry' ? { ...draft, entry: n.id } : { ...draft, exit: n.id }
    if (slot === 'entry' && !draft.exit) slot = 'exit'
    if (!draft.entry || !draft.exit) return
    error = ''
    try {
      await api.pinPair(draft.entry, draft.exit)
      pinned = await api.pinnedPair()
    } catch (err) {
      error = (err as Error).message
    }
  }

  async function useAuto() {
    error = ''
    try {
      await api.unpinPair()
      pinned = null
      draft = { entry: '', exit: '' }
    } catch (err) {
      error = (err as Error).message
    }
  }
</script>

<div class="wrap">
  <div class="top">
    <div class="lede">Your device picks the pair from the nodes {hubName} has approved. The hub does not choose for you.</div>
    <div class="pair">
      <div class="hop">
        <div><div class="k">Entry</div><div class="v">{shownEntry ? nodeLabel(shownEntry) : 'Picked when you connect'}</div></div>
        <div class="mono meta">{meta(shownEntry)}</div>
      </div>
      <div class="hop">
        <div><div class="k">Exit</div><div class="v">{shownExit ? nodeLabel(shownExit) : 'Picked when you connect'}</div></div>
        <div class="mono meta">{meta(shownExit)}</div>
      </div>
    </div>
    <div class="caption">{caption}</div>
    {#if pinned}
      <button class="bare link" onclick={useAuto}>Use the automatic pair</button>
    {/if}
    {#if loadError}
      <div class="err" role="alert">
        <span>{loadError}</span>
        <button class="bare link" disabled={loading} onclick={load}>Try again</button>
      </div>
    {:else if loading}
      <div class="caption">Loading nodes…</div>
    {/if}
    {#if error}<div class="err" role="alert">{error}</div>{/if}
  </div>

  <button class="bare toggle" onclick={() => (adv = !adv)}>
    <span>{adv ? 'Hide node list' : 'Choose nodes yourself'}</span>
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d={adv ? 'M6 15l6-6 6 6' : 'M6 9l6 6 6-6'} /></svg>
  </button>

  {#if adv}
    <div class="slots">
      <button class="bare slot" class:on={slot === 'entry'} onclick={() => (slot = 'entry')}>
        <div class="k">Entry</div><div class="v">{nodeLabel(byId(draft.entry)) || 'Not chosen'}</div>
      </button>
      <button class="bare slot" class:on={slot === 'exit'} onclick={() => (slot = 'exit')}>
        <div class="k">Exit</div><div class="v">{nodeLabel(byId(draft.exit)) || 'Not chosen'}</div>
      </button>
    </div>
    <div class="search">
      <input type="text" placeholder="Search this hub's nodes" aria-label="Search nodes" bind:value={query} />
      <div class="caption">Picking a node here replaces the automatic pair.</div>
    </div>
    {#if !loading && !loadError && list.length === 0}
      <div class="empty">No {slot} nodes match.</div>
    {/if}
    {#each loading || loadError ? [] : list as n (n.id)}
      {@const sel = (slot === 'entry' ? draft.entry : draft.exit) === n.id}
      {@const clash = !sel && sameHubOperator(n, otherNode)}
      <button class="bare node" class:sel class:clash disabled={clash} onclick={() => pick(n)}>
        <span class="radio" class:on={sel}></span>
        <div class="grow">
          <div class="name">{nodeLabel(n)}{#if sel}<span class="tag">Selected</span>{/if}</div>
          <div class="mono key">{shortKey(n.nostr_pubkey)}</div>
          {#if clash}<div class="reason">Hub lists the same operator or node as your {slot === 'entry' ? 'exit' : 'entry'}</div>{/if}
        </div>
        <div class="right">
          <div class="mono">{n.upload_mbps} Mbps</div>
          <div class="small">{meta(n).split(' · ')[1]}</div>
        </div>
      </button>
    {/each}
  {/if}
  <div class="foot">Approval lasts a few hours and the hub can revoke it at any time.</div>
</div>

<style>
  .wrap {
    display: flex;
    flex-direction: column;
    margin: -20px;
  }

  .top {
    padding: 20px;
    border-bottom: 1px solid var(--line);
  }

  .lede {
    font-size: 14px;
    color: var(--text2);
    line-height: 1.5;
  }

  .pair {
    margin-top: 14px;
    border-top: 1px solid var(--line);
  }

  .hop {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 0;
    border-bottom: 1px solid var(--line);
  }

  .k {
    font-size: 12px;
    color: var(--muted);
  }

  .v {
    font-size: 14.5px;
    font-weight: 500;
    margin-top: 2px;
  }

  .meta {
    font-size: 12px;
    color: var(--muted);
    text-align: right;
  }

  .caption {
    font-size: 12.5px;
    color: var(--muted);
    margin-top: 10px;
    line-height: 1.5;
  }

  .search .caption {
    font-size: 12px;
    margin-top: 8px;
  }

  .link {
    margin-top: 8px;
    font-size: 13px;
    font-weight: 500;
    color: var(--accentT);
    text-decoration: underline;
  }

  .err {
    margin-top: 10px;
    font-size: 13px;
    color: var(--red);
  }

  .toggle {
    display: flex;
    align-items: center;
    justify-content: space-between;
    min-height: 52px;
    padding: 0 20px;
    border-bottom: 1px solid var(--line);
    font-size: 14px;
    font-weight: 500;
    color: var(--text2);
  }

  .slots {
    display: flex;
    border-bottom: 1px solid var(--line);
  }

  .slot {
    flex: 1;
    padding: 12px 16px;
    min-height: 60px;
    border-bottom: 2px solid transparent;
    color: var(--text);
  }

  .slot.on {
    border-bottom-color: var(--accent);
    background: var(--surf);
  }

  .slot .v {
    font-size: 14px;
  }

  .search {
    padding: 12px 20px;
    border-bottom: 1px solid var(--line);
  }

  .search input {
    min-height: 40px;
    font-size: 14px;
  }

  .empty {
    padding: 16px 20px;
    font-size: 13px;
    color: var(--muted);
  }

  .node {
    width: 100%;
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--line);
    color: var(--text);
  }

  .node.sel {
    background: var(--surf);
  }

  .node.clash {
    opacity: 0.5;
  }

  .radio {
    width: 16px;
    height: 16px;
    border-radius: 8px;
    flex: none;
    margin-top: 2px;
    border: 2px solid var(--line2);
  }

  .radio.on {
    border-color: var(--accent);
    background: var(--accent);
  }

  .grow {
    flex: 1;
    min-width: 0;
  }

  .name {
    font-size: 14px;
    font-weight: 500;
    display: flex;
    gap: 8px;
    align-items: center;
  }

  .tag {
    font-size: 12px;
    font-weight: 400;
    color: var(--accentT);
  }

  .key {
    font-size: 11.5px;
    color: var(--muted);
    margin-top: 2px;
  }

  .reason {
    font-size: 12px;
    color: var(--amber);
    margin-top: 3px;
  }

  .right {
    text-align: right;
    flex: none;
    font-size: 12.5px;
  }

  .small {
    font-size: 12px;
    color: var(--muted);
    margin-top: 2px;
  }

  .foot {
    padding: 16px 20px;
    font-size: 12px;
    color: var(--muted);
    line-height: 1.55;
  }
</style>
