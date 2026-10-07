<script lang="ts">
  import QRCode from 'qrcode'
  import { api } from '../lib/api'
  import { MIN_BACKUP_PASSPHRASE, backupAge, lastBackup, markBackedUp } from '../lib/backup'
  import { applyPrefs, prefs, type ThemeChoice } from '../lib/prefs.svelte'
  import { cue } from '../lib/sound'
  import { gbFromTokens, tokensFromSats } from '../lib/units'

  let { hubName, hubMeta, onChangeHub }: {
    hubName: string
    hubMeta: string
    onChangeHub: () => void
  } = $props()

  const themes: [ThemeChoice, string][] = [
    ['dark', 'Dark'],
    ['light', 'Light'],
    ['system', 'System'],
  ]

  let fp = $state('')
  let heldGB = $state('0.0')
  let backedAt = $state(lastBackup())
  let bk = $state<0 | 1 | 2>(0)
  let passphrase = $state('')
  let savedPath = $state('')
  let keyQr = $state('')
  let atLogin = $state(false)
  let error = $state('')
  let busy = $state(false)

  async function load() {
    try {
      fp = await api.fingerprint()
      heldGB = gbFromTokens(tokensFromSats(await api.heldSats()))
      atLogin = await api.openAtLogin()
    } catch (err) {
      error = (err as Error).message
    }
  }
  $effect(() => {
    load()
  })

  function setTheme(t: ThemeChoice) {
    prefs.theme = t
    applyPrefs()
  }

  type Key = 'sounds' | 'reduceMotion' | 'ipv6Auto' | 'atLogin'
  const isOn = (key: Key) => (key === 'atLogin' ? atLogin : prefs[key])

  async function toggle(key: Key) {
    error = ''
    if (key === 'atLogin') {
      try {
        await api.setOpenAtLogin(!atLogin)
        atLogin = !atLogin
      } catch (err) {
        error = (err as Error).message
      }
      return
    }
    prefs[key] = !prefs[key]
    applyPrefs()
    if (key === 'sounds' && prefs.sounds) cue('pluck', true)
  }

  const toggles: { key: Key; label: string; sub: string }[] = [
    { key: 'sounds', label: 'Sounds', sub: 'Short tones when you connect, disconnect and pay.' },
    { key: 'ipv6Auto', label: 'Turn off IPv6 when connecting', sub: 'Stops IPv6 traffic leaking around the tunnel.' },
    { key: 'reduceMotion', label: 'Reduce motion', sub: 'Stops the pulses, glow and eye animations.' },
    { key: 'atLogin', label: 'Open at login', sub: 'Start ARFL when you sign in to this computer.' },
  ]

  async function saveBackup() {
    busy = true
    error = ''
    try {
      const path = await api.exportBackup(passphrase)
      if (!path) return
      cue('save')
      markBackedUp()
      backedAt = lastBackup()
      savedPath = path.split(/[\\/]/).pop() ?? path
      passphrase = ''
      bk = 2
    } catch (err) {
      error = (err as Error).message
    } finally {
      busy = false
    }
  }

  async function toggleKeyQr() {
    if (keyQr) {
      keyQr = ''
      return
    }
    error = ''
    try {
      keyQr = await QRCode.toDataURL(await api.keyTransfer(), {
        margin: 0,
        width: 320,
        color: { dark: '#0d0c10', light: '#00000000' },
      })
      bk = 0
    } catch (err) {
      error = (err as Error).message
    }
  }
</script>

<div class="settings">
  <div>
    <div class="label">Your key</div>
    <div class="fp mono">{fp}</div>
    <div class="status">
      <div class="status-title" style="color:{backedAt ? 'var(--cyan)' : 'var(--amber)'}">
        {backedAt ? `Backed up ${backupAge(backedAt)}` : 'Not backed up'}
      </div>
      <div class="status-sub">
        {backedAt ? 'Your key and tokens are in the file you saved.' : `${heldGB} GB across your hubs would be lost along with this device.`}
      </div>
    </div>

    {#if bk === 0}
      <div class="stack">
        <button class="primary" onclick={() => { bk = 1; keyQr = '' }}>Back up now</button>
        <button class="secondary" onclick={toggleKeyQr}>{keyQr ? 'Hide key QR' : 'Move to a new device'}</button>
      </div>
    {:else if bk === 1}
      <div class="stack">
        <label for="pass" class="field">Passphrase for the backup file</label>
        <input id="pass" type="password" placeholder="At least {MIN_BACKUP_PASSPHRASE} characters" bind:value={passphrase} />
        <div class="status-sub">The file holds your key and every token. It is encrypted with this passphrase, and nobody can recover the passphrase for you.</div>
        <button class="primary" class:off={[...passphrase].length < MIN_BACKUP_PASSPHRASE} disabled={[...passphrase].length < MIN_BACKUP_PASSPHRASE || busy} onclick={saveBackup}>Save backup file</button>
        <button class="bare cancel" onclick={() => { bk = 0; passphrase = '' }}>Cancel</button>
      </div>
    {:else}
      <div class="stack">
        <div class="mono saved">{savedPath}</div>
        <div class="status-sub">Keep it offline. Anyone with this file and your passphrase can spend your tokens.</div>
        <button class="secondary" onclick={() => (bk = 0)}>Done</button>
      </div>
    {/if}

    {#if keyQr}
      <div class="qr-block">
        <div class="qr"><img src={keyQr} alt="QR code of your device key" /></div>
        <div class="status-sub center">Scan this on your new device to move your key. Tokens do not travel in the QR. Move them with the backup file.</div>
      </div>
    {/if}
    {#if error}<div class="err" role="alert">{error}</div>{/if}
  </div>

  <div>
    <div class="label">Appearance</div>
    <div class="segmented">
      {#each themes as [id, text]}
        <button class="bare seg" class:on={prefs.theme === id} onclick={() => setTheme(id)}>{text}</button>
      {/each}
    </div>
  </div>

  <div>
    <div class="label tight">Preferences</div>
    <div class="list">
      {#each toggles as t}
        <div class="pref">
          <div>
            <div class="pref-label">{t.label}</div>
            <div class="pref-sub">{t.sub}</div>
          </div>
          <button
            class="bare track"
            class:on={isOn(t.key)}
            role="switch"
            aria-checked={isOn(t.key)}
            aria-label={t.label}
            onclick={() => toggle(t.key)}><span class="knob"></span></button
          >
        </div>
      {/each}
    </div>
  </div>

  <div>
    <div class="label tight">Active hub</div>
    <div class="hub-row">
      <div class="hub">{hubName}</div>
      <button class="bare link" onclick={onChangeHub}>Change hub</button>
    </div>
    <div class="pref-sub">{hubMeta}Tokens only work at this hub. It checks every token for double spends.</div>
  </div>

  <div class="foot">
    <div class="brand mono">NO ACCOUNTS. NO NATIVE TOKEN.</div>
    <div class="pref-sub">ARFL desktop · open source</div>
  </div>
</div>

<style>
  .settings {
    display: flex;
    flex-direction: column;
    gap: 28px;
  }

  .label {
    font-size: 13px;
    color: var(--muted);
    margin-bottom: 8px;
  }

  .label.tight {
    margin-bottom: 4px;
  }

  .fp {
    font-size: 16px;
    margin-top: -4px;
  }

  .status {
    margin-top: 16px;
    padding: 14px 0;
    border-top: 1px solid var(--line);
    border-bottom: 1px solid var(--line);
  }

  .status-title {
    font-size: 14px;
    font-weight: 600;
  }

  .status-sub {
    font-size: 12.5px;
    color: var(--muted);
    margin-top: 4px;
    line-height: 1.5;
  }

  .status .status-sub {
    font-size: 13px;
    color: var(--text2);
  }

  .center {
    text-align: center;
  }

  .stack {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-top: 14px;
  }

  .stack .status-sub {
    margin-top: 0;
  }

  .primary,
  .secondary {
    min-height: 46px;
    border-radius: 10px;
    font-size: 14.5px;
    text-align: center;
  }

  .primary.off,
  .primary:disabled {
    opacity: 1;
    background: transparent;
    border: 1px solid var(--line2);
    color: var(--muted);
  }

  .field {
    font-size: 13.5px;
    font-weight: 500;
  }

  .cancel {
    min-height: 40px;
    font-size: 13.5px;
    color: var(--muted);
    text-align: center;
  }

  .saved {
    font-size: 12.5px;
    color: var(--text2);
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .qr-block {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    margin-top: 16px;
  }

  .qr {
    width: 180px;
    background: var(--qrbg);
    padding: 10px;
    border-radius: 12px;
  }

  .qr img {
    width: 100%;
    display: block;
  }

  .err {
    margin-top: 12px;
    font-size: 13px;
    color: var(--red);
    line-height: 1.5;
  }

  .segmented {
    display: flex;
    gap: 4px;
    padding: 3px;
    border: 1px solid var(--line);
    border-radius: 10px;
  }

  .seg {
    flex: 1;
    min-height: 36px;
    border-radius: 8px;
    font-size: 13.5px;
    font-weight: 500;
    text-align: center;
    color: var(--muted);
  }

  .seg.on {
    background: var(--surf2);
    color: var(--text);
  }

  .list {
    border-top: 1px solid var(--line);
  }

  .pref {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    min-height: 64px;
    border-bottom: 1px solid var(--line);
  }

  .pref-label {
    font-size: 14px;
    font-weight: 500;
  }

  .pref-sub {
    font-size: 12.5px;
    color: var(--muted);
    margin-top: 2px;
    line-height: 1.4;
  }

  .track {
    flex: none;
    width: 44px;
    height: 26px;
    padding: 3px;
    border-radius: 13px;
    display: flex;
    align-items: center;
    justify-content: flex-start;
    background: var(--line2);
    transition: background 0.2s ease;
  }

  .track.on {
    justify-content: flex-end;
    background: var(--accent);
  }

  .knob {
    width: 20px;
    height: 20px;
    border-radius: 10px;
    background: var(--text);
  }

  .hub-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-top: 4px;
  }

  .hub {
    font-size: 15px;
    font-weight: 600;
  }

  .link {
    font-size: 13.5px;
    font-weight: 500;
    color: var(--accentT);
    text-decoration: underline;
  }

  .foot {
    border-top: 1px solid var(--line);
    padding-top: 16px;
  }

  .brand {
    font-size: 11px;
    letter-spacing: 0.14em;
    color: var(--brandMuted);
    line-height: 1.7;
  }
</style>
