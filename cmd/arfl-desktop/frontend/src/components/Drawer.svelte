<script lang="ts">
  import type { Snippet } from 'svelte'

  let {
    title,
    width = 420,
    onClose,
    children,
  }: { title: string; width?: number; onClose: () => void; children: Snippet } = $props()

  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') onClose()
  }
</script>

<svelte:window onkeydown={onKey} />

<div class="scrim">
  <button class="bare dismiss" aria-label="Close panel" onclick={onClose}></button>
  <div class="drawer" style="width:{width}px" role="dialog" aria-modal="true" aria-label={title}>
    <div class="head">
      <div class="title">{title}</div>
      <button class="bare close" aria-label="Close" onclick={onClose}>
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18" /></svg>
      </button>
    </div>
    <div class="body">
      {@render children()}
    </div>
  </div>
</div>

<style>
  .scrim {
    position: absolute;
    inset: 0;
    display: flex;
    justify-content: flex-end;
    background: var(--scrim);
    z-index: 10;
    animation: arfl-fade 0.2s ease-out;
  }

  .dismiss {
    position: absolute;
    inset: 0;
  }

  .drawer {
    position: relative;
    max-width: 100%;
    height: 100%;
    background: var(--panel);
    border-left: 1px solid var(--line);
    display: flex;
    flex-direction: column;
  }

  .head {
    height: 56px;
    flex: none;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 20px;
    border-bottom: 1px solid var(--line);
  }

  .title {
    font-size: 16px;
    font-weight: 600;
  }

  .close {
    width: 36px;
    height: 36px;
    border-radius: 8px;
    display: flex;
    align-items: center;
    justify-content: center;
    color: var(--muted);
  }

  .body {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 20px;
    display: flex;
    flex-direction: column;
  }
</style>
