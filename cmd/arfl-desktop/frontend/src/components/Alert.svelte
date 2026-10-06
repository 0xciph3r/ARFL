<script lang="ts">
  import type { Snippet } from 'svelte'
  import mark from '../assets/mark.svg'

  let {
    kicker,
    tone,
    title,
    body,
    primary,
    secondary,
    onPrimary,
    onSecondary,
    children,
  }: {
    kicker: string
    tone: 'amber' | 'red'
    title: string
    body: string
    primary: string
    secondary: string
    onPrimary: () => void
    onSecondary: () => void
    children?: Snippet
  } = $props()
</script>

<div class="scrim">
  <div class="card" role="alertdialog" aria-label={title}>
    <div class="kicker">
      <img src={mark} alt="" />
      <span style="color:var(--{tone})">{kicker}</span>
    </div>
    <div>
      <div class="title">{title}</div>
      <div class="body">{body}</div>
    </div>
    {@render children?.()}
    <div class="actions">
      <button class="primary" onclick={onPrimary}>{primary}</button>
      <button class="secondary" onclick={onSecondary}>{secondary}</button>
    </div>
  </div>
</div>

<style>
  .scrim {
    position: absolute;
    inset: 0;
    z-index: 20;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--scrim);
    animation: arfl-fade 0.2s ease-out;
  }

  .card {
    width: 448px;
    max-width: calc(100% - 32px);
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: 14px;
    padding: 22px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }

  .kicker {
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 12.5px;
    font-weight: 600;
  }

  .kicker img {
    width: 18px;
    height: 18px;
  }

  .title {
    font-size: 22px;
    font-weight: 600;
    letter-spacing: -0.01em;
  }

  .body {
    font-size: 14px;
    color: var(--text2);
    margin-top: 8px;
    line-height: 1.5;
  }

  .actions {
    display: flex;
    gap: 10px;
    margin-top: 2px;
  }

  .actions button {
    flex: 1;
    min-height: 44px;
    border-radius: 10px;
    font-size: 14.5px;
    text-align: center;
  }
</style>
