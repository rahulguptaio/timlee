// Starts a sign-in in the system browser and waits for it. The app does the
// work (src-tauri/src/signin.rs); this page only says what is happening.

import { invoke, localize, t } from './locale.js';

// The codes the site and the app send a reader back here with, and what each
// one says.
const ERRORS = new Map([
  ['timeout', 'signIn.error.timeout'],
  ['listener', 'signIn.error.listener'],
  ['browser', 'signIn.error.browser'],
  ['link_expired', 'signIn.error.linkExpired'],
  ['desktop_flow', 'signIn.error.desktopFlow'],
  ['access_denied', 'signIn.error.accessDenied'],
  ['provider_failed', 'signIn.error.providerFailed'],
]);

const views = {
  start: document.querySelector('[data-view="start"]'),
  waiting: document.querySelector('[data-view="waiting"]'),
};
const error = document.querySelector('[data-error]');

function show(name) {
  for (const [key, view] of Object.entries(views)) view.hidden = key !== name;
  views[name].querySelector('h1').focus();
}

function fail(code) {
  error.textContent = t(ERRORS.get(code) ?? 'signIn.error.other');
  error.hidden = false;
  show('start');
}

async function start() {
  try {
    await invoke('sign_in');
    error.hidden = true;
    show('waiting');
  } catch (reason) {
    fail(String(reason));
  }
}

for (const button of document.querySelectorAll('[data-start]')) {
  button.addEventListener('click', start);
}
document.querySelector('[data-cancel]').addEventListener('click', async () => {
  await invoke('cancel_sign_in');
  show('start');
});

await localize();
const code = new URLSearchParams(location.search).get('error');
if (code) fail(code);
