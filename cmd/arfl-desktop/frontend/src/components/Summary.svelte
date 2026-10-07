<script lang="ts">
  export type SessionSummary = {
    dur: string
    usedMB: number
    tokens: number
    forfeitMB: number
    hub: string
    route: string
  }

  let { last, onReconnect, onClose }: {
    last: SessionSummary
    onReconnect: () => void
    onClose: () => void
  } = $props()
</script>

<div class="col">
  <div class="lede">The tunnel is closed. Your traffic is not protected until you connect again.</div>
  <div class="grid">
    <div><div class="k">Time</div><div class="mono v">{last.dur}</div></div>
    <div><div class="k">Data used</div><div class="mono v">{last.usedMB} MB</div></div>
    <div><div class="k">Tokens spent</div><div class="mono v">{last.tokens}</div></div>
    <div><div class="k">Left unused in last token</div><div class="mono v">{last.forfeitMB} MB</div></div>
    <div><div class="k">Hub</div><div class="t">{last.hub}</div></div>
    <div><div class="k">Route</div><div class="t">{last.route}</div></div>
  </div>
  <div class="note">Tokens are redeemed whole, so the unused part of the last one is lost when a session ends. A hub or node operator may keep connection metadata.</div>
  <div class="grow"></div>
  <div class="actions">
    <button class="primary" onclick={onReconnect}>Reconnect</button>
    <button class="secondary" onclick={onClose}>Close</button>
  </div>
</div>

<style>
  .col {
    display: flex;
    flex-direction: column;
    flex: 1;
  }

  .grow {
    flex: 1;
  }

  .lede {
    font-size: 14px;
    color: var(--text2);
    line-height: 1.5;
  }

  .grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 18px 12px;
    margin-top: 20px;
    padding: 18px 0;
    border-top: 1px solid var(--line);
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

  .t {
    font-size: 14px;
    margin-top: 4px;
    line-height: 1.35;
  }

  .note {
    font-size: 12.5px;
    color: var(--muted);
    margin-top: 14px;
    line-height: 1.5;
  }

  .actions {
    display: flex;
    gap: 10px;
    padding-top: 16px;
  }

  .actions button {
    flex: 1;
    min-height: 48px;
    border-radius: 10px;
    font-size: 15px;
    text-align: center;
  }
</style>
