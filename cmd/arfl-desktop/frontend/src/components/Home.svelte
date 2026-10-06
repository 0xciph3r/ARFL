<script lang="ts">
  import { onDestroy } from 'svelte'
  import { Events } from '@wailsio/runtime'
  import { api, type HubStatus, type Session, type StatusView } from '../lib/api'
  import { isLight, prefs } from '../lib/prefs.svelte'
  import { cue } from '../lib/sound'
  import { TOKEN_SATS, gbFromTokens, tokensFromSats } from '../lib/units'
  import mark from '../assets/mark.svg'
  import wordDark from '../assets/wordmark-dark.png'
  import wordLight from '../assets/wordmark-light.png'
  import eyes from '../assets/eyes-hero.png'
  import Drawer from './Drawer.svelte'
  import TopUp from './TopUp.svelte'
  import Nodes from './Nodes.svelte'
  import SettingsPanel from './SettingsPanel.svelte'
  import Hubs from './Hubs.svelte'
  import Protection from './Protection.svelte'
  import Privacy from './Privacy.svelte'
  import Summary, { type SessionSummary } from './Summary.svelte'
  import Alert from './Alert.svelte'

  let { status, hub, initialOverlay = null, onChanged, onHubSwitched }: {
    status: StatusView
    hub: HubStatus | null
    initialOverlay?: 'topup' | null
    onChanged: () => void
    onHubSwitched: (url: string) => void
  } = $props()

  // Each hop is paid one whole token, so a session costs two.
  const PER_HOP_TOKENS = 1
  const STAGES = ['Choose nodes', 'Send tokens', 'Entry tunnel', 'Exit tunnel']
  const STAGE_MS = 800
  const SEGMENTS = 40

  type Overlay = 'topup' | 'settings' | 'hubs' | 'nodes' | 'protection' | 'privacy' | 'summary'
  const TOKEN_MB = 100
  // Warn when this much is left; matches the canvas's 0.4 GB example.
  const LOW_TOKENS = 4
  // WireGuard re-handshakes about every two minutes while traffic flows.
  const NODE_SILENT_SECS = 180
  const HUB_CHECK_MS = 60_000

  type AlertKind = 'low' | 'out' | 'node' | 'hub'
  let alert = $state<AlertKind | null>(null)
  let lowDismissed = $state(false)
  let silentHop = $state<'entry' | 'exit'>('exit')
  let nodeWarned = false
  let renewing = false
  let hubPreview = $state<{ margin_pct: number; node_count: number } | null>(null)
  let hubReachedAt = $state(Date.now())

  async function checkHub() {
    try {
      hubPreview = await api.previewHub(status.hub_url)
      hubReachedAt = Date.now()
      if (alert === 'hub') alert = null
    } catch {
      if (!alert) alert = 'hub'
    }
  }
  checkHub()
  const hubTimer = setInterval(checkHub, HUB_CHECK_MS)
  const hubMeta = $derived(hubPreview ? `Margin ${hubPreview.margin_pct}% · ${hubPreview.node_count} approved nodes. ` : '')

  // svelte-ignore state_referenced_locally
  let overlay = $state<Overlay | null>(initialOverlay)
  let session = $state<Session | null>(null)
  let stage = $state(-1)
  let closing = $state(false)
  let wake = $state(false)
  let error = $state('')
  let last = $state<SessionSummary | null>(null)
  let now = $state(Date.now())
  let pinnedRoute = $state(false)
  let usage = $state({ rx: 0, tx: 0 })
  let rate = $state({ down: 0, up: 0 })
  let ipv6Exposed = $state(false)
  let ipv6Off = $state(false)
  const ipv6Safe = $derived(!ipv6Exposed || ipv6Off)

  async function checkIPv6() {
    try {
      ipv6Exposed = await api.ipv6Exposed()
    } catch {
      ipv6Exposed = false
    }
  }
  checkIPv6()

  async function fixIPv6() {
    error = ''
    try {
      await api.disableIPv6()
      ipv6Off = true
    } catch (err) {
      error = (err as Error).message
    }
  }

  // Live usage comes from the tunnel's own byte counters, sampled each second.
  let lastSample = { rx: 0, tx: 0, at: 0 }
  async function sampleUsage() {
    try {
      const u = await api.usage()
      if (!u.connected) return
      const at = Date.now()
      if (lastSample.at) {
        const secs = (at - lastSample.at) / 1000
        rate = {
          down: Math.max(0, ((u.rx_bytes - lastSample.rx) * 8) / secs / 1e6),
          up: Math.max(0, ((u.tx_bytes - lastSample.tx) * 8) / secs / 1e6),
        }
      }
      lastSample = { rx: u.rx_bytes, tx: u.tx_bytes, at }
      usage = { rx: u.rx_bytes, tx: u.tx_bytes }

      const idle = Math.max(u.entry_idle_secs, u.exit_idle_secs)
      if (!nodeWarned && idle > NODE_SILENT_SECS) {
        nodeWarned = true
        silentHop = u.exit_idle_secs >= u.entry_idle_secs ? 'exit' : 'entry'
        if (!alert) alert = 'node'
      }

      // Each hop was paid for a fixed allowance. Near the end, pay for the
      // next one while there are tokens; otherwise the session is over.
      const allowance = Math.min(session?.config?.entry?.bytes_allowed || Infinity, session?.config?.exit?.bytes_allowed || Infinity)
      if (!renewing && Number.isFinite(allowance) && u.rx_bytes + u.tx_bytes >= allowance * 0.95) {
        renewing = true
        await renew()
        renewing = false
      }
    } catch {
      // A missed sample only delays the numbers.
    }
  }

  async function refreshPin() {
    try {
      pinnedRoute = !!(await api.pinnedPair())
    } catch {
      pinnedRoute = false
    }
  }
  $effect(() => {
    if (overlay === null) refreshPin()
  })

  let timers: ReturnType<typeof setTimeout>[] = []
  const later = (fn: () => void, ms: number) => timers.push(setTimeout(fn, ms))
  const clearTimers = () => {
    timers.forEach(clearTimeout)
    timers = []
  }
  const clock = setInterval(() => {
    now = Date.now()
    if (connected) sampleUsage()
  }, 1000)
  // The tray popover can open a drawer here, or connect and disconnect.
  const offOpen = Events.On('arfl:open', (e) => {
    const name = e.data as Overlay
    if (name) overlay = name
  })
  const offState = Events.On('arfl:state', () => {
    session = null
    onChanged()
  })
  onDestroy(() => {
    clearTimers()
    clearInterval(clock)
    clearInterval(hubTimer)
    offOpen()
    offState()
  })

  const connected = $derived(status.state === 'connected')
  const connecting = $derived(stage >= 0)
  const hubName = $derived(hub?.name || status.hub_url)
  const tokens = $derived(tokensFromSats(status.balance_sats))
  const noBalance = $derived(tokens < PER_HOP_TOKENS * 2)
  const balanceGB = $derived(gbFromTokens(tokens))

  // The meter shows what is left against the most this device has held, so it
  // drains as bandwidth is used instead of sitting near empty.
  const HIGH_KEY = 'arfl.tokensHigh'
  let high = $state(Number(localStorage.getItem(HIGH_KEY) || 0))
  $effect(() => {
    if (tokens > high) {
      high = tokens
      localStorage.setItem(HIGH_KEY, String(tokens))
    }
  })
  const filled = $derived(high > 0 ? Math.min(SEGMENTS, Math.round((tokens / high) * SEGMENTS)) : 0)

  $effect(() => {
    if (connected && !session) api.session().then((s) => (session = s))
  })

  function operator(nodeId: string | undefined) {
    return nodeId ? 'Operator ' + nodeId.slice(0, 4) : 'Not set'
  }
  const entryName = $derived(connected ? operator(session?.config?.entry?.node_id) : 'Picked when you connect')
  const exitName = $derived(connected ? operator(session?.config?.exit?.node_id) : '')

  function clockText(ms: number) {
    const s = Math.max(0, Math.floor(ms / 1000))
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${pad(Math.floor(s / 3600))}:${pad(Math.floor((s % 3600) / 60))}:${pad(s % 60)}`
  }
  const elapsed = $derived(session?.started_at ? now - new Date(session.started_at).getTime() : 0)

  async function connect() {
    clearTimers()
    error = ''
    stage = 0
    // The bridge connects in one call, so the stages advance on a timer and
    // hold on the last one until the tunnel is actually up.
    for (let i = 1; i < STAGES.length; i++) later(() => (stage = i), i * STAGE_MS)
    try {
      session = await api.connect(PER_HOP_TOKENS * TOKEN_SATS)
      lastSample = { rx: 0, tx: 0, at: 0 }
      usage = { rx: 0, tx: 0 }
      rate = { down: 0, up: 0 }
      ipv6Off = false
      await checkIPv6()
      if (ipv6Exposed && prefs.ipv6Auto) await fixIPv6()
      clearTimers()
      stage = -1
      cue('up')
      wake = true
      later(() => (wake = false), 650)
    } catch (err) {
      clearTimers()
      stage = -1
      error = (err as Error).message
    } finally {
      onChanged()
    }
  }

  async function renew() {
    const enough = tokensFromSats(status.balance_sats) >= PER_HOP_TOKENS * 2
    await disconnect(true)
    if (enough) {
      await connect()
    } else {
      alert = 'out'
    }
  }

  async function disconnect(quiet = false) {
    clearTimers()
    error = ''
    if (!quiet) cue('down')
    closing = true
    const ended = session
    nodeWarned = false
    try {
      await api.disconnect()
    } catch (err) {
      error = (err as Error).message
    }
    if (ended) {
      const usedMB = Math.round((usage.rx + usage.tx) / 1e6)
      const spent = tokensFromSats(ended.spent_sats)
      // Each hop holds one token; both carry the same traffic, so the unused
      // part is what is left of a hop's token.
      const perHop = Math.max(1, spent / 2) * TOKEN_MB
      last = {
        dur: clockText(Date.now() - new Date(ended.started_at).getTime()),
        usedMB,
        tokens: spent,
        forfeitMB: Math.max(0, Math.round(perHop - usedMB)),
        hub: hubName,
        route: operator(ended.config?.entry?.node_id) + ' → ' + operator(ended.config?.exit?.node_id),
      }
    }
    ipv6Off = false
    session = null
    later(() => (closing = false), 950)
    onChanged()
  }

  function mainAction() {
    if (closing) return
    if (connecting) return
    if (connected) return disconnect()
    if (noBalance) return (overlay = 'topup')
    return connect()
  }

  const connLabel = $derived(
    closing ? 'Disconnecting…' : connecting ? 'Connecting…' : connected ? 'Disconnect' : noBalance ? 'Top up to connect' : 'Connect',
  )
  const statusWord = $derived(
    noBalance && !connected
      ? 'No bandwidth'
      : closing
        ? 'Disconnecting'
        : connecting
          ? 'Connecting'
          : connected
            ? ipv6Safe
              ? 'Protected'
              : 'Partly protected'
            : 'Not connected',
  )
  const statusSmall = $derived(closing ? 'Disconnecting' : connecting ? 'Connecting' : connected ? 'Connected' : 'Disconnected')
  const statusColor = $derived(
    connecting ? 'var(--accent)' : connected && !closing ? (ipv6Safe ? 'var(--cyan)' : 'var(--amber)') : 'var(--muted)',
  )
  const statusHint = $derived.by(() => {
    if (error) return error
    if (!status.tunnel_ready) return status.tunnel_error || 'Run ARFL with administrator rights to enable the tunnel.'
    if (closing) return 'Closing the tunnel. Your traffic will not be protected after this.'
    if (connecting) return 'Your device is paying each node and building the two tunnels.'
    if (connected && !ipv6Safe) return 'The tunnel is up, but IPv6 traffic still goes out directly.'
    if (connected) return 'Your traffic is going through two separate nodes.'
    if (noBalance) return `You have no bandwidth at ${hubName}. Tokens only work at the hub that sold them.`
    return 'Your traffic leaves this device directly until you connect.'
  })

  const progress = $derived((stage + 1) / 5)
  const eyesStyle = $derived.by(() => {
    if (closing) return 'filter:var(--eyesOff);opacity:var(--eyesOffO);transition:filter .9s ease-in-out, opacity .9s ease-in-out'
    if (connecting) {
      const p = progress
      const light = isLight()
      const bright = light ? 1 : 0.5 + p * 0.5
      const offO = light ? 0.4 : 0.9
      return `filter:grayscale(${(1 - p).toFixed(2)}) brightness(${bright.toFixed(2)}) drop-shadow(0 0 ${Math.round(p * 30)}px var(--glow));opacity:${(offO + p * (1 - offO)).toFixed(2)};transition:filter .7s ease, opacity .7s ease`
    }
    if (connected) return 'filter:drop-shadow(0 0 30px var(--glow));opacity:1;transition:filter .6s ease, opacity .6s ease'
    return 'filter:var(--eyesOff);opacity:var(--eyesOffO);transition:filter .6s ease, opacity .6s ease'
  })
  const eyesClass = $derived(closing ? 'arfl-close' : wake ? 'arfl-wake' : '')

  const fmtMB = (bytes: number) => `${Math.round(bytes / 1e6).toLocaleString()} MB`
  const fmtRate = (mbps: number) => `${mbps.toFixed(1)} Mbps`
  const sessionLine = $derived(
    connected && session
      ? `${clockText(elapsed)} · ${fmtMB(usage.rx + usage.tx)} used · ${fmtRate(rate.down)} down`
      : last
        ? `Last session · ${last.dur} · ${last.usedMB} MB · ${last.tokens} tokens`
        : '',
  )
  $effect(() => {
    if (tokens > LOW_TOKENS) lowDismissed = false
    else if (tokens > 0 && !lowDismissed && !alert) alert = 'low'
  })
  const lastReached = $derived(Math.max(0, Math.round((now - hubReachedAt) / 60000)))

  const protShort = $derived(!connected ? 'Checked when connected' : ipv6Safe ? 'All checks passed' : '1 issue: IPv6')
  const protColor = $derived(!connected ? 'var(--muted)' : ipv6Safe ? 'var(--cyan)' : 'var(--amber)')

  const titles = $derived<Record<Overlay, string>>({
    topup: `Top up at ${hubName}`,
    settings: 'Settings',
    hubs: 'Choose a hub',
    nodes: 'Nodes',
    protection: 'Protection',
    privacy: 'What can be seen',
    summary: 'Session ended',
  })

  const wordSrc = $derived(prefs.theme && isLight() ? wordLight : wordDark)
</script>

<div class="window">
  <header style="--wails-draggable:drag">
    <div class="lockup">
      <img src={mark} alt="" class="mark" />
      <img src={wordSrc} alt="ARFL" class="word" />
    </div>
    <div class="actions" style="--wails-draggable:no-drag">
      <button class="topup" onclick={() => (overlay = 'topup')}>Top up</button>
      <button class="bare icon" aria-label="Settings" onclick={() => (overlay = 'settings')}>
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z" /></svg>
      </button>
    </div>
  </header>

  <div class="scroll">
    <div class="column">
      <img src={eyes} alt="" class="eyes {eyesClass}" style={eyesStyle} />

      <div class="small" style="color:{statusColor}">
        <span class="dot" style="background:{statusColor}"></span>{statusSmall}
      </div>
      <div class="word-status">{statusWord}</div>
      <div class="hint" class:err={!!error}>{statusHint}</div>

      <div class="under">
        {#if connecting}
          <div class="stages">
            {#each STAGES as label, i}
              <div class="stage">
                <div
                  class="stage-mark"
                  class:done={stage > i}
                  class:now={stage === i}
                  class:arfl-tick={stage === i}
                >
                  {stage > i ? '✓' : i + 1}
                </div>
                <div class="stage-label" class:done={stage > i} class:now={stage === i}>{label}</div>
              </div>
            {/each}
          </div>
        {:else}
          <div class="session-wrap">
            <div class="session mono">{sessionLine}</div>
            {#if !connected && last}
              <button class="bare details" onclick={() => (overlay = 'summary')}>Details</button>
            {/if}
          </div>
        {/if}
      </div>

      <button
        class="conn"
        class:on={connected && !closing}
        class:busy={connecting || closing}
        disabled={!status.tunnel_ready && !connected && !noBalance}
        onclick={mainAction}>{connLabel}</button
      >

      <div class="balance">
        <div class="balance-head">
          <div><span class="mono gb">{balanceGB} GB</span> <span class="muted">left at {hubName}</span></div>
          <div class="muted tiny">{tokens.toLocaleString()} tokens · 100 MB each</div>
        </div>
        <div class="meter">
          {#each Array(SEGMENTS) as _, i}
            <div
              class="seg"
              class:arfl-tick={connected && i === filled - 1}
              style="background:{i < filled ? 'var(--accent)' : 'var(--line)'}"
            ></div>
          {/each}
        </div>
      </div>

      <div class="rows">
        <button class="bare row" onclick={() => (overlay = 'hubs')}>
          <div><div class="row-label">Hub</div><div class="row-value">{hubName}</div></div>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6" /></svg>
        </button>
        <button class="bare row mid" onclick={() => (overlay = 'nodes')}>
          <div>
            <div class="row-label">{pinnedRoute ? 'Route · yours' : 'Route · automatic'}</div>
            <div class="row-value two">{entryName}{#if exitName}<br />{exitName}{/if}</div>
          </div>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6" /></svg>
        </button>
        <button class="bare row last" onclick={() => (overlay = 'protection')}>
          <div>
            <div class="row-label">Protection</div>
            <div class="row-value two" style="color:{protColor}">{protShort}</div>
          </div>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 6l6 6-6 6" /></svg>
        </button>
      </div>
    </div>
  </div>

  {#if overlay}
    <Drawer title={titles[overlay]} width={overlay === 'privacy' ? 520 : 420} onClose={() => (overlay = null)}>
      {#if overlay === 'topup'}
        <TopUp {hubName} {tokens} onPurchased={onChanged} onClose={() => (overlay = null)} />
      {:else if overlay === 'nodes'}
        <Nodes {hubName} {session} {connected} />
      {:else if overlay === 'settings'}
        <SettingsPanel {hubName} {hubMeta} onChangeHub={() => (overlay = 'hubs')} />
      {:else if overlay === 'hubs'}
        <Hubs
          currentUrl={status.hub_url}
          {connected}
          onSwitched={(url) => {
            overlay = null
            onHubSwitched(url)
          }}
        />
      {:else if overlay === 'protection'}
        <Protection
          {connected}
          session={clockText(elapsed)}
          used={fmtMB(usage.rx + usage.tx)}
          down={fmtRate(rate.down)}
          up={fmtRate(rate.up)}
          ipv6Exposed={!ipv6Safe}
          onFixIPv6={fixIPv6}
          onPrivacy={() => (overlay = 'privacy')}
        />
      {:else if overlay === 'privacy'}
        <Privacy {connected} {ipv6Safe} />
      {:else if overlay === 'summary' && last}
        <Summary
          {last}
          onReconnect={() => {
            overlay = null
            connect()
          }}
          onClose={() => (overlay = null)}
        />
      {/if}
    </Drawer>
  {/if}

  {#if alert === 'low'}
    <Alert
      kicker="Low balance"
      tone="amber"
      title={`${balanceGB} GB left at ${hubName}`}
      body={`When your tokens run out the tunnel closes and your traffic goes out unprotected. These tokens only work at ${hubName}.`}
      primary="Top up"
      secondary="Not now"
      onPrimary={() => { alert = null; lowDismissed = true; overlay = 'topup' }}
      onSecondary={() => { alert = null; lowDismissed = true }}
    >
      <div>
        <div class="abar"><div class="afill" style="width:{Math.max(2, (tokens / Math.max(high, 1)) * 100)}%"></div></div>
        <div class="arow mono"><span>{tokens} tokens</span><span>0</span></div>
      </div>
    </Alert>
  {:else if alert === 'out'}
    <Alert
      kicker="Disconnected"
      tone="red"
      title={`Out of bandwidth at ${hubName}`}
      body="Your last token was used up and the tunnel closed. Your traffic is not protected right now."
      primary="Top up"
      secondary="Close"
      onPrimary={() => { alert = null; overlay = 'topup' }}
      onSecondary={() => (alert = null)}
    >
      <div class="agrid">
        <div><div class="ak">Time</div><div class="av mono">{last?.dur ?? '—'}</div></div>
        <div><div class="ak">Used</div><div class="av mono">{last ? `${(last.usedMB / 1000).toFixed(1)} GB` : '—'}</div></div>
        <div><div class="ak">Tokens spent</div><div class="av mono">{last?.tokens ?? '—'}</div></div>
      </div>
    </Alert>
  {:else if alert === 'node'}
    <Alert
      kicker="Node offline"
      tone="amber"
      title={`Your ${silentHop} node stopped responding`}
      body={`${silentHop === 'exit' ? exitName : entryName} has not answered for a few minutes. Reconnect to switch to another node ${hubName} has approved. Traffic may pause for a moment.`}
      primary="Choose a node"
      secondary="Dismiss"
      onPrimary={() => { alert = null; overlay = 'nodes' }}
      onSecondary={() => (alert = null)}
    />
  {:else if alert === 'hub'}
    <Alert
      kicker="Hub unreachable"
      tone="red"
      title={`Can't reach ${hubName}`}
      body="Nodes check every token with the hub, so new connections can't start until it is back. Your balance is safe on this device."
      primary="Retry"
      secondary="Switch hub"
      onPrimary={checkHub}
      onSecondary={() => { alert = null; overlay = 'hubs' }}
    >
      <div class="aline">
        <div><div class="ak">Balance at {hubName}</div><div class="av mono">{balanceGB} GB</div></div>
        <div class="ak">Last reached {lastReached === 0 ? 'just now' : `${lastReached} min ago`}</div>
      </div>
    </Alert>
  {/if}
</div>

<style>
  .window {
    position: relative;
    height: 100%;
    display: flex;
    flex-direction: column;
    background: var(--bg);
    color: var(--text);
    overflow: hidden;
  }

  header {
    height: 56px;
    flex: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    /* Leaves room for the macOS traffic lights in the inset title bar. */
    padding: 0 16px 0 88px;
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
    display: block;
  }

  .word {
    height: 15px;
    width: auto;
    display: block;
  }

  .actions {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .topup {
    min-height: 36px;
    padding: 0 14px;
    border-radius: 8px;
    font-size: 13.5px;
  }

  .icon {
    width: 36px;
    height: 36px;
    border-radius: 8px;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--muted);
  }

  .scroll {
    flex: 1;
    min-height: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    overflow-y: auto;
  }

  .column {
    width: 560px;
    max-width: calc(100% - 32px);
    padding: 20px 0;
    display: flex;
    flex-direction: column;
    align-items: center;
    text-align: center;
  }

  .eyes {
    width: 460px;
    max-width: 100%;
    height: auto;
    display: block;
  }

  .small {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 14px;
    font-size: 13.5px;
    font-weight: 500;
  }

  .dot {
    width: 8px;
    height: 8px;
    border-radius: 4px;
  }

  .word-status {
    font-size: 32px;
    font-weight: 600;
    margin-top: 6px;
    letter-spacing: -0.015em;
  }

  .hint {
    font-size: 14.5px;
    color: var(--muted);
    margin-top: 6px;
    line-height: 1.5;
    max-width: 440px;
  }

  .hint.err {
    color: var(--red);
  }

  .under {
    min-height: 44px;
    margin-top: 8px;
    display: flex;
    align-items: center;
    justify-content: center;
  }

  .stages {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 12px;
    width: 480px;
    max-width: 100%;
  }

  .stage {
    display: flex;
    align-items: center;
    gap: 8px;
    justify-content: center;
  }

  .stage-mark {
    width: 20px;
    height: 20px;
    border-radius: 10px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-family: var(--mono);
    font-size: 11px;
    flex: none;
    border: 1.5px solid var(--line2);
    color: var(--muted);
  }

  .stage-mark.now {
    border-color: var(--accent);
    color: var(--text);
  }

  .stage-mark.done {
    border: 0;
    background: var(--cyanBg);
    color: var(--cyan);
  }

  .stage-label {
    font-size: 12.5px;
    color: var(--muted);
  }

  .stage-label.now {
    color: var(--text);
    font-weight: 500;
  }

  .stage-label.done {
    color: var(--text2);
  }

  .session-wrap {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .session {
    font-size: 12.5px;
    color: var(--muted);
  }

  .abar {
    height: 6px;
    border-radius: 3px;
    background: var(--line);
  }

  .afill {
    height: 6px;
    border-radius: 3px;
    background: var(--amber);
  }

  .arow {
    display: flex;
    justify-content: space-between;
    font-size: 12px;
    color: var(--muted);
    margin-top: 8px;
  }

  .agrid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    padding: 12px 0;
    border-top: 1px solid var(--line);
    border-bottom: 1px solid var(--line);
  }

  .aline {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 0;
    border-top: 1px solid var(--line);
    border-bottom: 1px solid var(--line);
  }

  .ak {
    font-size: 12px;
    color: var(--muted);
  }

  .aline > .ak {
    font-size: 13px;
  }

  .av {
    font-size: 14.5px;
    margin-top: 3px;
  }

  .details {
    font-size: 12.5px;
    font-weight: 500;
    color: var(--accentT);
    text-decoration: underline;
  }

  .conn {
    margin-top: 18px;
    width: 340px;
    max-width: 100%;
    min-height: 56px;
    padding: 0 28px;
    border-radius: 12px;
    font-size: 16px;
    font-weight: 600;
    text-align: center;
  }

  .conn.on {
    background: transparent;
    border: 1.5px solid var(--alt);
    color: var(--alt);
  }

  .conn.busy {
    background: transparent;
    border: 1.5px solid var(--line2);
    color: var(--text2);
  }

  .balance {
    width: 100%;
    margin-top: 26px;
    text-align: left;
  }

  .balance-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    margin-bottom: 8px;
    font-size: 13px;
  }

  .gb {
    font-size: 14px;
  }

  .muted {
    color: var(--muted);
  }

  .tiny {
    font-size: 12px;
  }

  .meter {
    display: grid;
    grid-template-columns: repeat(40, minmax(0, 1fr));
    gap: 3px;
  }

  .seg {
    height: 18px;
    border-radius: 2px;
  }

  .rows {
    width: 100%;
    margin-top: 22px;
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    border-top: 1px solid var(--line);
    border-bottom: 1px solid var(--line);
    text-align: left;
  }

  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    min-height: 72px;
    padding: 0 14px 0 4px;
    color: var(--text);
  }

  .row > div {
    min-width: 0;
  }

  .row svg {
    color: var(--muted);
    flex: none;
  }

  .row.mid {
    padding: 0 14px;
    border-left: 1px solid var(--line);
  }

  .row.last {
    padding: 0 4px 0 14px;
    border-left: 1px solid var(--line);
  }

  .row-label {
    font-size: 12px;
    color: var(--muted);
  }

  .row-value {
    font-size: 14px;
    font-weight: 500;
    margin-top: 3px;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .row-value.two {
    font-size: 13.5px;
    line-height: 1.35;
  }
</style>
