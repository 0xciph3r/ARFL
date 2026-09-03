<script lang="ts">
  // The vault is encrypted at rest, so nothing is readable until the user
  // supplies the passphrase. This is the first screen on every launch.
  import { api } from '../lib/api'
  import Mark from './Mark.svelte'

  let { onUnlocked }: { onUnlocked: () => void } = $props()

  let vaultExists = $state<boolean | null>(null)
  let mode = $state<'create' | 'unlock'>('unlock')
  let passphrase = $state('')
  let confirmPassphrase = $state('')
  let resetAck = $state('')
  let busy = $state(false)
  let resetting = $state(false)
  let error = $state('')

  $effect(() => {
    void loadVaultState()
  })

  async function loadVaultState() {
    try {
      const state = await api.vaultState()
      vaultExists = state.exists
      mode = state.exists ? 'unlock' : 'create'
    } catch (err) {
      error = (err as Error).message
    }
  }

  function userError(err: unknown): string {
    const msg = (err as Error).message
    if (msg.includes('wrong passphrase or corrupted file')) {
      return 'Wrong passphrase for this wallet.'
    }
    return msg
  }

  async function submit(event: Event) {
    event.preventDefault()
    if (!passphrase || busy || resetting) return
    if (mode === 'create' && passphrase !== confirmPassphrase) {
      error = 'Passphrases do not match.'
      return
    }

    busy = true
    error = ''
    try {
      await api.unlock(passphrase)
      passphrase = ''
      confirmPassphrase = ''
      onUnlocked()
    } catch (err) {
      error = userError(err)
    } finally {
      busy = false
    }
  }

  async function resetVault() {
    if (resetAck !== 'RESET' || resetting || busy) return
    resetting = true
    error = ''
    try {
      await api.resetVault()
      passphrase = ''
      confirmPassphrase = ''
      resetAck = ''
      vaultExists = false
      mode = 'create'
    } catch (err) {
      error = (err as Error).message
    } finally {
      resetting = false
    }
  }
</script>

<form class="unlock" onsubmit={submit}>
  <div class="brand">
    <Mark size={44} glow />
    <div class="wordmark" style="--wordmark-h: 44px" role="img" aria-label="ARFL"></div>
    <p>Decentralised VPN, paid over Lightning.</p>
  </div>

  <div class="decorative-hero" aria-hidden="true"></div>

  {#if vaultExists === null}
    <p class="hint">Loading wallet state…</p>
    {#if error}
      <p class="error" role="alert">{error}</p>
    {/if}
  {:else}
    <h2>{mode === 'create' ? 'Create wallet' : 'Unlock wallet'}</h2>

    <label for="passphrase">{mode === 'create' ? 'New passphrase' : 'Wallet passphrase'}</label>
    <input
      id="passphrase"
      type="password"
      bind:value={passphrase}
      placeholder={mode === 'create' ? 'Create a strong passphrase' : 'Unlock your token vault'}
      autocomplete={mode === 'create' ? 'new-password' : 'current-password'}
    />

    {#if mode === 'create'}
      <label for="confirm-passphrase">Confirm passphrase</label>
      <input
        id="confirm-passphrase"
        type="password"
        bind:value={confirmPassphrase}
        placeholder="Confirm your passphrase"
        autocomplete="new-password"
      />
    {/if}

    <p class="hint">
      Your tokens are encrypted on this device with this passphrase. There is no
      recovery service.
    </p>

    {#if error}
      <p class="error" role="alert">{error}</p>
    {/if}

    <button
      type="submit"
      disabled={!passphrase || busy || resetting || (mode === 'create' && !confirmPassphrase)}
    >
      {#if busy}
        {mode === 'create' ? 'Creating…' : 'Unlocking…'}
      {:else}
        {mode === 'create' ? 'Create wallet' : 'Unlock'}
      {/if}
    </button>

    {#if mode === 'unlock'}
      <div class="reset">
        <p class="hint">
          Forgot your passphrase? Type <code>RESET</code> to delete this local wallet and
          create a new one. Any tokens in it will be lost.
        </p>
        <input
          type="text"
          bind:value={resetAck}
          placeholder="Type RESET to confirm"
          autocomplete="off"
        />
        <button
          type="button"
          class="secondary"
          disabled={resetAck !== 'RESET' || resetting || busy}
          onclick={resetVault}
        >
          {resetting ? 'Resetting…' : 'Reset local wallet'}
        </button>
      </div>
    {/if}
  {/if}
</form>

<style>
  .unlock {
    display: flex;
    flex-direction: column;
    gap: 10px;
    height: 100%;
    overflow-y: auto;
    padding: 20px 24px 28px;
    margin: 0;
  }

  .brand {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 14px;
    margin-bottom: 10px;
  }

  h2 {
    margin: 0 0 2px;
    font-size: 18px;
    letter-spacing: 0.2px;
  }

  .brand p {
    margin: 0;
    color: var(--muted);
    font-size: 13px;
  }

  .decorative-hero {
    width: min(100%, 360px);
    height: 100px;
    margin: 0 auto 8px;
    border-radius: 12px;
    background-image:
      linear-gradient(180deg, rgba(20, 15, 28, 0.06), rgba(20, 15, 28, 0.32)),
      url(../assets/eyes.png);
    background-size: cover;
    background-position: center;
    filter: grayscale(1) blur(0.7px) contrast(0.92);
    opacity: 0.14;
    pointer-events: none;
  }

  label {
    font-size: 12px;
    color: var(--muted);
  }

  .hint {
    margin: 2px 0 0;
    font-size: 11px;
    line-height: 1.5;
    color: var(--muted);
  }

  .error {
    margin: 0;
    font-size: 12px;
    color: var(--err);
  }

  button {
    margin-top: 6px;
  }

  .reset {
    margin-top: 8px;
    padding-top: 10px;
    border-top: 1px solid var(--border-soft);
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding-bottom: 8px;
  }

  .reset button {
    width: 100%;
  }

  @media (max-height: 720px) {
    .unlock {
      padding-top: 14px;
      gap: 8px;
    }

    .brand {
      gap: 10px;
      margin-bottom: 4px;
    }

    .decorative-hero {
      height: 76px;
      opacity: 0.12;
      margin-bottom: 4px;
    }
  }
</style>
