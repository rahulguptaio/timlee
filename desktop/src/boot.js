// The first page the window loads. It checks that the site answers before
// handing the window over to it, so an unreachable site gets this screen
// rather than a blank window, and comes back on its own when the site does.

import { invoke, localize, t } from './locale.js';

const RETRY_EVERY_MS = 15_000;
const PROBE_TIMEOUT_MS = 8_000;
// A navigation that has not replaced this page by now has failed: WebKit
// leaves the current page in place, silently, when the site cannot be reached.
const NAVIGATION_TIMEOUT_MS = 20_000;

const connecting = document.querySelector('[data-view="connecting"]');
const offline = document.querySelector('[data-view="offline"]');
const heading = document.querySelector('#offline-title');
const retry = document.querySelector('[data-retry]');
const status = document.querySelector('[data-status]');

const { target, probe, intl } = await localize();

let timer = 0;

// Any HTTP answer at all — even an error page — proves the site is there, and
// then the site gets to say what is wrong. Only a network failure rejects.
async function reachable() {
  const controller = new AbortController();
  const abort = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
  try {
    await fetch(probe, {
      method: 'HEAD',
      mode: 'no-cors',
      cache: 'no-store',
      credentials: 'omit',
      signal: controller.signal,
    });
    return true;
  } catch {
    return false;
  } finally {
    clearTimeout(abort);
  }
}

function report(state, message) {
  status.dataset.state = state;
  status.textContent = message;
}

function showOffline() {
  if (!offline.hidden) return;
  connecting.hidden = true;
  offline.hidden = false;
  invoke('reveal');
  heading.focus();
}

async function attempt() {
  clearTimeout(timer);
  retry.disabled = true;
  report('busy', t('boot.status.checking'));

  if (await reachable()) {
    location.replace(target);
    timer = setTimeout(giveUp, NAVIGATION_TIMEOUT_MS);
    return;
  }
  giveUp();
}

function giveUp() {
  showOffline();
  retry.disabled = false;
  const time = new Date().toLocaleTimeString(intl, { hour: 'numeric', minute: '2-digit' });
  report('idle', t('boot.status.lastChecked', { time }));
  timer = setTimeout(attempt, RETRY_EVERY_MS);
}

retry.addEventListener('click', attempt);
window.addEventListener('online', attempt);
attempt();
