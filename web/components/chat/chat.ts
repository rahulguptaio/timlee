/**
 * <timlee-chat> — a conversation's transcript and its composer.
 *
 *   <timlee-chat enter-sends>
 *     <section role="log" aria-label="Messages" tabindex="0" data-transcript>
 *       <ol data-messages>…the messages the server rendered…</ol>
 *     </section>
 *     <template data-template="user"><li data-message="user">…<div data-body></div></li></template>
 *     <template data-template="assistant"><li data-message="assistant">…<div data-body></div></li></template>
 *     <form method="post" action="/apps/example/prompt" data-composer>
 *       <label for="composer-input">Message</label>
 *       <textarea id="composer-input" name="prompt" data-composer-input></textarea>
 *       <timlee-icon-button data-send-wrap><button type="submit" data-send>…</button></timlee-icon-button>
 *       <timlee-icon-button hidden data-stop-wrap><button type="button" data-stop>…</button></timlee-icon-button>
 *     </form>
 *   </timlee-chat>
 *
 * Everything the reader sees is the server's: the history is rendered by Go,
 * the composer is a form that posts without script, and the markup for a new
 * message is a <template> the server wrote, so its words come from the server
 * too. The element adds what only script can: Enter to send, Send that becomes
 * Stop in place, a reply that streams in as plain text, the swap to the
 * server's rendering of it when it ends, and a transcript that follows the
 * stream without moving what the reader is reading.
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM, for the reasons in base.ts, and no markdown parser. A
 *    streaming reply is plain text written into Text nodes once per animation
 *    frame — never innerHTML per token — and a blank line starts a new block,
 *    so a long reply lays out only its last paragraph again. When the reply
 *    ends, the page asks the server for its HTML and `replace()` swaps it in.
 *  - The transcript is a polite log that never speaks for itself. While a
 *    turn is in flight, and for a moment after any change made in its name,
 *    it is `aria-busy` and `aria-live="off"`, so neither tokens nor swapped
 *    HTML are read out; the page announces a finished reply once.
 *  - A streamed frame's text and the scroll that follows it are written in
 *    the same animation frame, before that frame's layout: the scroll then
 *    carries everything below the text with it, which the browser does not
 *    count as layout shift. Anything else that resizes the transcript is
 *    followed from a ResizeObserver callback. Following stops once the
 *    reply's start reaches the top of the transcript: a long reply is read
 *    from its beginning, not chased to its end.
 *  - The server may lay the transcript out as a reversed column, which lands
 *    a page without script at its end. Upgraded, it runs forwards
 *    (`data-forward`) and is held at its end by its scroll position: a reply
 *    growing at the end of a reversed column moves every message above it,
 *    which the browser counts as layout shift, where a scroll does not.
 *  - Enter is ignored while an input method is composing (`isComposing`, and
 *    `keyCode` 229, Safari's tell for a confirming Enter it delivers after
 *    compositionend), so choosing a candidate never sends half a sentence.
 *  - Its stylesheet, chat.css, is imported by app.css rather than bundled
 *    into components.css.
 */

import { define, TimleeElement } from '../base';

export type ChatPhase = 'idle' | 'submitted' | 'streaming';
export type ChatRole = 'user' | 'assistant';

/** `timlee-chat-send`: cancel it to take the message; leave it and the form posts. */
export interface ChatSendDetail {
  text: string;
}

export interface MessageFields {
  id?: string;
  name?: string;
  time?: Date;
  timeText?: string;
  /** Plain text, shown as written until the server's rendering replaces it. */
  text?: string;
}

/** How close to the end, in CSS pixels, still counts as at the end. */
const AT_END_PX = 32;
/** How long after a wheel, touch, key or pointer press a scroll counts as the reader's. */
const READER_INTENT_MS = 400;
/**
 * How long the transcript stays silent after a change made while it was
 * quiet. Accessibility trees catch up a frame or more after the DOM; a log
 * that turned polite again sooner would read the change out.
 */
const QUIET_TAIL_MS = 500;

const READER_EVENTS = ['wheel', 'touchmove', 'keydown', 'pointerdown'] as const;

function streamBlock(text = ''): HTMLParagraphElement {
  const block = document.createElement('p');
  block.dataset.stream = '';
  block.append(document.createTextNode(text));
  return block;
}

/**
 * A reply as it streams: text in, Text nodes out, once per frame. A blank
 * line ends a block; newlines at the end of a chunk are held until the next
 * character says whether they end a line or a block.
 */
export class ReplyStream {
  readonly #body: HTMLElement;
  readonly #onCommit: (() => void) | undefined;
  #node: Text | null = null;
  #pending = '';
  #held = '';
  #text = '';
  #frame = 0;

  constructor(body: HTMLElement, onCommit?: () => void) {
    this.#body = body;
    this.#onCommit = onCommit;
  }

  /** Everything pushed so far, written or not. */
  get text(): string {
    return this.#text + this.#pending;
  }

  push(text: string): void {
    if (!text) return;
    this.#pending += text;
    if (!this.#frame) this.#frame = requestAnimationFrame(this.#commit);
  }

  /** Writes what is still waiting for a frame. */
  flush(): void {
    if (this.#frame) cancelAnimationFrame(this.#frame);
    this.#commit();
  }

  /** Stops writing; what is on screen stays. */
  cancel(): void {
    if (this.#frame) cancelAnimationFrame(this.#frame);
    this.#frame = 0;
    this.#pending = '';
  }

  #commit = (): void => {
    this.#frame = 0;
    if (!this.#pending) return;
    const incoming = this.#pending;
    this.#pending = '';
    this.#text += incoming;

    let text = this.#held + incoming;
    this.#held = /\n+$/.exec(text)?.[0] ?? '';
    if (this.#held) text = text.slice(0, -this.#held.length);
    text.split(/\n{2,}/).forEach((piece, index) => {
      if (index > 0 && this.#node?.data) this.#node = null;
      if (!piece) return;
      if (this.#node) {
        this.#node.appendData(piece);
        return;
      }
      const block = streamBlock(piece);
      this.#body.append(block);
      this.#node = block.firstChild as Text;
    });
    this.#onCommit?.();
  };
}

export class TimleeChat extends TimleeElement {
  #phase: ChatPhase = 'idle';
  #hush = 0;
  /** Keeping the end of the transcript in view. */
  #following = true;
  /** The streaming reply whose start following stops at, until it does. */
  #reply: HTMLElement | null = null;
  /** The reply has filled the viewport and continues below it. */
  #continued = false;
  /** Where this element last left the scroll position, to tell its own scrolls apart. */
  #lastTop = 0;
  #touchedAt = Number.NEGATIVE_INFINITY;
  #resize: ResizeObserver | null = null;

  /* ------------------------------------------------------------ parts -- */

  get log(): HTMLElement | null {
    return this.querySelector<HTMLElement>('[data-transcript]');
  }

  get list(): HTMLElement | null {
    return this.querySelector<HTMLElement>('[data-messages]');
  }

  get form(): HTMLFormElement | null {
    return this.querySelector<HTMLFormElement>('form[data-composer]');
  }

  get field(): HTMLTextAreaElement | null {
    return this.querySelector<HTMLTextAreaElement>('[data-composer-input]');
  }

  #part(name: string): HTMLElement | null {
    return this.querySelector<HTMLElement>(`[data-${name}]`);
  }

  /* ------------------------------------------------------------ state -- */

  /** Enter sends, or starts a new line; Ctrl or ⌘ with Enter always sends. */
  get enterSends(): boolean {
    return this.hasAttribute('enter-sends');
  }

  set enterSends(value: boolean) {
    this.toggleAttribute('enter-sends', value);
  }

  get phase(): ChatPhase {
    return this.#phase;
  }

  /** The composer's text. */
  get value(): string {
    return this.field?.value ?? '';
  }

  set value(text: string) {
    const field = this.field;
    if (!field) return;
    field.value = text;
    field.dispatchEvent(new Event('input', { bubbles: true }));
  }

  /**
   * Idle shows Send; submitted and streaming show Stop in its place and keep
   * the transcript quiet. Focus on the button that goes moves to the field,
   * never to the page.
   */
  setPhase(phase: ChatPhase): void {
    if (phase === this.#phase) return;
    const busy = phase !== 'idle';
    const send = this.#part('send-wrap');
    const stop = this.#part('stop-wrap');
    const leaving = busy ? send : stop;
    const refocus = Boolean(leaving?.contains(document.activeElement));
    this.#phase = phase;
    this.toggleAttribute('busy', busy);
    if (send) send.hidden = busy;
    if (stop) stop.hidden = !busy;
    if (refocus) this.field?.focus();
    this.#syncLive();
  }

  /* --------------------------------------------------------- messages -- */

  /** A message from the server's template for `role`, filled in and appended. */
  addMessage(role: ChatRole, fields: MessageFields = {}): HTMLElement | null {
    const template = this.querySelector<HTMLTemplateElement>(`template[data-template="${role}"]`);
    const list = this.list;
    const first = template?.content.firstElementChild;
    if (!list || !first) return null;
    const message = first.cloneNode(true) as HTMLElement;
    if (fields.id) message.id = `msg-${fields.id}`;
    const name = message.querySelector('[data-name]');
    if (name && fields.name) name.textContent = fields.name;
    const time = message.querySelector('time');
    if (time && fields.time) {
      time.dateTime = fields.time.toISOString();
      if (fields.timeText) time.textContent = fields.timeText;
    }
    if (fields.text) message.querySelector('[data-body]')?.append(streamBlock(fields.text));
    this.quietly(() => list.append(message));
    return message;
  }

  /** Streams into `message`'s body, following it until its start reaches the top. */
  stream(message: HTMLElement, onCommit?: () => void): ReplyStream {
    this.#reply = message;
    return new ReplyStream(message.querySelector<HTMLElement>('[data-body]') ?? message, () => {
      this.#follow();
      onCommit?.();
    });
  }

  /**
   * Replaces `targets` — one message, or a run of adjacent ones — with the
   * server's markup for them, quietly, and returns what took their place.
   * Focus that was inside them moves to the new markup's first control, or
   * to the field.
   */
  replace(targets: Element | readonly Element[], html: string): HTMLElement[] {
    const list = targets instanceof Element ? [targets] : targets;
    const [first, ...rest] = list;
    if (!first) return [];
    const template = document.createElement('template');
    template.innerHTML = html;
    const nodes = Array.from(template.content.children) as HTMLElement[];
    const hadFocus = list.some((target) => target.contains(document.activeElement));
    if (this.#reply && list.includes(this.#reply)) this.#reply = null;
    this.quietly(() => {
      first.replaceWith(template.content);
      for (const target of rest) target.remove();
    });
    if (hadFocus) {
      const next = nodes
        .map((node) => node.querySelector<HTMLElement>('summary, button, a[href], [tabindex]'))
        .find(Boolean);
      (next ?? this.field)?.focus({ preventScroll: true });
    }
    return nodes;
  }

  /** Replaces the whole history, as on opening another conversation. */
  replaceAll(html: string): void {
    const list = this.list;
    if (!list) return;
    const template = document.createElement('template');
    template.innerHTML = html;
    this.#reply = null;
    this.quietly(() => list.replaceChildren(template.content));
    requestAnimationFrame(() => this.#positionOpenedConversation());
  }

  /** Runs `change` with the transcript silent, and keeps it so until the change has been taken in. */
  quietly(change: () => void): void {
    this.#hush += 1;
    this.#syncLive();
    try {
      change();
    } finally {
      window.setTimeout(() => {
        this.#hush -= 1;
        this.#syncLive();
      }, QUIET_TAIL_MS);
    }
  }

  #syncLive(): void {
    const log = this.log;
    if (!log) return;
    if (this.#hush > 0 || this.#phase !== 'idle') {
      log.setAttribute('aria-busy', 'true');
      log.setAttribute('aria-live', 'off');
    } else {
      log.removeAttribute('aria-busy');
      log.removeAttribute('aria-live');
    }
  }

  /* -------------------------------------------------------- scrolling -- */

  /**
   * Starts a turn with its prompt at the transcript's top padding. The stream
   * may follow from there until the reply itself reaches the same edge.
   */
  beginTurn(prompt: HTMLElement): void {
    const log = this.log;
    if (!log) return;
    this.#following = true;
    this.#continued = false;
    this.#reply = null;
    const by =
      prompt.getBoundingClientRect().top - log.getBoundingClientRect().top - this.#topInset(prompt);
    log.scrollTop += by;
    this.#lastTop = log.scrollTop;
    this.#showJump(false);
  }

  /** Brings the end into view and follows it again. */
  scrollToEnd(): void {
    const log = this.log;
    if (!log) return;
    this.#following = true;
    this.#continued = false;
    this.#reply = null;
    log.scrollTop = log.scrollHeight;
    this.#lastTop = log.scrollTop;
    this.#showJump(false);
  }

  #behind(log: HTMLElement): number {
    return log.scrollHeight - log.scrollTop - log.clientHeight;
  }

  /** Follows the end until the reply's start reaches the top. */
  #follow = (): void => {
    const log = this.log;
    if (!log) return;
    if (this.#following) {
      const behind = this.#behind(log);
      // How far the streaming reply's start is below the top edge now.
      const room = this.#reply?.isConnected
        ? this.#reply.getBoundingClientRect().top - log.getBoundingClientRect().top
        : Number.POSITIVE_INFINITY;
      if (room < behind) this.#release(log, Math.max(0, room));
      else if (behind > 0) log.scrollTop += behind;
    }
    this.#lastTop = log.scrollTop;
    this.#showJump(this.#behind(log) > AT_END_PX);
  };

  /** Scrolls by `by` to put the reply's start at the top, and stops following. */
  #release(log: HTMLElement, by: number): void {
    log.scrollTop += by;
    this.#following = false;
    this.#continued = true;
    this.#reply = null;
  }

  #onScroll = (): void => {
    const log = this.log;
    if (!log) return;
    const top = log.scrollTop;
    // A position this element set is not the reader's doing.
    if (Math.abs(top - this.#lastTop) <= 1) return;
    const away = top < this.#lastTop;
    this.#lastTop = top;
    if (this.#behind(log) <= AT_END_PX) {
      // Back at the end by their own hand: follow it to the end this time.
      this.#following = true;
      this.#continued = false;
      this.#reply = null;
      this.#showJump(false);
    } else if (away && performance.now() - this.#touchedAt < READER_INTENT_MS) {
      // Only the reader's own move away from the end lets go of it.
      this.#following = false;
      this.#continued = false;
      this.#reply = null;
      this.#showJump(true);
    }
  };

  #onReader = (): void => {
    this.#touchedAt = performance.now();
  };

  #showJump(show: boolean): void {
    const jump = this.#part('jump-wrap');
    if (jump && jump.hidden === show) jump.hidden = !show;
    const button = this.#part('jump');
    const label = this.#part('jump-label');
    if (!button || !label) return;
    const continued = show && this.#continued;
    label.textContent = label.dataset[continued ? 'continued' : 'latest'] ?? label.textContent;
    button.setAttribute(
      'aria-label',
      button.dataset[continued ? 'continuedLabel' : 'latestLabel'] ??
        button.getAttribute('aria-label') ??
        '',
    );
  }

  /** A tall last reply opens at its beginning; a short one opens at the end. */
  #positionOpenedConversation(): void {
    const log = this.log;
    const messages = this.list?.querySelectorAll<HTMLElement>('[data-message]');
    const last = messages?.item(messages.length - 1) ?? null;
    if (!log || !last) {
      this.scrollToEnd();
      return;
    }
    if (last.getBoundingClientRect().height <= log.clientHeight) {
      this.scrollToEnd();
      return;
    }
    const by =
      last.getBoundingClientRect().top - log.getBoundingClientRect().top - this.#topInset(last);
    log.scrollTop += by;
    this.#following = false;
    this.#continued = false;
    this.#lastTop = log.scrollTop;
    this.#showJump(true);
  }

  #topInset(message: HTMLElement): number {
    const inner = message.closest<HTMLElement>('.transcript-inner');
    return inner ? Number.parseFloat(getComputedStyle(inner).paddingBlockStart) || 0 : 0;
  }

  /* ---------------------------------------------------------- wiring -- */

  protected override hydrate(): void {
    const log = this.log;
    if (log && getComputedStyle(log).flexDirection === 'column-reverse') {
      // Forwards from here on, still at the end: in one task, so no frame
      // shows the column in between.
      this.toggleAttribute('data-forward', true);
      log.scrollTop = log.scrollHeight;
    }
    this.#syncLive();
  }

  protected override connected(signal: AbortSignal): void {
    const log = this.log;
    const list = this.list;
    if (log && list) {
      this.#lastTop = log.scrollTop;
      log.addEventListener('scroll', this.#onScroll, { passive: true, signal });
      for (const name of READER_EVENTS) {
        log.addEventListener(name, this.#onReader, { passive: true, signal });
      }
      // After layout, before paint: sizes are known, and the scroll is
      // written without forcing another layout.
      this.#resize = new ResizeObserver(this.#follow);
      this.#resize.observe(list);
      this.#resize.observe(log);
    }

    this.form?.addEventListener('submit', this.#onSubmit, { signal });
    this.field?.addEventListener('keydown', this.#onKeydown, { signal });
    this.#part('stop')?.addEventListener('click', () => this.emit('timlee-chat-stop'), { signal });
    this.#part('jump')?.addEventListener(
      'click',
      () => {
        this.scrollToEnd();
        this.field?.focus({ preventScroll: true });
      },
      { signal },
    );
  }

  protected override disconnected(): void {
    this.#resize?.disconnect();
    this.#resize = null;
  }

  #onSubmit = (event: SubmitEvent): void => {
    if (this.#phase !== 'idle') {
      event.preventDefault();
      return;
    }
    const text = this.value.trim();
    if (!text) {
      event.preventDefault();
      this.field?.focus();
      return;
    }
    if (!this.emit<ChatSendDetail>('timlee-chat-send', { text })) event.preventDefault();
  };

  #onKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'Enter' || event.shiftKey || event.altKey) return;
    if (!this.enterSends && !event.metaKey && !event.ctrlKey) return;
    // No newline either way: this Enter was meant to send, or to confirm.
    event.preventDefault();
    if (event.isComposing || event.keyCode === 229) return;
    if (this.#phase !== 'idle') return;
    // Enter does what the Send button would; a disabled one says why.
    const send = this.#part('send');
    if (send instanceof HTMLButtonElement && send.disabled) return;
    this.form?.requestSubmit(send ?? undefined);
  };
}

define('timlee-chat', TimleeChat);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-chat': TimleeChat;
  }
  interface HTMLElementEventMap {
    'timlee-chat-send': CustomEvent<ChatSendDetail>;
    'timlee-chat-stop': CustomEvent<undefined>;
  }
}
