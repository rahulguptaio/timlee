/**
 * editor.ts — the CodeMirror 6 editor used by the workspace's code tab.
 *
 * Loaded on demand, so pages without the code tab never download it. Colors
 * come from the site's design tokens, so the editor follows light and dark
 * mode with the rest of the page.
 */

import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { css } from '@codemirror/lang-css';
import { html } from '@codemirror/lang-html';
import { javascript } from '@codemirror/lang-javascript';
import { json } from '@codemirror/lang-json';
import {
  bracketMatching,
  foldGutter,
  HighlightStyle,
  indentOnInput,
  syntaxHighlighting,
} from '@codemirror/language';
import { highlightSelectionMatches, searchKeymap } from '@codemirror/search';
import { EditorState, type Extension } from '@codemirror/state';
import {
  drawSelection,
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
} from '@codemirror/view';
import { tags } from '@lezer/highlight';

export type Editor = {
  getValue(): string;
  setValue(value: string): void;
};

function language(path: string): Extension {
  const ext = path.slice(path.lastIndexOf('.') + 1).toLowerCase();
  switch (ext) {
    case 'js':
    case 'mjs':
    case 'cjs':
      return javascript();
    case 'ts':
      return javascript({ typescript: true });
    case 'css':
      return css();
    case 'json':
      return json();
    case 'html':
    case 'svelte':
      // Svelte components are HTML with <script> and <style> blocks; the HTML
      // mode highlights all three.
      return html();
    default:
      return [];
  }
}

const theme = EditorView.theme({
  '&': {
    height: '100%',
    fontSize: '0.875rem',
    color: 'var(--ts-text-1, CanvasText)',
    backgroundColor: 'var(--ts-surface-1, Canvas)',
  },
  '.cm-scroller': { fontFamily: 'var(--ts-font-mono, ui-monospace, monospace)', lineHeight: '1.6' },
  '.cm-gutters': {
    backgroundColor: 'var(--ts-surface-inset, Canvas)',
    color: 'var(--ts-text-3, GrayText)',
    borderRight: '1px solid var(--ts-border, GrayText)',
  },
  '&.cm-focused': { outline: '2px solid var(--ts-ring, Highlight)', outlineOffset: '-2px' },
  // CodeMirror's own dark rules never apply (colors come from tokens), so
  // the caret, selection and panels are themed here for both schemes.
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--ts-text-1, CanvasText)' },
  '.cm-selectionBackground, &.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground':
    { backgroundColor: 'color-mix(in oklab, var(--ts-primary, Highlight) 28%, transparent)' },
  '.cm-panels, .cm-tooltip': {
    backgroundColor: 'var(--ts-surface-raised, Canvas)',
    color: 'var(--ts-text-1, CanvasText)',
    borderColor: 'var(--ts-border, GrayText)',
  },
  '.cm-panels input, .cm-panels button': { color: 'inherit' },
  '.cm-activeLine, .cm-activeLineGutter': {
    backgroundColor: 'color-mix(in oklab, var(--ts-text-1, CanvasText) 5%, transparent)',
  },
});

// Ink tokens hold 4.5:1 on every surface in both themes (tokens.test.ts), so
// highlighting stays readable in light and dark without a second palette.
const highlight = HighlightStyle.define([
  { tag: [tags.keyword, tags.controlKeyword, tags.moduleKeyword], color: 'var(--ts-primary)' },
  { tag: [tags.string, tags.special(tags.string), tags.regexp], color: 'var(--ts-success-ink)' },
  { tag: [tags.number, tags.bool, tags.null, tags.atom], color: 'var(--ts-warning-ink)' },
  {
    tag: [tags.comment, tags.lineComment, tags.blockComment],
    color: 'var(--ts-text-3)',
    fontStyle: 'italic',
  },
  { tag: [tags.tagName, tags.typeName, tags.className], color: 'var(--ts-info-ink)' },
  { tag: [tags.attributeName, tags.propertyName], color: 'var(--ts-text-2)' },
  {
    tag: [tags.function(tags.variableName), tags.definition(tags.variableName)],
    color: 'var(--ts-text-heading)',
  },
  { tag: tags.invalid, color: 'var(--ts-danger-ink)' },
]);

export function createEditor(options: {
  parent: HTMLElement;
  doc: string;
  path: string;
  readOnly: boolean;
  label: string;
  describedBy: string;
  onSave: () => void;
  onChange: () => void;
}): Editor {
  const view = new EditorView({
    parent: options.parent,
    state: EditorState.create({
      doc: options.doc,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        foldGutter(),
        history(),
        drawSelection(),
        indentOnInput(),
        bracketMatching(),
        highlightActiveLine(),
        highlightSelectionMatches(),
        syntaxHighlighting(highlight),
        language(options.path),
        keymap.of([
          {
            key: 'Mod-s',
            preventDefault: true,
            run: () => {
              options.onSave();
              return true;
            },
          },
          ...defaultKeymap,
          ...historyKeymap,
          ...searchKeymap,
          // Tab indents; Escape then Tab still moves focus out (CodeMirror's
          // documented escape hatch), so keyboard users are never trapped.
          indentWithTab,
        ]),
        EditorState.readOnly.of(options.readOnly),
        EditorView.contentAttributes.of({
          'aria-label': options.label,
          ...(options.describedBy ? { 'aria-describedby': options.describedBy } : {}),
        }),
        EditorView.updateListener.of((update) => {
          if (update.docChanged) options.onChange();
        }),
        // The page's CSP has no 'unsafe-inline'; CodeMirror's injected styles
        // carry this response's nonce instead.
        EditorView.cspNonce.of(document.querySelector<HTMLElement>('[nonce]')?.nonce ?? ''),
        theme,
      ],
    }),
  });

  return {
    getValue: () => view.state.doc.toString(),
    setValue: (value) =>
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } }),
  };
}
