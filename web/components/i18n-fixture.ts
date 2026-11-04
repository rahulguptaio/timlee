import catalogSource from '../locales/en/components.arb?raw';

type Part = string | [name: string];

function parts(message: string): Part[] {
  return message
    .split(/(\{[a-zA-Z][a-zA-Z0-9]*\})/)
    .filter(Boolean)
    .map((part) => {
      const match = /^\{([a-zA-Z][a-zA-Z0-9]*)\}$/.exec(part);
      return match?.[1] ? [match[1]] : part;
    });
}

/** Storybook and test data compiled from the real English catalog. */
export function englishComponentBlock(): object {
  const catalog = JSON.parse(catalogSource) as Record<string, unknown>;
  const messages: Record<string, Part[]> = {};
  for (const [key, value] of Object.entries(catalog)) {
    if (key.startsWith('@') || typeof value !== 'string') continue;
    messages[key] = parts(value);
  }
  return { lang: 'en', intl: 'en-GB', dir: 'ltr', m: messages };
}

/** Install the English block in Storybook or a component test document. */
export function installEnglishComponentMessages(): void {
  if (document.getElementById('timlee-i18n')) return;
  const block = document.createElement('script');
  block.type = 'application/json';
  block.id = 'timlee-i18n';
  block.dataset.timleeTestI18n = '';
  block.textContent = JSON.stringify(englishComponentBlock());
  document.head.append(block);
}
