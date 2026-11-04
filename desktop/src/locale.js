// Puts a bundled page in the app's language: the site's, else the system's,
// else English (src-tauri/src/i18n.rs). An element names its message with
// data-i18n; the page stays hidden until they are filled in (app.css).

export const invoke = (command, args) => window.__TAURI_INTERNALS__.invoke(command, args);

// A host name or a time keeps its own direction inside a sentence in any
// language, as on the site (docs/I18N.md §7).
const FIRST_STRONG_ISOLATE = '\u2068';
const POP_DIRECTIONAL_ISOLATE = '\u2069';

let messages = {};

// The message `key` with its {name} arguments filled in.
export function t(key, args = {}) {
  const has = (object, name) => Object.prototype.hasOwnProperty.call(object, name);
  const message = has(messages, key) ? messages[key] : key;
  return message.replace(/\{(\w+)\}/g, (placeholder, name) =>
    has(args, name) ? FIRST_STRONG_ISOLATE + args[name] + POP_DIRECTIONAL_ISOLATE : placeholder,
  );
}

// Asks the app for the page's language and words, puts them on the page, and
// returns the rest of what the app said.
export async function localize() {
  const root = document.documentElement;
  try {
    const boot = await invoke('boot');
    messages = boot.messages;
    root.lang = boot.lang;
    root.dir = boot.dir;
    for (const element of document.querySelectorAll('[data-i18n]')) {
      element.textContent = t(element.dataset.i18n, { host: boot.host });
    }
    return boot;
  } finally {
    delete root.dataset.localizing;
  }
}
