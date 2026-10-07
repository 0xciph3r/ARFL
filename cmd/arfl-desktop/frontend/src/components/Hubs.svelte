<script lang="ts">
  import { api, type KnownHub } from '../lib/api'
  import { gbFromTokens, tokensFromSats } from '../lib/units'

  let { currentUrl, connected, onSwitch }: {
    currentUrl: string
    connected: boolean
    // Connects to the hub; the parent owns the switch so it happens once.
    onSwitch: (url: string) => Promise<void>
  } = $props()

  let hubs = $state<KnownHub[]>([])
  let loading = $state(true)
  let busy = $state(false)
  let error = $state('')
  let hubInput = $state('')

  async function load() {
    loading = true
    try {
      hubs = await api.knownHubs()
    } catch (err) {
      error = (err as Error).message
    } finally {
      loading = false
    }
  }

  $effect(() => {
    load()
  })

  async function pick(h: KnownHub) {
    if (h.url === currentUrl || busy) return
    busy = true
    error = ''
    try {
      // Switching hubs ends the session: tokens only work at their own hub.
      if (connected) await api.disconnect()
      await onSwitch(h.url)
    } catch (err) {
      error = (err as Error).message
    } finally {
      busy = false
    }
  }

  async function addHub() {
    const raw = hubInput.trim()
    if (!raw) return
    error = ''
    if (raw.startsWith('npub')) {
      error = 'Finding a hub by its npub is not supported yet. Paste the hub URL.'
      return
    }
    busy = true
    try {
      const preview = await api.previewHub(raw.includes('://') ? raw : 'https://' + raw)
      if (!hubs.some((h) => h.url === preview.url)) {
        hubs = [...hubs, { ...preview, sats: 0, reachable: true, custom: true } as KnownHub]
      }
      hubInput = ''
    } catch (err) {
      error = (err as Error).message
    } finally {
      busy = false
    }
  }

  // The address is always shown: hubs choose their own names, and two can share one.
  const host = (url: string) => url.replace(/^https?:\/\//, '')
  const meta = (h: KnownHub) =>
    h.reachable ? `${host(h.url)} · Margin ${h.margin_pct}% · ${h.node_count} approved nodes` : `${host(h.url)} · Not reachable right now`
</script>

<div class="col">
  <div class="lede">Hubs are run by independent operators. Each sets its own prices and approves its own nodes. Tokens only work at the hub that sold them.</div>
  <div class="label">Your hubs</div>
  <div class="list">
    {#if loading}<div class="note">Looking up hubs…</div>{/if}
    {#each hubs as h (h.url)}
      {@const on = h.url === currentUrl}
      <button class="bare hub" class:on disabled={busy} onclick={() => pick(h)}>
        <span class="radio" class:on></span>
        <div class="grow">
          <div class="title">
            <span class="name">{h.name}</span>
            <span class="tag" class:warn={h.custom}>{h.custom ? 'Not verified' : 'Trusted by ARFL'}</span>
          </div>
          <div class="meta">{meta(h)}</div>
        </div>
        <div class="right">
          <div class="mono">{gbFromTokens(tokensFromSats(h.sats))} GB</div>
          <div class="meta">your balance</div>
        </div>
      </button>
    {/each}
  </div>

  <div class="add">
    <label for="hubadd" class="field">Add a hub</label>
    <div class="add-row">
      <input id="hubadd" type="text" class="mono" placeholder="Hub URL or npub1…" bind:value={hubInput} />
      <button class="secondary" disabled={busy} onclick={addHub}>Add</button>
    </div>
    <div class="note">ARFL reads the hub's public info and shows its margin and prices before you pay. A hub you add yourself is marked Not verified.</div>
  </div>
  {#if error}<div class="err" role="alert">{error}</div>{/if}
  <div class="grow"></div>
  <div class="note foot">Switching hubs disconnects you. Your balance at each hub stays where it is.</div>
</div>

<style>
  .col {
    display: flex;
    flex-direction: column;
    flex: 1;
  }

  .grow {
    flex: 1;
    min-width: 0;
  }

  .lede {
    font-size: 14px;
    color: var(--muted);
    line-height: 1.5;
  }

  .label {
    font-size: 12.5px;
    color: var(--muted);
    margin-top: 18px;
  }

  .list {
    margin-top: 8px;
    border-top: 1px solid var(--line);
  }

  .hub {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 14px 4px;
    border-bottom: 1px solid var(--line);
    color: var(--text);
  }

  .hub.on {
    background: var(--surf);
  }

  .radio {
    width: 16px;
    height: 16px;
    border-radius: 8px;
    flex: none;
    border: 2px solid var(--line2);
  }

  .radio.on {
    border-color: var(--accent);
    background: var(--accent);
  }

  .title {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .name {
    font-size: 15px;
    font-weight: 600;
  }

  .tag {
    font-size: 12px;
    font-weight: 500;
    color: var(--muted);
  }

  .tag.warn {
    color: var(--amber);
  }

  .meta {
    font-size: 12.5px;
    color: var(--muted);
    margin-top: 3px;
  }

  .right {
    text-align: right;
    flex: none;
    font-size: 13px;
  }

  .right .meta {
    font-size: 12px;
    margin-top: 2px;
  }

  .add {
    margin-top: 22px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .field {
    font-size: 13.5px;
    font-weight: 500;
  }

  .add-row {
    display: flex;
    gap: 8px;
  }

  .add-row input {
    flex: 1;
    min-width: 0;
    font-size: 13px;
  }

  .add-row button {
    flex: none;
    min-height: 44px;
    padding: 0 18px;
    border-radius: 8px;
    font-size: 14px;
  }

  .note {
    font-size: 12.5px;
    color: var(--muted);
    line-height: 1.5;
  }

  .foot {
    padding-top: 16px;
  }

  .err {
    margin-top: 12px;
    font-size: 13px;
    color: var(--red);
  }
</style>
