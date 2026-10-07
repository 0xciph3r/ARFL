<script lang="ts">
  import { onDestroy } from 'svelte'
  import { api, type HubPreview, type RestoredHub, type SetupView } from '../lib/api'
  import { isLight, prefs } from '../lib/prefs.svelte'
  import { cue } from '../lib/sound'
  import { MIN_BACKUP_PASSPHRASE, markBackedUp } from '../lib/backup'
  import { gbFromTokens, tokensFromSats } from '../lib/units'
  import mark from '../assets/mark.svg'
  import wordDark from '../assets/wordmark-dark.png'
  import wordLight from '../assets/wordmark-light.png'
  import eyes from '../assets/eyes-hero.png'

  let { setup, startStep = 0, onDone }: {
    setup: SetupView
    startStep?: number
    onDone: (buy: boolean) => void
  } = $props()

  const NEW_STEPS = ['Welcome', 'Your key', 'Hub', 'Backup', 'Ready']
  const RESTORE_STEPS = ['Restore', 'Passphrase', 'Checking', 'Done']
  const CHECK_MS = 900

  let flow = $state<'new' | 'restore'>('new')
  // svelte-ignore state_referenced_locally
  let step = $state(startStep)
  let rs = $state(0)
  let busy = $state(false)
  let error = $state('')

  // Key
  // svelte-ignore state_referenced_locally
  let fp = $state(setup.fingerprint ?? '')
  let legacyPass = $state('')

  // Hub
  let hubs = $state<HubPreview[]>([])
  let hubLoading = $state(true)
  let hubPick = $state(0)
  let hubInput = $state('')
  let hubName = $state('')

  // Backup
  let exporting = $state(false)
  let exportPass = $state('')
  let exported = $state(false)
  let backedUp = $state(false)

  // Restore
  let file = $state('')
  let rpass = $state('')
  let chk = $state(0)
  let restored = $state<RestoredHub[]>([])

  let timers: ReturnType<typeof setTimeout>[] = []
  onDestroy(() => timers.forEach(clearTimeout))

  const wordSrc = $derived(prefs.theme && isLight() ? wordLight : wordDark)
  const names = $derived(flow === 'restore' ? RESTORE_STEPS : NEW_STEPS)
  const cur = $derived(flow === 'restore' ? rs : step)

  async function loadHubs() {
    hubLoading = true
    const urls = await api.recommendedHubs()
    const found: HubPreview[] = []
    for (const url of urls) {
      try {
        found.push(await api.previewHub(url))
      } catch {
        found.push({ url, name: url.replace(/^https?:\/\//, ''), margin_pct: 0, node_count: 0, trusted: false })
      }
    }
    hubs = [...found, ...hubs.filter((h) => !urls.includes(h.url))]
    hubLoading = false
  }

  $effect(() => {
    if (step === 2 && hubs.length === 0) loadHubs()
  })

  async function run(fn: () => Promise<void>) {
    busy = true
    error = ''
    try {
      await fn()
    } catch (err) {
      error = (err as Error).message
    } finally {
      busy = false
    }
  }

  const generate = () =>
    run(async () => {
      fp = await api.createKey()
    })

  const upgrade = () =>
    run(async () => {
      await api.upgradeLegacy(legacyPass)
      legacyPass = ''
      fp = await api.fingerprint()
    })

  const addHub = () =>
    run(async () => {
      const raw = hubInput.trim()
      if (!raw) return
      if (raw.startsWith('npub')) throw new Error('Finding a hub by its npub is not supported yet. Paste the hub URL.')
      const preview = await api.previewHub(raw.includes('://') ? raw : 'https://' + raw)
      hubs = [...hubs.filter((h) => h.url !== preview.url), { ...preview, custom: true } as HubPreview]
      hubPick = hubs.length - 1
      hubInput = ''
    })

  const chooseHub = () =>
    run(async () => {
      const h = hubs[hubPick]
      const status = await api.connectHub(h.url)
      hubName = status.name || h.name
      step = 3
    })

  const saveBackup = () =>
    run(async () => {
      const path = await api.exportBackup(exportPass)
      if (!path) return
      cue('save')
      markBackedUp()
      exported = true
      exporting = false
      exportPass = ''
    })

  const pickFile = () =>
    run(async () => {
      const path = await api.chooseBackupFile()
      if (path) file = path
    })

  async function unlockBackup() {
    rs = 2
    chk = 0
    error = ''
    for (let i = 1; i < 3; i++) timers.push(setTimeout(() => (chk = Math.max(chk, i)), i * CHECK_MS))
    try {
      restored = await api.restoreBackup(file, rpass)
      fp = await api.fingerprint()
      chk = 3
      timers.push(setTimeout(() => (rs = 3), 500))
    } catch (err) {
      timers.forEach(clearTimeout)
      error = (err as Error).message
      rs = 1
    }
  }

  // The address is always shown: hubs choose their own names, and two can share one.
  // Restoring brings back tokens but no hub choice. Open the hub that holds
  // the most, or ask for one when the backup held none.
  const openRestored = () =>
    run(async () => {
      const best = [...restored].sort((a, b) => b.sats - a.sats).find((r) => r.sats > 0 && !r.error)
      if (!best) {
        flow = 'new'
        step = 2
        return
      }
      await api.connectHub(best.hub_url)
      onDone(false)
    })

  const hubMeta = (h: HubPreview & { custom?: boolean }) =>
    `${h.trusted ? 'Trusted by ARFL' : 'Not verified'} · ${h.url.replace(/^https?:\/\//, '')} · Margin ${h.margin_pct}% · ${h.node_count} approved nodes`

  const fileName = $derived(file ? file.split(/[\\/]/).pop() : 'Choose file')
  const chkLabels = ['Decrypting the file', 'Restoring your key', 'Asking each hub which tokens are unspent']
</script>

<div class="window">
  <aside style="--wails-draggable:drag">
    <div class="lockup">
      <img src={mark} alt="" class="mark" /><img src={wordSrc} alt="ARFL" class="word" />
    </div>
    <ol class="rail">
      {#each names as name, i}
        <li class="rail-row">
          <span class="dot" class:active={cur === i} class:done={cur > i}>{cur > i ? '✓' : i + 1}</span>
          <span class="rail-label" class:active={cur === i}>{name}</span>
        </li>
      {/each}
    </ol>
    <div class="grow"></div>
    <div class="brand mono">NO LOGS.<br />NO ACCOUNTS.<br />NO FEE, EVER.</div>
  </aside>

  <section>
    {#if flow === 'new' && step === 0}
      <img src={eyes} alt="" class="eyes" />
      <div class="kicker mono">PRIVACY IN THE DARK CLOUD</div>
      <h1 class="hero">Privacy without an account.</h1>
      <p class="lede">Your device makes a key. That key is your only identity. You pay in bitcoin over Lightning, and your traffic goes through two separate nodes.</p>
      <div class="points">
        <div class="point"><span class="num mono">01</span><span>The entry node sees your IP but not where you go.</span></div>
        <div class="point"><span class="num mono">02</span><span>The exit node sees where you go but not who you are.</span></div>
        <div class="point"><span class="num mono">03</span><span>The hub cannot link what you paid to what you use.</span></div>
      </div>
      <div class="grow"></div>
      <div class="actions">
        <button class="primary" onclick={() => (step = 1)}>Get started</button>
        <button class="bare underline" onclick={() => { flow = 'restore'; rs = 0; error = '' }}>I already have a backup</button>
      </div>
    {:else if flow === 'new' && step === 1}
      <h1>Make your key</h1>
      <p class="lede">It is created on this device and never sent to ARFL, the hub or any node.</p>
      {#if setup.legacy_vault && !fp}
        <div class="card">
          <label for="legacy" class="field-label">This device already has a wallet. Enter its passphrase once to move it onto your new key.</label>
          <input id="legacy" type="password" placeholder="Your current wallet passphrase" bind:value={legacyPass} />
        </div>
      {:else}
        <div class="card">
          <div class="small">Your public key fingerprint</div>
          <div class="fp mono" class:empty={!fp}>{fp || '···· · ···· · ···· · ····'}</div>
        </div>
      {/if}
      {#if error}<div class="err" role="alert">{error}</div>{/if}
      <div class="grow"></div>
      <div class="actions">
        {#if fp}
          <button class="primary" onclick={() => (step = 2)}>Continue</button>
        {:else if setup.legacy_vault}
          <button class="primary" disabled={busy || legacyPass.length === 0} onclick={upgrade}>{busy ? 'Moving…' : 'Unlock and continue'}</button>
        {:else}
          <button class="primary" disabled={busy} onclick={generate}>{busy ? 'Generating…' : 'Generate key'}</button>
        {/if}
        <button class="ghost" onclick={() => (step = 0)}>Back</button>
      </div>
    {:else if flow === 'new' && step === 2}
      <h1>Choose a hub</h1>
      <p class="lede wide">Hubs are run by independent operators. Each sets its own prices and approves its own nodes. Tokens only work at the hub that sold them, so switching later means buying again.</p>
      <div class="hub-list">
        {#if hubLoading}
          <div class="small">Looking up hubs…</div>
        {/if}
        {#each hubs as h, i}
          <button class="bare hub" class:on={hubPick === i} onclick={() => (hubPick = i)}>
            <div>
              <div class="hub-name">{h.name}</div>
              <div class="small">{hubMeta(h)}</div>
            </div>
            <span class="radio" class:on={hubPick === i}></span>
          </button>
        {/each}
        <div class="add">
          <label for="hubkey" class="field-label">Add a hub</label>
          <div class="add-row">
            <input id="hubkey" type="text" class="mono" placeholder="Hub URL or npub1…" bind:value={hubInput} />
            <button class="secondary" disabled={busy} onclick={addHub}>Add</button>
          </div>
        </div>
      </div>
      {#if error}<div class="err" role="alert">{error}</div>{/if}
      <div class="grow"></div>
      <div class="actions">
        <button class="primary" disabled={busy || hubs.length === 0} onclick={chooseHub}>{busy ? 'Connecting…' : 'Continue'}</button>
        <button class="ghost" onclick={() => (step = 1)}>Back</button>
      </div>
    {:else if flow === 'new' && step === 3}
      <h1>Back up your key</h1>
      <p class="lede wide">There is no account recovery. If you lose this device without a backup, you lose your key and any bandwidth you bought.</p>
      <div class="warn">
        <div class="warn-title">Keep it offline</div>
        <div class="warn-text">Anyone holding the backup can use your tokens. Store it somewhere only you can open.</div>
        {#if exporting}
          <label for="exportpass" class="field-label warn-label">Passphrase for the backup file</label>
          <input id="exportpass" type="password" placeholder="At least {MIN_BACKUP_PASSPHRASE} characters" bind:value={exportPass} />
          <div class="warn-text">The file is encrypted with this passphrase, and nobody can recover it for you.</div>
          <div class="actions tight">
            <button class="amber" disabled={busy || [...exportPass].length < MIN_BACKUP_PASSPHRASE} onclick={saveBackup}>Save backup file</button>
            <button class="ghost" onclick={() => { exporting = false; exportPass = '' }}>Cancel</button>
          </div>
        {:else}
          <button class="amber" onclick={() => (exporting = true)}>{exported ? 'Backup exported' : 'Export backup'}</button>
        {/if}
      </div>
      <label class="check">
        <input type="checkbox" bind:checked={backedUp} />
        <span>I saved my backup somewhere safe</span>
      </label>
      {#if error}<div class="err" role="alert">{error}</div>{/if}
      <div class="grow"></div>
      <div class="actions">
        <button class="primary" disabled={!backedUp} onclick={() => (step = 4)}>Continue</button>
        <button class="ghost" onclick={() => (step = 2)}>Back</button>
      </div>
    {:else if flow === 'new' && step === 4}
      <h1 class="set">You are set.</h1>
      <p class="lede big">You have a key and a hub. Next you will see the nodes this hub has approved, buy bandwidth from it, and connect.</p>
      <div class="card summary">
        <div class="sum-row"><span class="muted">Key</span><span class="mono">{fp}</span></div>
        <div class="sum-row"><span class="muted">Hub</span><span>{hubName}</span></div>
        <div class="sum-row"><span class="muted">Balance</span><span class="mono amber-text">0.0 GB</span></div>
      </div>
      <div class="grow"></div>
      <div class="actions">
        <button class="primary" onclick={() => onDone(true)}>Buy bandwidth</button>
      </div>
    {:else if flow === 'restore' && rs === 0}
      <h1>Restore from a backup</h1>
      <p class="lede">Bring your key and your tokens to this device.</p>
      <div class="rows">
        <button class="bare file-row" onclick={pickFile}>
          <div><div class="row-title">Backup file</div><div class="small">Restores your key and every token</div></div>
          <div class="mono file" class:chosen={!!file}>{fileName}</div>
        </button>
        <div class="file-row muted-row">
          <div><div class="row-title muted">Key QR only</div><div class="small">Moves your key but not your tokens. Use the backup file for those.</div></div>
        </div>
      </div>
      {#if error}<div class="err" role="alert">{error}</div>{/if}
      <div class="grow"></div>
      <div class="actions">
        <button class="primary" class:off={!file} disabled={!file} onclick={() => (rs = 1)}>Continue</button>
        <button class="ghost" onclick={() => { flow = 'new'; step = 0; file = ''; error = '' }}>Back</button>
      </div>
    {:else if flow === 'restore' && rs === 1}
      <h1>Unlock the backup</h1>
      <p class="lede">Enter the passphrase you chose when you saved the file. It never leaves this device.</p>
      <div class="pass">
        <label for="rpass" class="field-label">Passphrase</label>
        <input id="rpass" type="password" placeholder="Your backup passphrase" bind:value={rpass} />
        <div class="small">If you forgot it, the file cannot be opened. Nobody can reset it for you.</div>
      </div>
      {#if error}<div class="err" role="alert">{error}</div>{/if}
      <div class="grow"></div>
      <div class="actions">
        <button class="primary" class:off={rpass.length < 4} disabled={rpass.length < 4} onclick={unlockBackup}>Unlock</button>
        <button class="ghost" onclick={() => (rs = 0)}>Back</button>
      </div>
    {:else if flow === 'restore' && rs === 2}
      <h1>Checking your tokens</h1>
      <p class="lede">Each hub is asked which of your tokens are still unspent.</p>
      <div class="rows">
        {#each chkLabels as label, i}
          <div class="chk">
            <span class="cmark" class:done={chk > i} class:now={chk === i} class:arfl-tick={chk === i}>{chk > i ? '✓' : i + 1}</span>
            <span class="clabel" class:done={chk > i} class:now={chk === i}>{label}</span>
          </div>
        {/each}
      </div>
    {:else if flow === 'restore' && rs === 3}
      <h1>Your key is back.</h1>
      <div class="rows top24">
        <div class="kv"><span class="muted">Key</span><span class="mono">{fp}</span></div>
        {#each restored as r}
          <div class="kv">
            <span>{r.name}</span>
            <span class="mono" class:muted={tokensFromSats(r.sats) === 0}>{gbFromTokens(tokensFromSats(r.sats))} GB · {tokensFromSats(r.sats)} tokens</span>
          </div>
        {/each}
      </div>
      <div class="warn top22">
        <div class="warn-title">Stop using your old device</div>
        <div class="warn-text">A token can only be spent once. If both devices have it, whichever spends it first wins and the other gets nothing.</div>
      </div>
      {#if error}<div class="err" role="alert">{error}</div>{/if}
      <div class="grow"></div>
      <div class="actions">
        <button class="primary" disabled={busy} onclick={openRestored}>{busy ? 'Opening…' : 'Open ARFL'}</button>
      </div>
    {/if}
  </section>
</div>

<style>
  .window {
    height: 100%;
    display: flex;
    background: var(--bg);
    color: var(--text);
    overflow: hidden;
  }

  aside {
    width: 300px;
    flex: none;
    background: var(--panel);
    border-right: 1px solid var(--line);
    display: flex;
    flex-direction: column;
    /* The top band leaves room for the macOS traffic lights. */
    padding: 58px 24px 28px;
  }

  .lockup {
    display: flex;
    align-items: center;
    gap: 10px;
    padding-bottom: 36px;
  }

  .mark {
    height: 22px;
    width: 22px;
  }

  .word {
    height: 15px;
    width: auto;
  }

  .rail {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .rail-row {
    display: flex;
    align-items: center;
    gap: 12px;
    min-height: 44px;
  }

  .dot {
    width: 28px;
    height: 28px;
    border-radius: 14px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-family: var(--mono);
    font-size: 12px;
    flex: none;
    border: 1.5px solid var(--line2);
    color: var(--muted);
  }

  .dot.active {
    border: 0;
    background: var(--btn);
    color: var(--onBtn);
    font-weight: 600;
  }

  .dot.done {
    border: 0;
    background: var(--cyanBg);
    color: var(--cyan);
  }

  .rail-label {
    font-size: 14.5px;
    font-weight: 500;
    color: var(--muted);
  }

  .rail-label.active {
    font-weight: 600;
    color: var(--text);
  }

  .grow {
    flex: 1;
  }

  .brand {
    font-size: 11px;
    letter-spacing: 0.14em;
    color: var(--brandMuted);
    line-height: 1.7;
  }

  section {
    flex: 1;
    min-width: 0;
    padding: 44px 72px 40px;
    display: flex;
    flex-direction: column;
    overflow-y: auto;
  }

  .eyes {
    width: 400px;
    max-width: 100%;
    height: auto;
    display: block;
    margin-left: -8px;
  }

  .kicker {
    font-size: 12px;
    letter-spacing: 0.22em;
    color: var(--cyan);
    margin-top: 18px;
  }

  h1 {
    font-weight: 700;
    font-size: 36px;
    line-height: 1.1;
    margin: 0;
  }

  h1.hero {
    font-weight: 600;
    font-size: 34px;
    max-width: 520px;
    margin-top: 10px;
  }

  h1.set {
    font-weight: 600;
    font-size: 40px;
    line-height: 1.05;
  }

  .lede {
    font-size: 15px;
    color: var(--text2);
    margin: 14px 0 0;
    line-height: 1.55;
    max-width: 500px;
  }

  .lede.wide {
    max-width: 520px;
  }

  .lede.big {
    font-size: 16px;
    margin-top: 16px;
  }

  h1.hero + .lede {
    margin-top: 12px;
    line-height: 1.5;
  }

  .points {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-top: 20px;
    max-width: 500px;
  }

  .point {
    display: flex;
    gap: 14px;
    align-items: baseline;
    font-size: 15px;
    line-height: 1.5;
  }

  .num {
    color: var(--accentT);
    font-size: 13px;
    width: 20px;
    flex: none;
  }

  .actions {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .actions:has(.underline) {
    gap: 20px;
  }

  .actions.tight {
    margin-top: 12px;
  }

  .primary {
    min-height: 48px;
    padding: 0 28px;
    border-radius: 10px;
    font-size: 15px;
  }

  .primary.off,
  .primary:disabled {
    opacity: 1;
    background: transparent;
    border: 1px solid var(--line2);
    color: var(--muted);
  }

  .ghost {
    min-height: 48px;
    padding: 0 20px;
    border-radius: 10px;
    background: transparent;
    color: var(--muted);
    font-size: 15px;
    font-weight: 500;
  }

  .underline {
    font-size: 14px;
    font-weight: 500;
    color: var(--text2);
    text-decoration: underline;
  }

  .card {
    margin-top: 32px;
    background: var(--surf);
    border: 1px solid var(--line);
    border-radius: 14px;
    padding: 24px;
    max-width: 520px;
  }

  .card input {
    margin-top: 10px;
    background: var(--panel);
  }

  .small {
    font-size: 12.5px;
    color: var(--muted);
    margin-top: 3px;
    line-height: 1.5;
  }

  .card > .small:first-child {
    font-size: 12px;
    margin-top: 0;
  }

  .fp {
    font-size: 22px;
    margin-top: 10px;
    letter-spacing: 0.04em;
  }

  .fp.empty {
    color: var(--line2);
  }

  .err {
    margin-top: 14px;
    font-size: 13.5px;
    color: var(--red);
    max-width: 520px;
    line-height: 1.5;
  }

  .hub-list {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-top: 26px;
    max-width: 540px;
  }

  .hub {
    display: flex;
    align-items: center;
    justify-content: space-between;
    min-height: 68px;
    padding: 0 18px;
    border-radius: 12px;
    width: 100%;
    background: var(--surf);
    border: 2px solid var(--line);
    color: var(--text);
  }

  .hub.on {
    background: var(--surf2);
    border-color: var(--accent);
  }

  .hub-name {
    font-size: 16px;
    font-weight: 600;
  }

  .radio {
    width: 18px;
    height: 18px;
    border-radius: 9px;
    border: 2px solid var(--line2);
    flex: none;
  }

  .radio.on {
    border-color: var(--accent);
    background: var(--accent);
  }

  .add {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-top: 10px;
  }

  .field-label {
    font-size: 13.5px;
    font-weight: 500;
    line-height: 1.5;
  }

  .add-row {
    display: flex;
    gap: 8px;
  }

  .add-row input {
    flex: 1;
    min-width: 0;
    background: var(--surf);
    font-size: 13px;
  }

  .add-row .secondary {
    flex: none;
    min-height: 44px;
    padding: 0 18px;
    border-radius: 8px;
    font-size: 14px;
  }

  .warn {
    margin-top: 28px;
    background: var(--warnBg);
    border: 1px solid var(--warnLine);
    border-radius: 14px;
    padding: 18px 20px;
    max-width: 520px;
  }

  .warn.top22 {
    margin-top: 22px;
    padding: 16px 18px;
    max-width: 540px;
  }

  .warn-title {
    font-size: 14px;
    font-weight: 600;
    color: var(--amber);
  }

  .warn-text {
    font-size: 13.5px;
    color: var(--warnText);
    margin-top: 4px;
    line-height: 1.5;
  }

  .warn-label {
    display: block;
    margin-top: 14px;
    color: var(--warnText);
  }

  .warn input {
    margin-top: 8px;
  }

  .amber {
    margin-top: 14px;
    min-height: 44px;
    padding: 0 20px;
    border-radius: 10px;
    background: var(--amberBtn);
    color: #1d1200;
    font-size: 14px;
  }

  .actions.tight .amber {
    margin-top: 0;
  }

  .check {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-top: 22px;
    font-size: 14.5px;
  }

  .check input {
    width: 20px;
    height: 20px;
    min-height: 0;
    accent-color: var(--accentT);
  }

  .summary {
    margin-top: 28px;
    padding: 18px 20px;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .sum-row {
    display: flex;
    justify-content: space-between;
    font-size: 14px;
  }

  .muted {
    color: var(--muted);
  }

  .amber-text {
    color: var(--amber);
  }

  .rows {
    margin-top: 28px;
    max-width: 540px;
    border-top: 1px solid var(--line);
  }

  .rows.top24 {
    margin-top: 24px;
  }

  .file-row {
    width: 100%;
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    min-height: 72px;
    padding: 0 4px;
    border-bottom: 1px solid var(--line);
    color: var(--text);
  }

  .row-title {
    font-size: 15px;
    font-weight: 600;
  }

  .file {
    font-size: 12.5px;
    color: var(--muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 50%;
  }

  .file.chosen {
    color: var(--cyan);
  }

  .pass {
    margin-top: 28px;
    max-width: 520px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .pass input {
    min-height: 48px;
    background: var(--surf);
    font-size: 15px;
  }

  .chk {
    display: flex;
    align-items: center;
    gap: 14px;
    min-height: 60px;
    border-bottom: 1px solid var(--line);
  }

  .cmark {
    width: 24px;
    height: 24px;
    border-radius: 12px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-family: var(--mono);
    font-size: 12px;
    flex: none;
    border: 1.5px solid var(--line2);
    color: var(--muted);
  }

  .cmark.now {
    border-color: var(--accent);
    color: var(--text);
  }

  .cmark.done {
    border: 0;
    background: var(--cyanBg);
    color: var(--cyan);
  }

  .clabel {
    font-size: 14.5px;
    color: var(--muted);
  }

  .clabel.now {
    color: var(--text);
    font-weight: 500;
  }

  .clabel.done {
    color: var(--text2);
  }

  .kv {
    display: flex;
    justify-content: space-between;
    align-items: center;
    min-height: 52px;
    border-bottom: 1px solid var(--line);
    font-size: 14px;
  }
</style>
