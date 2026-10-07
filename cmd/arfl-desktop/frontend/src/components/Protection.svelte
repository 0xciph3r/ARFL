<script lang="ts">
  let { connected, session, used, down, up, ipv6Exposed, onFixIPv6, onPrivacy }: {
    connected: boolean
    session: string
    used: string
    down: string
    up: string
    ipv6Exposed: boolean
    onFixIPv6: () => void
    onPrivacy: () => void
  } = $props()
</script>

<div class="col">
  <div class="grid">
    <div><div class="k">Session</div><div class="mono v">{connected ? session : '—'}</div></div>
    <div><div class="k">Used</div><div class="mono v">{connected ? used : '—'}</div></div>
    <div><div class="k">Down</div><div class="mono v">{connected ? down : '—'}</div></div>
    <div><div class="k">Up</div><div class="mono v">{connected ? up : '—'}</div></div>
  </div>
  {#if connected}
    <div class="checks">
      <div class="row"><span>IPv4 traffic is tunnelled</span><span class="ok">OK</span></div>
      <div class="row"><span>DNS set to the tunnel resolver</span><span class="ok">Configured</span></div>
      <div class="row tall">
        <span>IPv6</span>
        {#if ipv6Exposed}
          <div class="fix">
            <span class="warn">Leaks around the tunnel</span>
            <button class="amber" onclick={onFixIPv6}>Turn off IPv6</button>
          </div>
        {:else}
          <span class="ok">OK</span>
        {/if}
      </div>
    </div>
  {/if}
  <div class="foot">
    The entry sees your IP; the exit sees destination traffic. This does not prevent node collusion or DNS leaks.
    <button class="bare link" onclick={onPrivacy}>What can be seen?</button>
  </div>
</div>

<style>
  .col {
    display: flex;
    flex-direction: column;
  }

  .grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 16px 12px;
    padding-bottom: 18px;
    border-bottom: 1px solid var(--line);
  }

  .k {
    font-size: 12px;
    color: var(--muted);
  }

  .v {
    font-size: 15px;
    margin-top: 4px;
  }

  .checks {
    display: flex;
    flex-direction: column;
    margin-top: 6px;
  }

  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    min-height: 48px;
    border-bottom: 1px solid var(--line);
    font-size: 14px;
  }

  .row.tall {
    min-height: 56px;
  }

  .ok {
    font-size: 13px;
    color: var(--cyan);
    font-weight: 500;
  }

  .fix {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .warn {
    font-size: 13px;
    color: var(--amber);
    font-weight: 500;
  }

  .amber {
    min-height: 36px;
    padding: 0 12px;
    border-radius: 8px;
    background: var(--amberBtn);
    color: #1d1200;
    font-size: 13px;
  }

  .foot {
    font-size: 13.5px;
    color: var(--text2);
    margin-top: 18px;
    line-height: 1.5;
  }

  .link {
    display: inline;
    font-size: 13.5px;
    color: var(--accentT);
    font-weight: 500;
    text-decoration: underline;
  }
</style>
