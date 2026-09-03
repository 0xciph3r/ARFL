<script lang="ts">
  // The vault is encrypted at rest, so nothing is readable until the user
  // supplies the passphrase. This is the first screen on every launch.
  import { api } from '../lib/api'

  let { onUnlocked }: { onUnlocked: () => void } = $props()

  let passphrase = $state('')
  let busy = $state(false)
  let error = $state('')

  async function submit(event: Event) {
    event.preventDefault()
    if (!passphrase || busy) return

    busy = true
    error = ''
    try {
      await api.unlock(passphrase)
      passphrase = ''
      onUnlocked()
    } catch (err) {
      error = (err as Error).message
    } finally {
      busy = false
    }
  }
</script>

<form class="unlock" onsubmit={submit}>
  <div class="brand">
    <h1>ARFL</h1>
    <p>Decentralised VPN, paid over Lightning.</p>
  </div>

  <label for="passphrase">Wallet passphrase</label>
  <input
    id="passphrase"
    type="password"
    bind:value={passphrase}
    placeholder="Unlock your token vault"
    autocomplete="current-password"
  />

  <p class="hint">
    Your tokens are bearer assets encrypted on this device. There is no
    recovery — lose the passphrase and the balance is gone.
  </p>

  {#if error}
    <p class="error">{error}</p>
  {/if}

  <button type="submit" disabled={!passphrase || busy}>
    {busy ? 'Unlocking…' : 'Unlock'}
  </button>
</form>

<style>
  .unlock {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 32px 24px;
    margin: auto 0;
  }

  .brand {
    text-align: center;
    margin-bottom: 18px;
  }

  h1 {
    margin: 0;
    font-size: 30px;
    letter-spacing: 5px;
    color: var(--accent);
  }

  .brand p {
    margin: 6px 0 0;
    color: var(--muted);
    font-size: 13px;
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
</style>
