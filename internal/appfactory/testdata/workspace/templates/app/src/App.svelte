<script>
  import '@awesome.me/webawesome/dist/components/button/button.js';
  import '@awesome.me/webawesome/dist/components/card/card.js';
  import { getResolvedColorScheme, onColorSchemeChange, toggleColorScheme } from '@timlee/design';

  let scheme = $state(getResolvedColorScheme());
  $effect(() => onColorSchemeChange((next) => (scheme = next)));
</script>

<header class="wa-split site-header">
  <strong>__APP_NAME__</strong>
  <wa-button size="small" appearance="plain" onclick={toggleColorScheme}>
    {scheme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
  </wa-button>
</header>

<main class="wa-stack wa-gap-xl">
  <wa-card>
    <div class="wa-stack">
      <h1>__APP_NAME__</h1>
      <p class="wa-body-l">__APP_DESCRIPTION__</p>
      <div>
        <wa-button variant="brand">Get started</wa-button>
      </div>
    </div>
  </wa-card>
</main>

<style>
  .site-header,
  main {
    max-inline-size: 64rem;
    margin-inline: auto;
    padding: var(--wa-space-l) var(--wa-space-xl);
  }
</style>
