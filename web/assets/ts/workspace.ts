/**
 * workspace.ts — enhancement for an app's workspace page (/apps/<slug>).
 *
 * The page works without this file: the prompt, code, and theme forms post,
 * the server redirects back, and the next page shows the outcome. This module
 * adds what needs a script: prompt progress streamed live over Server-Sent
 * Events, a syntax-highlighting editor in place of the plain textarea (with
 * Cmd/Ctrl+S to save), saving without a page load, and refreshing the preview
 * when a change has been built.
 */

type BuildResult = { ok: boolean; log?: string; commit?: string };
type JobEvent = { seq: number; kind: string; text: string; ok?: boolean };
type Messages = {
  saving: string;
  saved: string;
  buildFailed: string;
  copilotChanged: string;
  requestFailed: string;
  finished: string;
  failed: string;
};

// Localized by the server: data-msg-* attributes on the workspace root.
let msg: Messages = {
  saving: '',
  saved: '',
  buildFailed: '',
  copilotChanged: '',
  requestFailed: '',
  finished: '',
  failed: '',
};

export function enhanceWorkspace(root: HTMLElement): void {
  const d = root.dataset;
  msg = {
    saving: d.msgSaving ?? '',
    saved: d.msgSaved ?? '',
    buildFailed: d.msgBuildfailed ?? '',
    copilotChanged: d.msgCopilotchanged ?? '',
    requestFailed: d.msgRequestfailed ?? '',
    finished: d.msgFinished ?? '',
    failed: d.msgFailed ?? '',
  };

  const log = root.querySelector<HTMLOListElement>('[data-job-log]');
  const promptForm = root.querySelector<HTMLFormElement>('form[data-prompt-form]');
  if (log && promptForm) enhancePrompt(root, promptForm, log);

  const codeForm = root.querySelector<HTMLFormElement>('form[data-code-form]');
  if (codeForm) void enhanceCode(root, codeForm);

  const themeForm = root.querySelector<HTMLFormElement>('form[data-theme-form]');
  if (themeForm) enhanceTheme(root, themeForm);
}

/* --- Shared ---------------------------------------------------------------- */

async function postForm(form: HTMLFormElement, extra?: Record<string, string>): Promise<Response> {
  const body = new URLSearchParams();
  for (const [key, value] of new FormData(form)) {
    if (typeof value === 'string') body.append(key, value);
  }
  for (const [key, value] of Object.entries(extra ?? {})) body.set(key, value);
  return fetch(form.action, {
    method: 'POST',
    headers: { Accept: 'application/json' },
    body,
    credentials: 'same-origin',
  });
}

async function errorText(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string };
    if (body.error) return body.error;
  } catch {
    // Not JSON; fall through to the status line.
  }
  return `${msg.requestFailed} (HTTP ${res.status})`;
}

function reloadPreview(root: HTMLElement): void {
  const frame = root.querySelector<HTMLIFrameElement>('iframe[data-preview]');
  if (!frame) {
    // The first successful build: only the server knows the preview URL.
    location.reload();
    return;
  }
  const url = new URL(frame.src, location.href);
  url.searchParams.set('v', String(Date.now()));
  frame.src = url.toString();
}

/**
 * Shows an outcome: a one-line summary in the form's status region (announced),
 * and any build log in a separate, focusable <pre> (not announced verbatim).
 */
function setStatus(
  form: HTMLFormElement,
  text: string,
  tone: 'info' | 'success' | 'danger',
  log = '',
): void {
  const status = form.querySelector('[data-save-status]');
  if (status) {
    status.textContent = text;
    status.setAttribute('data-tone', tone);
  }
  const pre = form.querySelector<HTMLPreElement>('[data-build-log]');
  if (pre) {
    pre.textContent = log;
    pre.hidden = log === '';
  }
}

function showBuild(form: HTMLFormElement, build: BuildResult): void {
  if (build.ok)
    setStatus(form, build.commit ? `${msg.saved} (${build.commit})` : msg.saved, 'success');
  else setStatus(form, msg.buildFailed, 'danger', build.log ?? '');
}

// A busy button stays focusable (aria-disabled), so keyboard focus is not lost
// to <body> while a request runs.
function setBusy(button: HTMLButtonElement | null, busy: boolean): void {
  if (!button) return;
  button.disabled = false;
  if (busy) button.setAttribute('aria-disabled', 'true');
  else button.removeAttribute('aria-disabled');
}

const isBusy = (button: HTMLButtonElement | null): boolean =>
  button?.getAttribute('aria-disabled') === 'true';

/* --- Prompt ---------------------------------------------------------------- */

function appendEvent(log: HTMLOListElement, event: JobEvent): void {
  const item = document.createElement('li');
  item.dataset.kind = event.kind;
  if (event.ok !== undefined) item.dataset.ok = String(event.ok);
  item.textContent = event.text;
  log.append(item);
  log.scrollTop = log.scrollHeight;
}

function enhancePrompt(root: HTMLElement, form: HTMLFormElement, log: HTMLOListElement): void {
  const submit = form.querySelector<HTMLButtonElement>('button[type="submit"]');
  const textarea = form.querySelector<HTMLTextAreaElement>('textarea');
  const announce = root.querySelector('[data-job-status]');
  if (submit?.disabled) setBusy(submit, true);

  const follow = (jobURL: string, after: number): void => {
    setBusy(submit, true);
    let ok = false;
    const source = new EventSource(`${jobURL}/events?after=${after}`);
    source.addEventListener('progress', (message) => {
      const event = JSON.parse((message as MessageEvent<string>).data) as JobEvent;
      if (event.kind === 'done') ok = event.ok === true;
      appendEvent(log, event);
    });
    source.addEventListener('end', () => {
      source.close();
      setBusy(submit, false);
      if (announce) announce.textContent = ok ? msg.finished : msg.failed;
      if (ok) reloadPreview(root);
      root.dispatchEvent(new CustomEvent('workspace:changed'));
    });
    source.addEventListener('error', () => {
      // EventSource retries a dropped connection on its own and the server
      // replays from Last-Event-ID; only a closed source is final.
      if (source.readyState !== EventSource.CLOSED) return;
      setBusy(submit, false);
      appendEvent(log, { seq: -1, kind: 'error', text: msg.requestFailed });
    });
  };

  const running = log.dataset.jobUrl;
  if (running && log.dataset.done === 'false') {
    // Resume after the events the server already rendered (the prompt line is not an event).
    follow(running, log.querySelectorAll('li:not([data-kind="prompt"])').length);
  }

  textarea?.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) form.requestSubmit();
  });

  form.addEventListener('submit', (event) => {
    event.preventDefault();
    if (isBusy(submit) || !form.reportValidity()) return;
    void (async () => {
      setBusy(submit, true);
      try {
        const res = await postForm(form);
        if (!res.ok) {
          setBusy(submit, false);
          appendEvent(log, { seq: -1, kind: 'error', text: await errorText(res) });
          return;
        }
        const { jobUrl } = (await res.json()) as { jobUrl: string };
        log.replaceChildren();
        const prompt = document.createElement('li');
        prompt.dataset.kind = 'prompt';
        prompt.textContent = textarea?.value ?? '';
        log.append(prompt);
        if (textarea) textarea.value = '';
        log.dataset.jobUrl = jobUrl;
        follow(jobUrl, 0);
      } catch {
        setBusy(submit, false);
        appendEvent(log, { seq: -1, kind: 'error', text: msg.requestFailed });
      }
    })();
  });
}

/* --- Code editor ----------------------------------------------------------- */

async function enhanceCode(root: HTMLElement, form: HTMLFormElement): Promise<void> {
  const textarea = form.querySelector<HTMLTextAreaElement>('textarea[name="content"]');
  const path = form.querySelector<HTMLInputElement>('input[name="path"]')?.value ?? '';
  const submit = form.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (!textarea) return;

  const { createEditor } = await import('./editor');
  let saved = textarea.value;
  const editor = createEditor({
    parent: textarea.parentElement as HTMLElement,
    doc: textarea.value,
    path,
    readOnly: textarea.readOnly,
    label: textarea.getAttribute('aria-label') ?? path,
    describedBy: textarea.getAttribute('aria-describedby') ?? '',
    onSave: () => form.requestSubmit(),
    onChange: () => guard(),
  });
  textarea.hidden = true;

  // Warn before leaving only while there are unsaved edits: a permanent
  // beforeunload listener would keep the page out of the back/forward cache.
  const dirty = (): boolean => editor.getValue() !== saved;
  const onBeforeUnload = (event: BeforeUnloadEvent): void => event.preventDefault();
  let guarding = false;
  function guard(): void {
    if (dirty() === guarding) return;
    guarding = !guarding;
    if (guarding) window.addEventListener('beforeunload', onBeforeUnload);
    else window.removeEventListener('beforeunload', onBeforeUnload);
  }

  form.addEventListener('submit', (event) => {
    event.preventDefault();
    if (textarea.readOnly || isBusy(submit)) return;
    void (async () => {
      setBusy(submit, true);
      setStatus(form, msg.saving, 'info');
      const content = editor.getValue();
      try {
        const res = await postForm(form, { content });
        if (!res.ok) {
          setStatus(form, await errorText(res), 'danger');
          return;
        }
        const { build } = (await res.json()) as { build: BuildResult };
        saved = content;
        textarea.value = content;
        guard();
        showBuild(form, build);
        if (build.ok) reloadPreview(root);
      } catch {
        setStatus(form, msg.requestFailed, 'danger');
      } finally {
        setBusy(submit, false);
      }
    })();
  });

  // After a prompt run, show what Copilot wrote, unless the user has unsaved edits.
  root.addEventListener('workspace:changed', () => {
    if (dirty()) {
      setStatus(form, msg.copilotChanged, 'info');
      return;
    }
    void (async () => {
      try {
        const res = await fetch(`${form.action}?path=${encodeURIComponent(path)}`, {
          headers: { Accept: 'application/json' },
          credentials: 'same-origin',
        });
        if (!res.ok) return;
        const { content } = (await res.json()) as { content: string };
        saved = content;
        editor.setValue(content);
        guard();
      } catch {
        // The open file stays as it was; a reload shows Copilot's version.
      }
    })();
  });
}

/* --- Theme ----------------------------------------------------------------- */

function enhanceTheme(root: HTMLElement, form: HTMLFormElement): void {
  const submit = form.querySelector<HTMLButtonElement>('button[type="submit"]');
  form.addEventListener('submit', (event) => {
    event.preventDefault();
    if (isBusy(submit)) return;
    void (async () => {
      setBusy(submit, true);
      setStatus(form, msg.saving, 'info');
      try {
        const res = await postForm(form);
        if (!res.ok) {
          setStatus(form, await errorText(res), 'danger');
          return;
        }
        const { build } = (await res.json()) as { build: BuildResult };
        showBuild(form, build);
        if (build.ok) reloadPreview(root);
      } catch {
        setStatus(form, msg.requestFailed, 'danger');
      } finally {
        setBusy(submit, false);
      }
    })();
  });
  // Keep each control's visible value in sync while picking.
  form.addEventListener('input', (event) => {
    const input = event.target as HTMLInputElement;
    if (input.type !== 'color' && input.type !== 'range') return;
    const label = form.querySelector(`output[for="${CSS.escape(input.id)}"]`);
    if (label) label.textContent = input.value;
  });
}
