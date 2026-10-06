<script lang="ts">
  let { connected, ipv6Safe }: { connected: boolean; ipv6Safe: boolean } = $props()

  const now = $derived([
    { label: 'Payment cannot be linked to your use', status: 'Always on', tone: 'ok' },
    { label: 'IPv4 traffic is tunnelled', status: connected ? 'OK' : 'Not connected', tone: connected ? 'ok' : 'off' },
    { label: 'DNS stays inside the tunnel', status: connected ? 'OK' : 'Not connected', tone: connected ? 'ok' : 'off' },
    {
      label: 'IPv6',
      status: connected ? (ipv6Safe ? 'OK' : 'Leaking') : 'Not connected',
      tone: connected ? (ipv6Safe ? 'ok' : 'bad') : 'off',
    },
  ])

  // Kept in step with the code and whitepaper sections 9 and 10.
  const limits = $derived([
    {
      title: 'Setup-time IP exposure',
      body: 'The entry node sees your real IP while the session is set up, as it does for the whole session.',
      status: 'Fixed for the exit',
      tone: 'ok',
      help: 'Your device pays the exit through the entry tunnel, so the exit never sees your IP.',
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
      help: 'Choosing nodes from Nostr instead of the hub keeps the hub out of setup. Turn it on in client.json.',
    },
    {
      title: 'IPv6 leaks',
      body: 'The tunnel carries IPv4 only. IPv6 must be off while you are connected.',
      status: ipv6Safe ? 'Fixed' : 'Leaking',
      tone: ipv6Safe ? 'ok' : 'bad',
      help: ipv6Safe ? 'IPv6 is turned off while you are connected.' : 'Turn on "Turn off IPv6 when connecting" in Settings.',
    },
  ])
</script>

<div class="col">
  <div>
    <div class="lede">Three parties touch your session. Each only gets part of the picture.</div>
    <div class="parties">
      <div class="party">
        <div class="pname">Entry</div>
        <div class="k">Sees</div>
        <div class="v">Your real IP</div>
        <div class="k blind">Blind to</div>
        <div class="v dim">The sites you visit<br />Who you are</div>
      </div>
      <div class="party mid">
        <div class="pname">Exit</div>
        <div class="k">Sees</div>
        <div class="v">The sites you visit<br />A tunnel address</div>
        <div class="k blind">Blind to</div>
        <div class="v dim">Your real IP<br />Who you are</div>
      </div>
      <div class="party last">
        <div class="pname">Hub</div>
        <div class="k">Sees</div>
        <div class="v">Your key<br />Amounts minted<br />Tokens redeemed</div>
        <div class="k blind">Blind to</div>
        <div class="v dim">Which payment paid for which session<br />Your browsing</div>
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
