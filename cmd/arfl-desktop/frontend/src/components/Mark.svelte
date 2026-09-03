<script lang="ts">
  // The ARFL mark, inline rather than an <img> so it can be tinted from CSS
  // and stay crisp at any size. The path geometry is Meyer's, taken from
  // assets/brand/arfl-logo-dark.svg; stroke width is tuned for small UI sizes.
  let { size = 20, glow = false }: { size?: number; glow?: boolean } = $props()
</script>

<svg
  class:glow
  width={size}
  height={size}
  viewBox="0 0 260 260"
  fill="none"
  aria-hidden="true"
>
  <path
    d="M5 5H187L255 73V255H73L5 187Z"
    stroke="currentColor"
    stroke-width="14"
    stroke-linejoin="miter"
  />
</svg>

<style>
  svg {
    display: block;
    color: var(--accent);
    flex-shrink: 0;
  }

  .glow {
    filter: drop-shadow(0 0 10px var(--accent-glow));
  }
</style>
