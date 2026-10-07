<script lang="ts">
  let { connected }: { connected: boolean } = $props()

  const now = $derived([
    { label: 'Blind tokens prevent a direct purchase-to-redemption match', status: 'Token only', tone: 'ok' },
    { label: 'IPv4 traffic is tunnelled', status: connected ? 'OK' : 'Not connected', tone: connected ? 'ok' : 'off' },
    { label: 'DNS set to the tunnel resolver', status: connected ? 'Configured' : 'Not connected', tone: connected ? 'ok' : 'off' },
    {
      label: 'Outbound IPv6 block',
      status: connected ? 'Configured at connect' : 'Not connected',
      tone: connected ? 'ok' : 'off',
    },
  ])

  // Kept in step with the code and whitepaper sections 9 and 10.
  const limits = $derived([
    {
      title: 'Setup-time IP exposure',
      body: 'The entry sees your real IP. With direct HTTP delivery, the exit is set up through the entry tunnel.',
      status: 'Entry sees IP',
      tone: 'ok',
      help: 'The exit normally sees the entry, not your source IP; colluding nodes can correlate sessions.',
    },
    {
      title: 'Purchase IP exposure',
      body: 'The hub sees your network address when your device contacts it to buy bandwidth.',
      status: 'Visible to hub',
      tone: 'warn',
      help: 'Blind signatures protect tokens, not the network metadata of a purchase.',
    },
    {
      title: 'Relay metadata',
      body: 'If you use Nostr relay setup, a relay can see your network address and when you connect.',
      status: 'Visible to relay',
      tone: 'warn',
      help: 'Token contents are encrypted; the relay still sees connection metadata.',
    },
    {
      title: 'Hub as double-spend referee',
      body: 'The hub checks every token, so you rely on its honesty and uptime.',
      status: 'Trust hub',
      tone: 'warn',
      help: 'Pick a hub you trust, or run your own. The hub code is open source.',
    },
    {
      title: 'Hub timing correlation',
      body: 'A compromised hub could match your two hops by timing alone.',
      status: 'Possible',
      tone: 'warn',
      help: 'Relay discovery removes the hub node-list lookup, but the hub still sees token redemption.',
    },
    {
      title: 'Same party behind both nodes',
      body: 'Different node keys do not prove different people operate the two hops.',
      status: 'Possible',
      tone: 'warn',
      help: 'Hubs vet operators, but this is not a cryptographic independence guarantee.',
    },
    {
      title: 'Fail-closed protection',
      body: 'There is no verified kill switch. If the tunnel drops, traffic may use your normal connection.',
      status: 'Not guaranteed',
      tone: 'warn',
      help: 'Stop sensitive traffic if the app reports a disconnected or interrupted session.',
    },
    {
      title: 'IPv6 outside the tunnel',
      body: 'The tunnel carries IPv4 only. An outbound IPv6 block is installed during connection setup, but is not continuously checked.',
      status: 'Block required',
      tone: 'warn',
      help: 'A failed block aborts connection. This is not a general kill switch for IPv4 traffic.',
    },
  ])
</script>

<div class="col">
  <div>
    <div class="lede">These parties may observe a session. Their information can be combined if they cooperate.</div>
    <div class="parties">
      <div class="party">
        <div class="pname">Entry</div>
        <div class="k">Sees</div>
        <div class="v">Your real IP</div>
        <div class="k blind">Blind to</div>
        <div class="v dim">Destinations inside the encrypted inner tunnel</div>
      </div>
      <div class="party mid">
        <div class="pname">Exit</div>
        <div class="k">Sees</div>
        <div class="v">Destination IPs<br />Traffic leaving the tunnel</div>
        <div class="k blind">Blind to</div>
        <div class="v dim">Your source IP, without collusion</div>
      </div>
      <div class="party last">
        <div class="pname">Hub</div>
        <div class="k">Sees</div>
        <div class="v">Your IP when you buy<br />Amounts minted<br />Token redemption timing</div>
        <div class="k blind">Blind to</div>
        <div class="v dim">Which payment funded a blind proof<br />Your browsing</div>
      </div>
    </div>
  </div>

  <div>
    <div class="h">Right now</div>
    <div class="list">
      {#each now as r}
        <div class="row"><span>{r.label}</span><span class="tone {r.tone}">{r.status}</span></div>
      {/each}
    </div>
  </div>

  <div>
    <div class="h">Not covered yet</div>
    <div class="sub">What this version cannot protect, and what helps.</div>
    <div class="list">
      {#each limits as l}
        <div class="limit">
          <div class="lhead"><span class="ltitle">{l.title}</span><span class="tone {l.tone}">{l.status}</span></div>
          <div class="lbody">{l.body}</div>
          <div class="lhelp"><span class="helps">What helps: </span>{l.help}</div>
        </div>
      {/each}
    </div>
  </div>

  <div class="foot">Based on the ARFL whitepaper, sections 9 and 10.</div>
</div>

<style>
  .col {
    display: flex;
    flex-direction: column;
    gap: 28px;
  }

  .lede {
    font-size: 14px;
    color: var(--text2);
    line-height: 1.5;
  }

  .parties {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    margin-top: 16px;
    border-top: 1px solid var(--line);
    border-bottom: 1px solid var(--line);
  }

  .party {
    padding: 16px 14px 16px 0;
    border-right: 1px solid var(--line);
  }

  .party.mid {
    padding: 16px 14px;
  }

  .party.last {
    padding: 16px 0 16px 14px;
    border-right: 0;
  }

  .pname {
    font-size: 15px;
    font-weight: 600;
  }

  .k {
    font-size: 12px;
    color: var(--muted);
    margin-top: 14px;
  }

  .k.blind {
    color: var(--cyan);
    font-weight: 600;
  }

  .v {
    font-size: 13.5px;
    margin-top: 4px;
    line-height: 1.4;
  }

  .v.dim {
    color: var(--text2);
  }

  .h {
    font-size: 14px;
    font-weight: 600;
  }

  .sub {
    font-size: 12.5px;
    color: var(--muted);
    margin-top: 4px;
    line-height: 1.5;
  }

  .list {
    margin-top: 6px;
    border-top: 1px solid var(--line);
  }

  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    min-height: 48px;
    border-bottom: 1px solid var(--line);
    font-size: 14px;
  }

  .tone {
    flex: none;
    font-size: 12.5px;
    font-weight: 500;
  }

  .tone.ok {
    color: var(--cyan);
  }

  .tone.warn {
    color: var(--amber);
  }

  .tone.bad {
    color: var(--red);
  }

  .tone.off {
    color: var(--muted);
    font-weight: 400;
  }

  .limit {
    padding: 14px 0;
    border-bottom: 1px solid var(--line);
  }

  .lhead {
    display: flex;
    justify-content: space-between;
    gap: 16px;
    align-items: baseline;
  }

  .ltitle {
    font-size: 14px;
    font-weight: 500;
  }

  .lbody {
    font-size: 13px;
    color: var(--text2);
    margin-top: 4px;
    line-height: 1.5;
  }

  .lhelp {
    font-size: 12.5px;
    color: var(--muted);
    margin-top: 6px;
    line-height: 1.5;
  }

  .helps {
    color: var(--cyan);
    font-weight: 500;
  }

  .foot {
    font-size: 12px;
    color: var(--muted);
    line-height: 1.55;
  }
</style>
