<script lang="ts">
  import { onDestroy } from 'svelte'
  import QRCode from 'qrcode'
  import { BrowserOpenURL, ClipboardSetText } from '../../wailsjs/runtime/runtime'
  import { api, type Invoice } from '../lib/api'
  import { cue } from '../lib/sound'
  import { TOKEN_SATS, gbFromTokens } from '../lib/units'

  let { hubName, tokens, onPurchased, onClose }: {
    hubName: string
    tokens: number
    onPurchased: () => void
    onClose: () => void
  } = $props()

  // Priced at the hub's actual mint rate: the hub issues exactly the sats paid,
  // so a tier can only cost what its tokens are backed by.
  const TIERS = [1, 10, 50].map((gb) => ({ gb, tokens: gb * 10, price: gb * 10 * TOKEN_SATS }))
  const MINT_MS = 900

  let tier = $state(1)
  let step = $state<0 | 1 | 2>(0)
  let invoice = $state<Invoice | null>(null)
  let qr = $state('')
  let waiting = $state(false)
  let error = $state('')
  let copied = $state(false)
  let mint = $state(0)
  let now = $state(Date.now())

  const clock = setInterval(() => (now = Date.now()), 1000)
  let timers: ReturnType<typeof setTimeout>[] = []
  onDestroy(() => {
    clearInterval(clock)
    timers.forEach(clearTimeout)
  })

  const picked = $derived(TIERS[tier])
  const fmt = (n: number) => n.toLocaleString('en-US')
  const cap = $derived(Math.max(100, tokens + picked.tokens))
  const remaining = $derived(
    invoice ? Math.max(0, Math.floor((new Date(invoice.expires_at).getTime() - now) / 1000)) : 0,
  )
  const expiresIn = $derived(
    `${String(Math.floor(remaining / 60)).padStart(2, '0')}:${String(remaining % 60).padStart(2, '0')}`,
  )

  async function requestInvoice() {
    error = ''
    try {
      const created = await api.purchase(picked.price)
      invoice = created
      qr = await QRCode.toDataURL(created.bolt11.toUpperCase(), {
        margin: 0,
        width: 368,
        color: { dark: '#0d0c10', light: '#00000000' },
      })
      step = 1
      await settle(created)
    } catch (err) {
      error = (err as Error).message
    }
  }

  // A failure while waiting keeps the invoice on screen: it may still be
  // payable, and discarding it would strand a payment already in flight.
  async function settle(created: Invoice) {
    waiting = true
    error = ''
    try {
      await api.awaitPurchase(created.quote_id)
      cue('pluck')
      step = 2
      mint = 0
      // The bridge pays and mints in one call; the three stages are replayed
      // at the spec's pace so the user sees what just happened.
      for (let i = 1; i <= 3; i++) {
        timers.push(
          setTimeout(() => {
            mint = i
            if (i === 3) {
              cue('mint')
              onPurchased()
            }
          }, i * MINT_MS),
        )
      }
    } catch (err) {
      error = (err as Error).message
    } finally {
      waiting = false
    }
  }

  async function copy() {
    if (!invoice) return
    await ClipboardSetText(invoice.bolt11)
    copied = true
    timers.push(setTimeout(() => (copied = false), 1500))
  }

  const mintLabels = $derived([
    `Your device blinds ${picked.tokens} tokens`,
    'The hub signs them without seeing them',
    'Your device unblinds and stores them',
  ])
</script>

{#if step === 0}
  <div class="col">
    <div class="lede">
      Paying {hubName} over Lightning. You receive blind-signed tokens, so the hub cannot link them to this payment.
    </div>
    <div class="list">
      {#each TIERS as t, i}
        <button class="bare tier" class:on={tier === i} onclick={() => (tier = i)}>
          <span class="radio" class:on={tier === i}></span>
          <div class="grow">
            <div class="gb">{t.gb} GB</div>
            <div class="sub">{fmt(t.tokens)} tokens · {fmt(t.price / t.gb)} sats per GB</div>
          </div>
          <div class="right">
            <div class="mono price">{fmt(t.price)} sats</div>
          </div>
        </button>
      {/each}
    </div>
    <div class="after">
      <div class="after-row"><span class="muted">After this you will have</span><span class="mono">{gbFromTokens(tokens + picked.tokens)} GB</span></div>
      <div class="bar">
        <div class="fill ghost" style="width:{((tokens + picked.tokens) / cap) * 100}%"></div>
        <div class="fill" style="width:{(tokens / cap) * 100}%"></div>
      </div>
    </div>
    <div class="note">Prices are set by this hub. Part of a 100 MB token is lost if you disconnect before it is used up.</div>
    {#if error}<div class="err" role="alert">{error}</div>{/if}
    <div class="grow"></div>
    <button class="primary" onclick={requestInvoice}>Request invoice · {fmt(picked.price)} sats</button>
  </div>
{:else if step === 1 && invoice}
  <div class="col gap14">
    <div class="pay-head">
      <div><div class="muted small">Pay this invoice</div><div class="big">{fmt(invoice.amount_sats)} sats</div></div>
      <div class="muted small right">{picked.gb} GB at<br />{hubName}</div>
    </div>
    <div class="qr-wrap"><img src={qr} alt="Lightning invoice QR code" /></div>
    <div class="mono bolt">{invoice.bolt11}</div>
    <div class="row2">
      <button class="secondary" onclick={copy}>{copied ? 'Copied' : 'Copy'}</button>
      <button class="secondary" onclick={() => invoice && BrowserOpenURL('lightning:' + invoice.bolt11)}>Open wallet</button>
    </div>
    {#if waiting}
      <div class="waiting"><span class="pulse arfl-tick"></span>Waiting for payment · expires in {expiresIn}</div>
    {/if}
    {#if error}
      <div class="err" role="alert">{error}</div>
      <button class="secondary" onclick={() => invoice && settle(invoice)}>Check again</button>
    {/if}
    <div class="grow"></div>
    <button class="secondary" onclick={() => { step = 0; invoice = null; error = '' }}>Choose another amount</button>
  </div>
{:else}
  <div class="col gap18">
    <div>
      <div class="received">Payment received</div>
      <div class="mint-title">{mint < 3 ? `Minting ${picked.tokens} tokens` : `${picked.gb} GB added`}</div>
    </div>
    <div class="steps">
      {#each mintLabels as label, i}
        <div class="mstep">
          <div class="mmark" class:done={mint > i} class:now={mint === i} class:arfl-tick={mint === i}>{mint > i ? '✓' : i + 1}</div>
          <div class="mlabel" class:done={mint > i} class:now={mint === i}>{label}</div>
        </div>
      {/each}
    </div>
    <div class="note">
      The hub never sees the tokens you end up with, so it cannot connect them to this payment. They only work at {hubName}.
    </div>
    <div class="grow"></div>
    <button class="primary" disabled={mint < 3} onclick={onClose}>{mint < 3 ? 'Minting…' : 'Done'}</button>
  </div>
{/if}

<style>
  .col {
    display: flex;
    flex-direction: column;
    flex: 1;
  }

  .gap14 {
    gap: 14px;
  }

  .gap18 {
    gap: 18px;
  }

  .grow {
    flex: 1;
  }

  .lede {
    font-size: 14px;
    color: var(--muted);
    line-height: 1.5;
  }

  .list {
    margin-top: 16px;
    border-top: 1px solid var(--line);
  }

  .tier {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 16px 4px;
    border-bottom: 1px solid var(--line);
    color: var(--text);
  }

  .tier.on {
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

  .gb {
    font-size: 15px;
    font-weight: 600;
  }

  .sub,
  .small {
    font-size: 12.5px;
    color: var(--muted);
    margin-top: 2px;
  }

  .right {
    text-align: right;
  }

  .price {
    font-size: 14px;
  }

  .after {
    margin-top: 16px;
  }

  .after-row {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    font-size: 13px;
  }

  .muted {
    color: var(--muted);
  }

  .bar {
    height: 6px;
    border-radius: 3px;
    background: var(--line);
    margin-top: 8px;
    position: relative;
    overflow: hidden;
  }

  .fill {
    position: absolute;
    left: 0;
    top: 0;
    height: 6px;
    background: var(--accent);
  }

  .fill.ghost {
    opacity: 0.4;
  }

  .note {
    font-size: 12.5px;
    color: var(--muted);
    margin-top: 14px;
    line-height: 1.5;
  }

  .gap18 .note {
    margin-top: 0;
    font-size: 13px;
  }

  .err {
    font-size: 13px;
    color: var(--red);
    margin-top: 12px;
    line-height: 1.5;
  }

  .primary {
    min-height: 48px;
    border-radius: 10px;
    font-size: 15px;
    text-align: center;
  }

  .primary:disabled {
    opacity: 1;
    background: transparent;
    border: 1px solid var(--line2);
    color: var(--muted);
  }

  .pay-head {
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
  }

  .big {
    font-size: 24px;
    font-weight: 600;
    margin-top: 4px;
  }

  .qr-wrap {
    align-self: center;
    width: 204px;
    background: var(--qrbg);
    padding: 10px;
    border-radius: 12px;
  }

  .qr-wrap img {
    width: 100%;
    display: block;
  }

  .bolt {
    font-size: 12px;
    color: var(--muted);
    text-align: center;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .row2 {
    display: flex;
    gap: 8px;
  }

  .row2 button {
    flex: 1;
    min-height: 44px;
    font-size: 14px;
  }

  .secondary {
    min-height: 44px;
    border-radius: 10px;
    text-align: center;
  }

  .waiting {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 8px;
    font-size: 13px;
    color: var(--muted);
  }

  .pulse {
    width: 8px;
    height: 8px;
    border-radius: 4px;
    background: var(--amber);
  }

  .received {
    font-size: 12.5px;
    color: var(--cyan);
    font-weight: 500;
  }

  .mint-title {
    font-size: 22px;
    font-weight: 600;
    margin-top: 4px;
    letter-spacing: -0.01em;
  }

  .steps {
    border-top: 1px solid var(--line);
  }

  .mstep {
    display: flex;
    align-items: center;
    gap: 14px;
    min-height: 56px;
    border-bottom: 1px solid var(--line);
  }

  .mmark {
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

  .mmark.now {
    border-color: var(--accent);
    color: var(--text);
  }

  .mmark.done {
    border: 0;
    background: var(--cyanBg);
    color: var(--cyan);
  }

  .mlabel {
    font-size: 14px;
    color: var(--muted);
  }

  .mlabel.now {
    color: var(--text);
    font-weight: 500;
  }

  .mlabel.done {
    color: var(--text2);
  }
</style>
