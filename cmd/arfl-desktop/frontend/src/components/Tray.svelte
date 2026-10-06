<script lang="ts">
  import { onDestroy } from 'svelte'
  import { Events } from '@wailsio/runtime'
  import { api, type Session, type StatusView } from '../lib/api'
  import { isLight, prefs } from '../lib/prefs.svelte'
  import { cue } from '../lib/sound'
  import { TOKEN_SATS, gbFromTokens, tokensFromSats } from '../lib/units'
  import mark from '../assets/mark.svg'
  import wordDark from '../assets/wordmark-dark.png'
  import wordLight from '../assets/wordmark-light.png'
  import eyes from '../assets/eyes-hero.png'

  let status = $state<StatusView | null>(null)
  let session = $state<Session | null>(null)
  let busy = $state(false)
  let error = $state('')

  async function refresh() {
    try {
      status = await api.status()
      session = status.state === 'connected' ? await api.session() : null
    } catch (err) {
      error = (err as Error).message
    }
  }

  refresh()
  const offState = Events.On('arfl:state', () => refresh())
  // The popover is shown and hidden rather than reloaded, so re-read on focus.
  window.addEventListener('focus', refresh)
  onDestroy(() => {
    offState()
    window.removeEventListener('focus', refresh)
  })

  const connected = $derived(status?.state === 'connected')
  const tokens = $derived(tokensFromSats(status?.balance_sats ?? 0))
  const operator = (id: string | undefined) => (id ? 'Operator ' + id.slice(0, 4) : 'Not set')
  const wordSrc = $derived(prefs.theme && isLight() ? wordLight : wordDark)

  async function toggle() {
    if (busy || !status) return
    busy = true
    error = ''
    try {
      if (connected) {
        cue('down')
        await api.disconnect()
      } else if (tokens < 2) {
        await api.showMain('topup')
      } else {
        await api.connect(TOKEN_SATS)
        cue('up')
      }
    } catch (err) {
      error = (err as Error).message
    } finally {
      busy = false
      refresh()
    }
  }

  const label = $derived(
    busy ? (connected ? 'Disconnecting…' : 'Connecting…') : connected ? 'Disconnect' : tokens < 2 ? 'Top up to connect' : 'Connect',
  )
</script>

<div class="pop">
  <div class="head">
    <div class="lockup"><img src={mark} alt="" class="mark" /><img src={wordSrc} alt="ARFL" class="word" /></div>
    <div class="left"><span class="mono">{gbFromTokens(tokens)} GB</span> left</div>
  </div>

  <div class="eyes-wrap"><img src={eyes} alt="" class="eyes" class:on={connected} /></div>
  <div class="status">
    <div class="small" style="color:{connected ? 'var(--cyan)' : 'var(--muted)'}">
      <span class="dot" style="background:{connected ? 'var(--cyan)' : 'var(--muted)'}"></span>{connected ? 'Connected' : 'Disconnected'}
    </div>
    <div class="word-status">{connected ? 'Protected' : 'Not connected'}</div>
    {#if error}<div class="err">{error}</div>{/if}
  </div>

  <div class="rows">
    <div class="row"><span class="k">Entry</span><span class="v">{connected ? operator(session?.config?.entry?.node_id) : 'Not set'}</span></div>
    <div class="row"><span class="k">Exit</span><span class="v">{connected ? operator(session?.config?.exit?.node_id) : 'Not set'}</span></div>
  </div>

  <div class="grow"></div>

  <div class="actions">
    <button class="conn" class:on={connected} disabled={busy || (!connected && !status?.tunnel_ready && tokens >= 2)} onclick={toggle}>{label}</button>
    <div class="pair">
      <button class="secondary" onclick={() => api.showMain('topup')}>Top up</button>
      <button class="secondary" onclick={() => api.showMain()}>Open ARFL</button>
    </div>
  </div>
</div>

<style>
  .pop {
    height: 100vh;
    box-sizing: border-box;
    background: var(--bg);
    color: var(--text);
    border: 1px solid var(--line);
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }

  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 14px 16px;
    border-bottom: 1px solid var(--line);
  }

  .lockup {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .mark {
    width: 22px;
    height: 22px;
  }

  .word {
    height: 15px;
    width: auto;
  }

  .left {
    font-size: 13px;
    color: var(--muted);
  }

  .left .mono {
    color: var(--text);
  }

  .eyes-wrap {
    padding: 16px 16px 0;
    display: flex;
    justify-content: center;
  }

  .eyes {
    width: 240px;
    height: auto;
    margin-bottom: 10px;
    filter: var(--eyesOff);
    opacity: var(--eyesOffO);
    transition: filter 0.6s ease, opacity 0.6s ease;
  }

  .eyes.on {
    filter: drop-shadow(0 0 22px var(--glow));
    opacity: 1;
  }

  .status {
    padding: 0 16px;
  }

  .small {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    font-weight: 500;
  }

  .dot {
    width: 8px;
    height: 8px;
    border-radius: 4px;
  }

  .word-status {
    font-size: 24px;
    font-weight: 600;
    margin-top: 6px;
    letter-spacing: -0.01em;
  }

  .err {
    font-size: 12.5px;
    color: var(--red);
    margin-top: 6px;
    line-height: 1.4;
  }

  .rows {
    margin: 18px 16px 0;
    border-top: 1px solid var(--line);
  }

  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    min-height: 44px;
    border-bottom: 1px solid var(--line);
  }

  .k {
    font-size: 13px;
    color: var(--muted);
  }

  .v {
    font-size: 13.5px;
    font-weight: 500;
  }

  .grow {
    flex: 1;
  }

  .actions {
    padding: 0 16px 16px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .conn {
    min-height: 48px;
    border-radius: 10px;
    font-size: 15px;
    text-align: center;
  }

  .conn.on {
    background: transparent;
    border: 1.5px solid var(--alt);
    color: var(--alt);
  }

  .pair {
    display: flex;
    gap: 8px;
  }

  .pair button {
    flex: 1;
    min-height: 40px;
    border-radius: 8px;
    font-size: 13.5px;
    text-align: center;
  }
</style>
