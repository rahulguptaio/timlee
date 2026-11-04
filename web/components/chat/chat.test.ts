import { afterEach, beforeAll, describe, expect, it } from 'vitest';

import { $, cleanup, mount, nextFrame, press, record } from '../test-utils';
import type { ChatSendDetail, TimleeChat } from './chat';
import './chat';
import chatCss from './chat.css?inline';

/** A transcript short enough to scroll, laid out the way the console lays it out. */
const LAYOUT = `
  .test-log { display: flex; flex-direction: column-reverse; block-size: 12rem; overflow-y: auto; }
  .test-log.forward { flex-direction: column; }
  .test-log ol { margin: 0; padding: 0; list-style: none; }
  .test-log li { min-block-size: 3rem; }
`;

beforeAll(() => {
  const style = document.createElement('style');
  style.textContent = `${chatCss}\n${LAYOUT}`;
  document.head.append(style);
});

afterEach(() => cleanup());

function markup({ enterSends = true, forward = false, messages = 2 } = {}): string {
  const history = Array.from(
    { length: messages },
    (_, i) =>
      `<li data-message="${i % 2 ? 'assistant' : 'user'}" id="msg-${i}"><div data-body><p>Message ${i}</p></div></li>`,
  ).join('');
  return `
    <script type="application/json" id="timlee-i18n">${JSON.stringify({
      lang: 'en',
      intl: 'en-GB',
      dir: 'ltr',
      m: {
        'console.composer.jump': ['Jump to latest'],
        'console.composer.jumpLabel': ['Jump to latest message'],
        'console.composer.replyContinues': ['Reply continues'],
        'console.composer.replyContinuesLabel': ['Reply continues. Jump to latest message'],
      },
    })}</script>
    <timlee-chat${enterSends ? ' enter-sends' : ''}>
      <section class="test-log${forward ? ' forward' : ''}" role="log" aria-label="Messages" tabindex="0" data-transcript>
        <div><ol data-messages>${history}</ol></div>
      </section>
      <template data-template="user"><li data-message="user"><p><span>You, </span><time></time></p><div data-body></div></li></template>
      <template data-template="assistant"><li data-message="assistant"><p><span data-name></span> <time></time></p><div data-body></div><p data-live-receipt>Generating</p></li></template>
      <form method="post" action="/apps/example/prompt" data-composer>
        <timlee-button hidden data-jump-wrap><button type="button" data-jump aria-label="Jump to latest message" data-latest-label="Jump to latest message" data-continued-label="Reply continues. Jump to latest message"><span data-jump-label data-latest="Jump to latest" data-continued="Reply continues">Jump to latest</span></button></timlee-button>
        <label for="composer-input">Message</label>
        <textarea id="composer-input" name="prompt" data-composer-input></textarea>
        <timlee-icon-button data-send-wrap><button type="submit" aria-label="Send message" data-send>↑</button></timlee-icon-button>
        <timlee-icon-button hidden data-stop-wrap><button type="button" aria-label="Stop generating" data-stop>■</button></timlee-icon-button>
      </form>
    </timlee-chat>`;
}

function setup(options?: Parameters<typeof markup>[0]) {
  const container = mount(markup(options));
  const chat = $<TimleeChat>(container, 'timlee-chat');
  const field = $<HTMLTextAreaElement>(container, '[data-composer-input]');
  const form = $<HTMLFormElement>(container, 'form');
  // Nothing may navigate the test page: a listener added after the
  // element's records what the element decided, then stops the post.
  const posted: boolean[] = [];
  form.addEventListener('submit', (event) => {
    posted.push(!event.defaultPrevented);
    event.preventDefault();
  });
  return { container, chat, field, form, posted };
}

/** Takes every message the element offers, as the console island does. */
function take(chat: TimleeChat): () => string[] {
  const sent: string[] = [];
  chat.addEventListener('timlee-chat-send', (event) => {
    event.preventDefault();
    sent.push((event as CustomEvent<ChatSendDetail>).detail.text);
  });
  return () => sent;
}

describe('<timlee-chat> composer', () => {
  it('sends on Enter and starts a new line on Shift+Enter', () => {
    const { chat, field } = setup();
    const sent = take(chat);
    field.value = '  How do I raise the hit ratio?  ';

    const newline = press(field, 'Enter', { shiftKey: true });
    expect(newline.defaultPrevented).toBe(false);
    expect(sent()).toEqual([]);

    const enter = press(field, 'Enter');
    expect(enter.defaultPrevented).toBe(true);
    expect(sent()).toEqual(['How do I raise the hit ratio?']);
  });

  it('never sends while an input method is composing', () => {
    const { chat, field } = setup();
    const sent = take(chat);
    field.value = '東京';

    press(field, 'Enter', { isComposing: true });
    // Safari delivers the confirming Enter after compositionend: 229 is its tell.
    press(field, 'Enter', { keyCode: 229 });
    expect(sent()).toEqual([]);

    press(field, 'Enter');
    expect(sent()).toEqual(['東京']);
  });

  it('leaves Enter to the field when Enter does not send, and sends on Ctrl+Enter', () => {
    const { chat, field } = setup({ enterSends: false });
    const sent = take(chat);
    field.value = 'two lines';

    expect(press(field, 'Enter').defaultPrevented).toBe(false);
    expect(sent()).toEqual([]);
    press(field, 'Enter', { ctrlKey: true });
    expect(sent()).toEqual(['two lines']);
  });

  it('sends nothing empty, and posts the form when no script takes the message', () => {
    const { chat, field, form, posted } = setup();
    field.value = '   ';
    form.requestSubmit();
    expect(posted).toEqual([false]);

    // With no listener cancelling timlee-chat-send, the form posts as it
    // would without script.
    field.value = 'hello';
    form.requestSubmit();
    expect(posted).toEqual([false, true]);

    take(chat);
    form.requestSubmit();
    expect(posted).toEqual([false, true, false]);
  });

  it('does nothing on Enter while Send is disabled, as the button would', () => {
    const { chat, container, field } = setup();
    const sent = take(chat);
    $<HTMLButtonElement>(container, '[data-send]').disabled = true;
    field.value = 'does not fit';

    expect(press(field, 'Enter').defaultPrevented).toBe(true);
    expect(sent()).toEqual([]);
    $<HTMLButtonElement>(container, '[data-send]').disabled = false;
    press(field, 'Enter');
    expect(sent()).toEqual(['does not fit']);
  });

  it('starts an empty field on the page’s side, then lets the text choose', () => {
    const { container, field } = setup();
    container.dir = 'rtl';
    field.dir = 'auto';
    field.placeholder = 'Message Timlee';

    expect(getComputedStyle(field).direction).toBe('rtl');
    field.value = 'Hello';
    expect(getComputedStyle(field).direction).toBe('ltr');
  });

  it('turns Send into Stop in place and keeps focus in the composer', async () => {
    const { chat, container, field } = setup();
    const send = $<HTMLElement>(container, '[data-send-wrap]');
    const stop = $<HTMLElement>(container, '[data-stop-wrap]');
    const stops = record(chat, 'timlee-chat-stop');

    $<HTMLButtonElement>(container, '[data-send]').focus();
    chat.setPhase('submitted');
    expect(send.hidden).toBe(true);
    expect(stop.hidden).toBe(false);
    // The focused Send went away; focus went to the field, not the page.
    expect(document.activeElement).toBe(field);

    // Enter while a reply is on its way neither sends nor adds a line.
    field.value = 'next question';
    const sent = take(chat);
    expect(press(field, 'Enter').defaultPrevented).toBe(true);
    expect(sent()).toEqual([]);

    const stopButton = $<HTMLButtonElement>(container, '[data-stop]');
    stopButton.focus();
    stopButton.click();
    expect(stops()).toHaveLength(1);
    chat.setPhase('idle');
    expect(stop.hidden).toBe(true);
    expect(send.hidden).toBe(false);
    expect(document.activeElement).toBe(field);
  });

  it('keeps the transcript silent while a turn is in flight', async () => {
    const { chat, container } = setup();
    const log = $<HTMLElement>(container, '[data-transcript]');
    expect(log.hasAttribute('aria-live')).toBe(false);

    chat.setPhase('streaming');
    expect(log.getAttribute('aria-busy')).toBe('true');
    expect(log.getAttribute('aria-live')).toBe('off');

    chat.setPhase('idle');
    expect(log.hasAttribute('aria-live')).toBe(false);
    expect(log.hasAttribute('aria-busy')).toBe(false);

    // A swap made after the turn stays unspoken until it has been taken in.
    const reply = chat.addMessage('assistant', { text: 'raw' });
    expect(reply).not.toBeNull();
    expect(log.getAttribute('aria-live')).toBe('off');
    await new Promise((resolve) => setTimeout(resolve, 600));
    expect(log.hasAttribute('aria-live')).toBe(false);
  });
});

describe('<timlee-chat> streaming', () => {
  it('writes a frame’s worth of text at once, as Text nodes', async () => {
    const { chat, container } = setup();
    const reply = chat.addMessage('assistant', { id: 'a-1', name: 'Qwen 2.5 7B Instruct' });
    if (!reply) throw new Error('no template');
    await nextFrame();

    const records: MutationRecord[] = [];
    const observer = new MutationObserver((list) => records.push(...list));
    observer.observe($(container, '[data-messages]'), {
      childList: true,
      characterData: true,
      subtree: true,
    });

    const stream = chat.stream(reply);
    for (const token of ['Prefix', ' reuse', ' is', ' the', ' lever.']) stream.push(token);
    await Promise.resolve();
    expect(records).toHaveLength(0);
    await nextFrame();
    records.push(...observer.takeRecords());

    // One block with one Text node, added once for the whole frame.
    expect(records).toHaveLength(1);
    const body = $(reply, '[data-body]');
    expect(body.innerHTML).toBe('<p data-stream="">Prefix reuse is the lever.</p>');

    for (const token of [' It', ' keeps', ' going.']) stream.push(token);
    await nextFrame();
    records.push(...observer.takeRecords());
    // The next frame appends to the same Text node.
    expect(records.at(-1)?.type).toBe('characterData');
    expect(body.querySelectorAll('p')).toHaveLength(1);
    observer.disconnect();

    expect(reply.id).toBe('msg-a-1');
    expect($(reply, '[data-name]').textContent).toBe('Qwen 2.5 7B Instruct');
  });

  it('starts a block at a blank line, wherever the chunk boundaries fall', async () => {
    const { chat } = setup();
    const reply = chat.addMessage('assistant');
    if (!reply) throw new Error('no template');
    const stream = chat.stream(reply);

    stream.push('First line\nstill first');
    stream.push(' paragraph.\n');
    await nextFrame();
    stream.push('\n- a list item\n- another\n');
    await nextFrame();
    stream.push('\n\nLast.');
    stream.flush();

    const blocks = Array.from(reply.querySelectorAll('[data-stream]'), (p) => p.textContent);
    expect(blocks).toEqual([
      'First line\nstill first paragraph.',
      '- a list item\n- another',
      'Last.',
    ]);
    expect(stream.text).toBe(
      'First line\nstill first paragraph.\n\n- a list item\n- another\n\n\nLast.',
    );
  });

  it('swaps in the server’s rendering, moving focus that was inside it', () => {
    const { chat, container } = setup();
    const reply = chat.addMessage('assistant', { text: '**bold**' });
    if (!reply) throw new Error('no template');
    reply.tabIndex = -1;
    reply.focus();

    const [swapped] = chat.replace(
      reply,
      '<li data-message="assistant" id="msg-x"><div data-body><p><strong>bold</strong></p></div><details><summary>Usage</summary></details></li>',
    );
    expect(reply.isConnected).toBe(false);
    expect(swapped?.id).toBe('msg-x');
    expect($(container, '#msg-x strong').textContent).toBe('bold');
    expect(document.activeElement?.localName).toBe('summary');
  });
});

describe('<timlee-chat> swapping a turn', () => {
  it('replaces a prompt and its reply with the server’s markup for both', () => {
    const { chat, container } = setup();
    const prompt = chat.addMessage('user', { id: 'u-1', text: 'Which **GPU**?' });
    const reply = chat.addMessage('assistant', { id: 'a-1', text: 'Use an L40S.' });
    if (!prompt || !reply) throw new Error('no template');
    reply.tabIndex = -1;
    reply.focus();

    const nodes = chat.replace(
      [prompt, reply],
      '<li data-message="user" id="msg-u-1"><div data-body><p>Which <strong>GPU</strong>?</p></div></li>' +
        '<li data-message="assistant" id="msg-a-1"><div data-body><p>Use an L40S.</p></div><details><summary>Usage</summary></details></li>',
    );
    expect(nodes.map((node) => node.id)).toEqual(['msg-u-1', 'msg-a-1']);
    expect(prompt.isConnected || reply.isConnected).toBe(false);
    const ids = Array.from(container.querySelectorAll('[data-messages] > li'), (li) => li.id);
    expect(ids.slice(-2)).toEqual(['msg-u-1', 'msg-a-1']);
    expect(ids.filter((id) => id === 'msg-u-1')).toHaveLength(1);
    expect($(container, '#msg-u-1 strong').textContent).toBe('GPU');
    // Focus that was in the swapped reply lands on the new markup's control.
    expect(document.activeElement?.localName).toBe('summary');
  });
});

describe('<timlee-chat> scrolling', () => {
  async function streamLines(chat: TimleeChat, reply: HTMLElement, lines: number) {
    const stream = chat.stream(reply);
    for (let i = 0; i < lines; i++) {
      stream.push(`line ${i}\n`);
      await nextFrame();
      await nextFrame();
    }
    return stream;
  }

  const behind = (log: HTMLElement) => log.scrollHeight - log.scrollTop - log.clientHeight;

  it('turns a reversed transcript forwards on upgrade, still at its end', () => {
    const { chat, container } = setup({ messages: 12 });
    const log = $<HTMLElement>(container, '[data-transcript]');
    expect(chat.hasAttribute('data-forward')).toBe(true);
    expect(getComputedStyle(log).flexDirection).toBe('column');
    expect(behind(log)).toBeLessThanOrEqual(1);
  });

  it('moves the scroll, not the messages, while a reply streams at the end', async () => {
    const { chat } = setup({ messages: 12 });
    const reply = chat.addMessage('assistant');
    if (!reply) throw new Error('no template');
    chat.scrollToEnd();
    const stream = chat.stream(reply);
    stream.push('first line\n');
    await nextFrame();
    await nextFrame();
    let shifted = 0;
    const observer = new PerformanceObserver((list) => {
      for (const entry of list.getEntries())
        shifted += (entry as PerformanceEntry & { value: number }).value;
    });
    observer.observe({ type: 'layout-shift' });
    // Short enough that the reply's start never reaches the top, where
    // following stops by design and what is below the text may move.
    for (let i = 0; i < 3; i++) {
      stream.push(`line ${i}\n`);
      await nextFrame();
      await nextFrame();
    }
    await nextFrame();
    observer.disconnect();
    // The line under the reply moves with each new line of text: only a
    // scroll written in the same frame as the text carries it without a
    // shift.
    expect(shifted).toBe(0);
  });

  it('adds a turn at the end without moving the messages on screen', async () => {
    const { chat } = setup({ messages: 12 });
    chat.scrollToEnd();
    await nextFrame();
    await nextFrame();
    let shifted = 0;
    const observer = new PerformanceObserver((list) => {
      for (const entry of list.getEntries())
        shifted += (entry as PerformanceEntry & { value: number }).value;
    });
    observer.observe({ type: 'layout-shift' });
    chat.addMessage('user', { text: 'One more question.' });
    chat.addMessage('assistant');
    chat.scrollToEnd();
    await nextFrame();
    await nextFrame();
    observer.disconnect();
    // Messages that leave the last few must not change size as they go,
    // as content-visibility with a placeholder height made them do.
    expect(shifted).toBe(0);
  });

  it('follows the end of a growing reply until its start reaches the top', async () => {
    const { chat, container } = setup({ messages: 8 });
    const log = $<HTMLElement>(container, '[data-transcript]');
    const reply = chat.addMessage('assistant');
    if (!reply) throw new Error('no template');
    chat.scrollToEnd();

    await streamLines(chat, reply, 3);
    expect(behind(log)).toBeLessThanOrEqual(1);

    await streamLines(chat, reply, 20);
    // The reply outgrew the view: its start stays at the top edge.
    const top = reply.getBoundingClientRect().top - log.getBoundingClientRect().top;
    expect(Math.abs(top)).toBeLessThanOrEqual(1);
    expect($<HTMLElement>(container, '[data-jump-wrap]').hidden).toBe(false);
    expect($<HTMLElement>(container, '[data-jump-label]').textContent).toBe('Reply continues');
    expect($<HTMLButtonElement>(container, '[data-jump]').ariaLabel).toBe(
      'Reply continues. Jump to latest message',
    );

    $<HTMLButtonElement>(container, '[data-jump]').click();
    expect(behind(log)).toBeLessThanOrEqual(1);
    expect(document.activeElement).toBe($(container, '[data-composer-input]'));
  });

  it('anchors a newly sent prompt at the top before following its reply', async () => {
    const { chat, container } = setup({ messages: 12 });
    const log = $<HTMLElement>(container, '[data-transcript]');
    const prompt = chat.addMessage('user', { text: 'One more question.' });
    if (!prompt) throw new Error('no prompt template');
    for (let i = 0; i < 8; i++) chat.addMessage('assistant', { text: `Reply line ${i}` });
    await nextFrame();
    await nextFrame();

    chat.beginTurn(prompt);
    await nextFrame();

    const top = prompt.getBoundingClientRect().top - log.getBoundingClientRect().top;
    expect(Math.abs(top)).toBeLessThanOrEqual(1);
  });

  it('holds still while the reader is reading higher up', async () => {
    const { chat, container } = setup({ messages: 12 });
    const log = $<HTMLElement>(container, '[data-transcript]');
    const reply = chat.addMessage('assistant');
    if (!reply) throw new Error('no template');
    await nextFrame();
    await nextFrame();

    // The reader scrolls up with the wheel.
    log.dispatchEvent(new WheelEvent('wheel', { deltaY: -200 }));
    log.scrollTop = log.scrollHeight - log.clientHeight - 200;
    await nextFrame();
    const anchor = $<HTMLElement>(container, '#msg-6');
    const before = anchor.getBoundingClientRect().top;

    await streamLines(chat, reply, 6);
    expect(anchor.getBoundingClientRect().top).toBeCloseTo(before, 0);
  });

  it('follows a column the server laid out forwards', async () => {
    const { chat, container } = setup({ messages: 8, forward: true });
    const log = $<HTMLElement>(container, '[data-transcript]');
    chat.scrollToEnd();
    const reply = chat.addMessage('assistant');
    if (!reply) throw new Error('no template');
    await streamLines(chat, reply, 2);
    expect(log.scrollHeight - log.scrollTop - log.clientHeight).toBeLessThanOrEqual(1);
  });
});
