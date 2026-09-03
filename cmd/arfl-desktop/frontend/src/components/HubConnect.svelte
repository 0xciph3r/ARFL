<script lang="ts">
  // Hub selection is deliberately unopinionated: the user types any URL and
  // the client talks to it. ARFL ships no default hub — we are the protocol,
  // not an operator.
  import { api, type HubStatus } from '../lib/api'

  let { onConnected }: { onConnected: (hub: HubStatus) => void } = $props()

  let url = $state('')
  let busy = $state(false)
  let error = $state('')

  async function submit(event: Event) {
    event.preventDefault()
    if (!url.trim() || busy) return

    busy = true
    error = ''
    try {
      onConnected(await api.connectHub(url.trim()))
    } catch (err) {
      error = (err as Error).message
    } finally {
      busy = false
    }
  }
</script>

<form class="hub" onsubmit={submit}>
  <h2>Connect to a hub</h2>
  <p class="lede">
    Hubs are run independently. ARFL does not operate one — point the client at
    whichever you trust. Your balance is held with the hub that issued it and
    cannot be spent at another.
  </p>

  <input
    bind:value={url}
    placeholder="https://hub.example.com"
    autocomplete="off"
    autocapitalize="off"
    spellcheck="false"
  />

  {#if error}
    <p class="error" role="alert">{error}</p>
  {/if}

  <button type="submit" disabled={!url.trim() || busy}>
    {busy ? 'Connecting…' : 'Connect'}
  </button>
</form>

<style>
  .hub {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 24px;
  }

  h2 {
    margin: 0;
    font-size: 17px;
  }

  .lede {
    margin: 0 0 4px;
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
</style>
