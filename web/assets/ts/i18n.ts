/**
 * Messages for script: the page's #timlee-i18n data block, which the server
 * renders from the reader's catalog with every message already parsed
 * (docs/I18N.md §5, internal/i18n/block.go). Script makes text only from
 * these, so what it writes is translated with the rest of the page.
 *
 * A message is an array of parts: text, ["name"] for an argument, ["#"] for
 * the innermost plural's number, or ["name", "plural" | "select", branches].
 * Numbers are formatted and plural forms chosen for the block's Intl locale,
 * and string arguments are isolated in FSI … PDI, as the server does.
 */

type Part = string | [name: string] | [name: string, kind: 'plural' | 'select', branches: Branches];
type Branches = Record<string, Part[]>;

interface Block {
  lang: string;
  intl: string;
  dir: string;
  m: Record<string, Part[]>;
}

export type Args = Record<string, string | number>;

const FSI = '\u2068';
const PDI = '\u2069';

let source: Element | null | undefined;
let block: Block = { lang: 'en', intl: 'en', dir: 'ltr', m: {} };
let numbers: Intl.NumberFormat | undefined;
let plurals: Intl.PluralRules | undefined;
let percentages: Intl.NumberFormat | undefined;
const dates = new Map<string, Intl.DateTimeFormat>();
const relatives = new Map<string, Intl.RelativeTimeFormat>();
const units = new Map<string, Intl.NumberFormat>();

/** The page's block, read again only when the page has a different one. */
function current(): Block {
  const el = document.getElementById('timlee-i18n');
  if (el !== source) {
    source = el;
    block = el
      ? (JSON.parse(el.textContent || '{}') as Block)
      : { lang: 'en', intl: 'en', dir: 'ltr', m: {} };
    numbers = undefined;
    plurals = undefined;
    percentages = undefined;
    dates.clear();
    relatives.clear();
    units.clear();
  }
  return block;
}

/** A number as the page prints it: "1,204" in English. */
export function num(value: number): string {
  numbers ??= new Intl.NumberFormat(current().intl || undefined);
  return numbers.format(value);
}

/** The Intl locale behind the page's server-rendered catalog. */
export function intlLocale(): string | undefined {
  return current().intl || undefined;
}

/** The document's declared base direction. */
export function direction(): 'ltr' | 'rtl' {
  return current().dir === 'rtl' ? 'rtl' : 'ltr';
}

/** Isolate arbitrary user, model or identifier text inside surrounding copy. */
export function isolate(value: string): string {
  return FSI + value + PDI;
}

/** A ratio as a locale-aware whole percentage. */
export function percent(value: number): string {
  percentages ??= new Intl.NumberFormat(current().intl || undefined, {
    style: 'percent',
    maximumFractionDigits: 0,
  });
  return percentages.format(value);
}

/** A date or time in the document locale and the reader's time zone. */
export function dateTime(
  value: Date | number | string,
  options: Intl.DateTimeFormatOptions,
): string {
  const key = JSON.stringify(options);
  let formatter = dates.get(key);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat(current().intl || undefined, options);
    dates.set(key, formatter);
  }
  return formatter.format(value instanceof Date ? value : new Date(value));
}

/** A relative value such as "3 days ago" in the document locale. */
export function relative(
  value: number,
  unit: Intl.RelativeTimeFormatUnit,
  options: Intl.RelativeTimeFormatOptions = { numeric: 'auto' },
): string {
  const key = JSON.stringify(options);
  let formatter = relatives.get(key);
  if (!formatter) {
    formatter = new Intl.RelativeTimeFormat(current().intl || undefined, options);
    relatives.set(key, formatter);
  }
  return formatter.format(value, unit);
}

/** A browser-supported unit such as milliseconds, seconds or minutes. */
export function unit(
  value: number,
  name: 'millisecond' | 'second' | 'minute',
  display: 'long' | 'short' | 'narrow' = 'short',
): string {
  const key = `${name}:${display}`;
  let formatter = units.get(key);
  if (!formatter) {
    formatter = new Intl.NumberFormat(current().intl || undefined, {
      style: 'unit',
      unit: name,
      unitDisplay: display,
      maximumFractionDigits: 0,
    });
    units.set(key, formatter);
  }
  return formatter.format(value);
}

function category(count: number): string {
  plurals ??= new Intl.PluralRules(current().intl || undefined);
  return plurals.select(count);
}

function write(parts: Part[], args: Args, count: number | undefined, out: string[]): void {
  for (const part of parts) {
    if (typeof part === 'string') {
      out.push(part);
      continue;
    }
    const [name, kind, branches] = part;
    if (name === '#') {
      if (count !== undefined) out.push(num(count));
      continue;
    }
    const value = args[name];
    if (kind === undefined) {
      if (typeof value === 'number') out.push(num(value));
      else out.push(value === undefined ? `{${name}}` : isolate(value));
    } else if (kind === 'plural' && branches) {
      const n = Number(value);
      write(branches[`=${n}`] ?? branches[category(n)] ?? branches.other ?? [], args, n, out);
    } else if (branches) {
      write(branches[String(value)] ?? branches.other ?? [], args, count, out);
    }
  }
}

/** The message key in the reader's language, or ⟦key⟧ when the page lacks it. */
export function t(key: string, args: Args = {}): string {
  const parts = current().m[key];
  if (!parts) return `⟦${key}⟧`;
  const out: string[] = [];
  write(parts, args, undefined, out);
  return out.join('');
}

/** A plural message whose "count" argument is count. */
export function tn(key: string, count: number, args: Args = {}): string {
  return t(key, { ...args, count });
}
