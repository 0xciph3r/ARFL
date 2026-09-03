<script lang="ts">
  // Bandwidth purchase: request an invoice, show it as a QR, then block on the
  // mint until it settles. The Go side polls the hub, so this component only
  // has to survive the wait and report the outcome.
  import QRCode from 'qrcode'
  import { api, type Invoice } from '../lib/api'

  let { onPurchased }: { onPurchased: (balance: number) => void } = $props()

  const presets = [1_000, 5_000, 25_000]

  let amount = $state(5_000)
  let invoice = $state<Invoice | null>(null)
  let qr = $state('')
  let waiting = $state(false)
  let error = $state('')
  let copied = $state(false)

  async function request() {
    if (amount <= 0) {
      error = 'Enter an amount greater than zero.'
      return
    }

    error = ''
    try {
      const created = await api.purchase(amount)
      invoice = created
      qr = await QRCode.toDataURL(created.bolt11.toUpperCase(), {
        margin: 1,
        width: 260,
        color: { dark: '#0c0e14', light: '#ffffff' },
      })
      await settle(created)
    } catch (err) {
      error = (err as Error).message
    }
  }

  // Kept separate from request() so a failure while waiting leaves the invoice
  // on screen — it may still be payable, and discarding it would strand any
  // payment already in flight.
  async function settle(created: Invoice) {
    waiting = true
    error = ''
    try {
      onPurchased(await api.awaitPurchase(created.quote_id))
      invoice = null
      qr = ''
    } catch (err) {
      error = (err as Error).message
    } finally {
      waiting = false
    }
  }

  function retry() {
    if (invoice) settle(invoice)
  }

  async function copy() {
    if (!invoice) return
    await navigator.clipboard.writeText(invoice.bolt11)
    copied = true
    setTimeout(() => (copied = false), 1_500)
  }

  function cancel() {
    invoice = null
    qr = ''
    error = ''
  }
</script>

<div class="buy">
  {#if !invoice}
    <h2>Buy bandwidth</h2>
    <p class="lede">
      You are paying the hub over Lightning for blind-signed tokens. The hub
      cannot link them back to this purchase.
    </p>

    <div class="presets">
      {#each presets as preset}
        <button
          class="secondary"
          class:active={amount === preset}
          aria-pressed={amount === preset}
          onclick={() => (amount = preset)}
        >
          {preset.toLocaleString()}
        </button>
      {/each}
    </div>

    <label for="amount">Amount (sats)</label>
    <input id="amount" type="number" min="1" bind:value={amount} />

    {#if error}
      <p class="error" role="alert">{error}</p>
    {/if}

    <button onclick={request} disabled={amount <= 0}>Request invoice</button>
  {:else}
    <h2>Pay {invoice.amount_sats.toLocaleString()} sats</h2>

    {#if qr}
      <img class="qr" src={qr} alt="Lightning invoice QR code" />
    {/if}

    <button class="secondary" onclick={copy}>
      {copied ? 'Copied' : 'Copy invoice'}
    </button>

    {#if waiting}
      <p class="waiting" role="status">Waiting for payment…</p>
    {/if}

    {#if error}
      <p class="error" role="alert">{error}</p>
      <button class="secondary" onclick={retry} disabled={waiting}>
        Check payment again
      </button>
      <p class="hint">
        Cancelling discards this invoice. If you have already paid it, check
        again instead — requesting a new one would charge you twice.
      </p>
    {/if}

    <button class="secondary" onclick={cancel} disabled={waiting}>Cancel</button>
  {/if}
</div>

<style>
  .buy {
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

  .presets {
    display: flex;
    gap: 8px;
  }

  .presets button {
    flex: 1;
    padding: 8px 0;
    font-size: 12px;
  }

  .presets button.active {
    border-color: var(--accent);
    color: var(--accent);
  }

  label {
    font-size: 12px;
    color: var(--muted);
  }

  .qr {
    align-self: center;
    width: 220px;
    height: 220px;
    border-radius: var(--radius);
    background: #fff;
    padding: 8px;
  }

  .waiting {
    margin: 0;
    text-align: center;
    font-size: 12px;
    color: var(--accent);
  }

  .error {
    margin: 0;
    font-size: 12px;
    color: var(--err);
    word-break: break-word;
  }

  .hint {
    margin: 0;
    font-size: 11px;
    line-height: 1.5;
    color: var(--muted);
  }
</style>
