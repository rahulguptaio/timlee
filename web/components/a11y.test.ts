/**
 * a11y.test.ts — axe over every component, in both themes.
 *
 * Each fixture is written the way a Go template would write it, so a
 * violation here is a violation a real page would ship. The rule set is
 * WCAG 2.0/2.1/2.2 A and AA plus axe's best-practice set.
 */

import axe, { type AxeResults, type Result, type RunOptions } from 'axe-core';
import { afterEach, describe, expect, it } from 'vitest';

import { cleanup, mount, settle } from './test-utils';

const RUN_OPTIONS: RunOptions = {
  runOnly: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'],
  // Scoped to a fixture, so page-level landmark and heading-order rules would
  // be reporting on the harness rather than on the component.
  rules: {
    region: { enabled: false },
    'page-has-heading-one': { enabled: false },
    'landmark-one-main': { enabled: false },
    'heading-order': { enabled: false },
  },
};

function describeViolations(results: AxeResults): string {
  return results.violations
    .map(
      (violation: Result) =>
        `${violation.id} (${violation.impact}): ${violation.help}\n` +
        violation.nodes.map((node) => `    ${node.html}\n    ${node.failureSummary}`).join('\n'),
    )
    .join('\n\n');
}

/**
 * Wait for entry animations to finish.
 *
 * Several components fade in. Sampling colour contrast halfway through a fade
 * measures a blend the user is never asked to read, so the audit waits for
 * every finite animation to settle. Infinite ones — the spinner's rotation,
 * the skeleton's shimmer — are skipped, since `finished` never resolves.
 */
async function animationsSettled(root: Element): Promise<void> {
  const running = [...root.getAnimations({ subtree: true }), ...document.getAnimations()].filter(
    (animation) => (animation.effect?.getTiming().iterations ?? 1) !== Number.POSITIVE_INFINITY,
  );
  await Promise.allSettled(running.map((animation) => animation.finished));
}

async function auditElement(target: Element): Promise<void> {
  await settle();
  await animationsSettled(target);
  const results = await axe.run(target, RUN_OPTIONS);
  expect(describeViolations(results)).toBe('');
}

async function audit(html: string): Promise<void> {
  await auditElement(mount(`<main>${html}</main>`));
}

/** Every component, in the markup a server would emit. */
const FIXTURES: Record<string, string> = {
  'timlee-button': `
    <timlee-button variant="primary"><button type="button">Create key</button></timlee-button>
    <timlee-button variant="secondary"><button type="button">Cancel</button></timlee-button>
    <timlee-button variant="danger"><button type="button" disabled>Revoke</button></timlee-button>
    <timlee-button variant="ghost"><button type="button">More</button></timlee-button>
    <timlee-button variant="link"><a href="/docs">Read the docs</a></timlee-button>
    <timlee-button loading><button type="button">Saving</button></timlee-button>
  `,

  'timlee-icon-button': `
    <timlee-icon-button><button type="button" aria-label="Copy key"><svg width="16" height="16" aria-hidden="true" focusable="false"><circle cx="8" cy="8" r="6" fill="currentColor"></circle></svg></button></timlee-icon-button>
    <timlee-icon-button variant="danger"><button type="button" title="Delete key"><svg width="16" height="16" aria-hidden="true" focusable="false"><rect width="12" height="12" x="2" y="2" fill="currentColor"></rect></svg></button></timlee-icon-button>
  `,

  'timlee-input': `
    <timlee-input>
      <label for="a11y-email">Work email</label>
      <input id="a11y-email" name="email" type="email" required>
      <p data-hint>Billing receipts go here.</p>
      <p data-error></p>
    </timlee-input>
    <timlee-input invalid>
      <label for="a11y-name">Key name</label>
      <input id="a11y-name" name="name" value="prod">
      <p data-error>That name is already taken.</p>
    </timlee-input>
    <timlee-input>
      <label for="a11y-disabled">Account id</label>
      <input id="a11y-disabled" value="acct_123" disabled>
    </timlee-input>
  `,

  'timlee-textarea': `
    <timlee-textarea counter autosize>
      <label for="a11y-notes">Notes</label>
      <textarea id="a11y-notes" name="notes" maxlength="280" rows="3"></textarea>
      <p data-hint>Visible to your team only.</p>
    </timlee-textarea>
  `,

  'timlee-select': `
    <timlee-select>
      <label for="a11y-env">Environment</label>
      <select id="a11y-env" name="env">
        <option value="prod">Production</option>
        <option value="staging">Staging</option>
      </select>
      <p data-hint>Determines which base URL the key works against.</p>
    </timlee-select>
  `,

  'timlee-checkbox / timlee-radio': `
    <timlee-checkbox>
      <label><input type="checkbox" name="tos" value="1"><span>I accept the terms</span></label>
      <p data-hint>You can withdraw consent later.</p>
    </timlee-checkbox>
    <timlee-checkbox indeterminate>
      <label><input type="checkbox" name="all"><span>Select all keys</span></label>
    </timlee-checkbox>
    <fieldset>
      <legend>Deployment</legend>
      <timlee-radio boxed><label><input type="radio" name="mode" value="hosted" checked><span>Timlee hosted</span></label></timlee-radio>
      <timlee-radio boxed><label><input type="radio" name="mode" value="byoc"><span>Your cloud</span></label></timlee-radio>
    </fieldset>
  `,

  'timlee-switch': `
    <timlee-switch>
      <label><input type="checkbox" role="switch" name="streaming" checked><span>Stream responses</span></label>
    </timlee-switch>
    <timlee-switch name="telemetry" value="on">Share anonymous telemetry</timlee-switch>
    <timlee-switch name="locked" disabled>Locked by your administrator</timlee-switch>
  `,

  'timlee-card': `
    <timlee-card interactive>
      <header><h3><a href="/keys/42" data-card-link>Production key</a></h3><timlee-badge tone="success" dot>Active</timlee-badge></header>
      <p>Created 3 days ago. 4,812 requests.</p>
      <footer><timlee-button size="sm"><button type="button">Rotate</button></timlee-button></footer>
    </timlee-card>
    <timlee-card elevation="flat" tone="danger"><p>This key was revoked.</p></timlee-card>
  `,

  'timlee-badge': `
    <timlee-badge>Neutral</timlee-badge>
    <timlee-badge tone="success" dot>Active</timlee-badge>
    <timlee-badge tone="warning" dot>Degraded</timlee-badge>
    <timlee-badge tone="danger" dot>Revoked</timlee-badge>
    <timlee-badge tone="info">Beta</timlee-badge>
    <timlee-badge tone="primary">Hosted</timlee-badge>
    <timlee-badge tone="byoc">BYOC</timlee-badge>
    <timlee-badge variant="outline">Outline</timlee-badge>
    <timlee-badge removable value="m1">gpt-4o-mini</timlee-badge>
  `,

  'timlee-alert': `
    <timlee-alert><strong>Nothing to report.</strong></timlee-alert>
    <timlee-alert tone="info"><strong>New region available.</strong><p>eu-central-1 is now accepting traffic.</p></timlee-alert>
    <timlee-alert tone="success"><strong>Key rotated.</strong></timlee-alert>
    <timlee-alert tone="warning" dismissible><strong>Approaching your quota.</strong><p>92% of this month's tokens used.</p></timlee-alert>
    <timlee-alert tone="danger"><strong>Upstream timeout.</strong><p><a href="/status">Check status</a></p></timlee-alert>
  `,

  'timlee-avatar': `
    <timlee-avatar name="Rahul Kumar" status="online"></timlee-avatar>
    <timlee-avatar size="sm" tone="neutral" name="Timlee Ops"></timlee-avatar>
    <timlee-avatar-group aria-label="Workspace members">
      <timlee-avatar name="Ada Lovelace"></timlee-avatar>
      <timlee-avatar name="Grace Hopper"></timlee-avatar>
    </timlee-avatar-group>
  `,

  'timlee-spinner / timlee-progress': `
    <timlee-spinner label="Loading models"></timlee-spinner>
    <timlee-spinner size="lg" label="Rotating key"></timlee-spinner>
    <timlee-progress show-value>
      <label for="a11y-upload">Uploading dataset</label>
      <progress id="a11y-upload" value="40" max="100"></progress>
    </timlee-progress>
    <timlee-progress tone="danger" label="Quota used"><progress value="92" max="100"></progress></timlee-progress>
  `,

  'timlee-empty-state / timlee-skeleton': `
    <timlee-empty-state>
      <h2>No API keys</h2>
      <p>Create a key to start sending requests.</p>
      <timlee-button variant="primary"><button type="button">Create key</button></timlee-button>
    </timlee-empty-state>
    <div aria-busy="true"><timlee-skeleton lines="3"></timlee-skeleton></div>
  `,

  'timlee-breadcrumb / timlee-pagination': `
    <timlee-breadcrumb>
      <nav><ol>
        <li><a href="/">Dashboard</a></li>
        <li><a href="/keys">API keys</a></li>
        <li><a href="/keys/42">Production</a></li>
      </ol></nav>
    </timlee-breadcrumb>
    <timlee-pagination>
      <nav>
        <a href="?page=1" rel="prev">Previous</a>
        <a href="?page=1">1</a>
        <a href="?page=2" aria-current="page">2</a>
        <span data-ellipsis>&hellip;</span>
        <a href="?page=9">9</a>
        <a href="?page=3" rel="next">Next</a>
      </nav>
    </timlee-pagination>
  `,

  'timlee-table': `
    <timlee-table label="API keys" sortable hoverable sticky>
      <table>
        <caption>Keys issued this month</caption>
        <thead><tr>
          <th scope="col" data-sort="text">Name</th>
          <th scope="col" data-sort="number">Requests</th>
          <th scope="col">Status</th>
        </tr></thead>
        <tbody>
          <tr><td>production</td><td data-sort-value="4812">4,812</td><td><timlee-badge tone="success" dot>Active</timlee-badge></td></tr>
          <tr><td>staging</td><td data-sort-value="90">90</td><td><timlee-badge tone="warning" dot>Idle</timlee-badge></td></tr>
        </tbody>
      </table>
    </timlee-table>
  `,

  'timlee-code-block': `
    <timlee-code-block label="curl example" language="bash">
      <pre><code>curl https://api.timlee.dev/v1/chat/completions -H "Authorization: Bearer $TIMLEE_API_KEY"</code></pre>
    </timlee-code-block>
  `,

  'timlee-tabs': `
    <timlee-tabs>
      <nav data-tablist aria-label="Key details">
        <a href="#a11y-usage">Usage</a>
        <a href="#a11y-limits">Limits</a>
      </nav>
      <section id="a11y-usage" data-tabpanel><p>4,812 requests today.</p></section>
      <section id="a11y-limits" data-tabpanel><p>60 requests per minute.</p></section>
    </timlee-tabs>
  `,

  'timlee-tooltip': `
    <timlee-tooltip>
      <timlee-button variant="ghost" size="sm"><button type="button">Rate limit</button></timlee-button>
      <span role="tooltip">Requests per minute, per API key.</span>
    </timlee-tooltip>
    <timlee-tooltip placement="bottom">
      <abbr>RPM</abbr>
      <span role="tooltip">Requests per minute</span>
    </timlee-tooltip>
  `,

  'timlee-toast': `
    <timlee-toaster>
      <timlee-toast tone="success">API key created.</timlee-toast>
      <timlee-toast tone="danger">Upstream timeout. <a href="/status">Check status</a></timlee-toast>
    </timlee-toaster>
  `,
};

afterEach(cleanup);

for (const theme of ['light', 'dark'] as const) {
  describe(`axe — ${theme} theme`, () => {
    afterEach(() => {
      document.documentElement.removeAttribute('data-theme');
    });

    for (const [name, html] of Object.entries(FIXTURES)) {
      it(`${name} has no violations`, async () => {
        document.documentElement.dataset.theme = theme;
        await audit(html);
      });
    }
  });
}

describe('axe — stateful variants', () => {
  afterEach(() => {
    document.documentElement.removeAttribute('data-theme');
  });

  it('an open modal dialog has no violations', async () => {
    const container = mount(`
      <main>
        <timlee-dialog id="a11y-dialog" dismissible>
          <dialog aria-labelledby="a11y-dialog-title">
            <form method="dialog">
              <h2 id="a11y-dialog-title">Revoke this key?</h2>
              <p>Requests using it will start failing within 30 seconds.</p>
              <menu>
                <timlee-button variant="secondary"><button value="cancel">Cancel</button></timlee-button>
                <timlee-button variant="danger"><button value="revoke">Revoke</button></timlee-button>
              </menu>
            </form>
          </dialog>
        </timlee-dialog>
      </main>
    `);
    const host = container.querySelector('timlee-dialog');
    (host as HTMLElement & { show(): void }).show();

    await auditElement(container.querySelector('dialog') as Element);
    (host as HTMLElement & { close(): void }).close();
  });

  it('a selected tab panel has no violations', async () => {
    const container = mount(`<main>${FIXTURES['timlee-tabs']}</main>`);
    await settle();
    const tabs = container.querySelectorAll<HTMLElement>('[role="tab"]');
    tabs[1]?.click();
    await auditElement(container);
  });

  it('an invalid field with a visible error has no violations', async () => {
    await audit(`
      <form>
        <timlee-input invalid>
          <label for="a11y-invalid">Key name</label>
          <input id="a11y-invalid" name="name" value="prod" aria-invalid="true">
          <p data-error>That name is already taken.</p>
        </timlee-input>
        <timlee-button variant="primary"><button type="submit">Save</button></timlee-button>
      </form>
    `);
  });

  it('a scrollable table is reachable by keyboard', async () => {
    await auditElement(mount(`<main style="inline-size:200px">${FIXTURES['timlee-table']}</main>`));
  });
});
