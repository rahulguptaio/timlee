// Writes intl-golden.json: what the browser's Intl prints for a fixed set of
// inputs in every formatting locale Timlee ships. internal/i18n's tests
// require its own output to match it byte for byte, so a value formatted by
// the server and later re-formatted by an island never changes on screen.
//
// Development tooling only; nothing runs it in production or CI. Regenerate
// after changing the inputs or the pinned CLDR release (internal/i18n/cldr):
//
//   node internal/i18n/testdata/intl-golden.mjs
//
// Use a Node whose ICU carries the same CLDR release as internal/i18n/cldr;
// the file records the versions it was produced with.
import { createHash } from 'node:crypto';
import { writeFileSync } from 'node:fs';

const locales = ['en-GB', 'de', 'fr', 'ja', 'ar-u-nu-latn'];
const categories = ['zero', 'one', 'two', 'few', 'many', 'other'];

const ints = [
  '0', '1', '-1', '7', '12', '999', '1000', '1234', '-1234', '9999', '10000', '12345', '123456',
  '1234567', '-1234567', '1000000', '123456789', '1234567891', '9007199254740993',
  '9223372036854775807', '-9223372036854775808',
];

const decimals = [
  ['0', 3], ['-0', 2], ['1', 0], ['1.5', 0], ['2.5', 0], ['-2.5', 0], ['1.005', 2], ['1.0049', 2],
  ['1234567.891', 3], ['1234567.891', 1], ['0.0001', 3], ['0.0005', 3], ['-0.0001', 2], ['0.1', 3],
  ['0.30000000000000004', 3], ['999.9996', 3], ['999999.95', 1], ['12.3456789', 6], ['1e21', 0],
  ['123456.789', 0], ['-1234.5', 1], ['5e-7', 6], ['NaN', 2], ['Infinity', 0], ['-Infinity', 0],
];

const ratios = [
  '0', '0.42', '0.425', '0.005', '0.0049', '1', '-0.42', '12.3456', '0.999', '0.9995', '1234.5', 'NaN',
];

const compacts = [
  '0', '5', '-5', '999', '1000', '1234', '-1234', '1500', '9949', '9950', '9999', '10000', '12345',
  '99499', '99500', '99999', '100000', '123456', '999499', '999500', '999999', '1000000', '1234567',
  '-1234567', '12345678', '123456789', '999999999', '1234567891', '12345678912', '123456789123',
  '999999999999', '1234567891234', '1000000000000000', '123456789012345678', '9223372036854775807',
];

const cents = [
  ['0', false], ['0', true], ['1', false], ['50', true], ['149', true], ['150', true], ['-150', true],
  ['99', false], ['123450', false], ['123450', true], ['-123450', false], ['-123499', true],
  ['100000000', false], ['922337203685477580', false],
];

const instants = [
  Date.UTC(2026, 9, 29, 14, 5), Date.UTC(2026, 8, 3, 9, 7), Date.UTC(2026, 0, 1, 0, 0),
  Date.UTC(2026, 1, 28, 12, 0), Date.UTC(2026, 2, 9, 12, 30), Date.UTC(2026, 3, 30, 23, 59),
  Date.UTC(2026, 4, 5, 1, 1), Date.UTC(2026, 5, 15, 11, 59), Date.UTC(2026, 6, 4, 13, 0),
  Date.UTC(2026, 7, 31, 0, 30), Date.UTC(2026, 10, 11, 18, 45), Date.UTC(2026, 11, 31, 22, 10),
  Date.UTC(1999, 11, 31, 23, 59), Date.UTC(2100, 1, 3, 4, 5), Date.UTC(1970, 0, 1, 0, 0),
];

const relatives = [];
for (const unit of ['second', 'minute', 'hour', 'day']) {
  for (const value of [0, 1, 2, 3, 5, 10, 11, 21, 59, 100, 101, 1234]) {
    relatives.push([value, unit, false], [value, unit, true]);
  }
}

const units = [];
for (const unit of ['millisecond', 'second', 'minute']) {
  for (const value of ['0', '1', '2', '3', '11', '100', '212', '1234', '0.5', '1.5', '2.25', '59.9']) {
    units.push([value, unit]);
  }
}

const pluralDecimals = [
  '0.0', '0.00', '1.0', '1.00', '1.5', '2.0', '2.5', '3.0', '10.0', '11.0', '99.5', '100.0',
  '101.0', '103.0', '111.0', '1000000.0', '0.1', '0.5', '1.25',
];

const utc = { timeZone: 'UTC' };
const out = {
  node: process.version,
  icu: process.versions.icu,
  cldr: process.versions.cldr,
  locales: {},
};

for (const locale of locales) {
  const number = new Intl.NumberFormat(locale);
  const percent = new Intl.NumberFormat(locale, { style: 'percent' });
  const compact = new Intl.NumberFormat(locale, { notation: 'compact' });
  const usd = (whole) =>
    new Intl.NumberFormat(locale, {
      style: 'currency',
      currency: 'USD',
      currencyDisplay: 'narrowSymbol',
      ...(whole ? { minimumFractionDigits: 0, maximumFractionDigits: 0 } : {}),
    });
  const date = new Intl.DateTimeFormat(locale, { ...utc, dateStyle: 'medium' });
  const monthDay = new Intl.DateTimeFormat(locale, { ...utc, month: 'short', day: 'numeric' });
  const time = new Intl.DateTimeFormat(locale, { ...utc, timeStyle: 'short' });
  const dateTime = new Intl.DateTimeFormat(locale, { ...utc, dateStyle: 'medium', timeStyle: 'short' });
  const monthDayTime = new Intl.DateTimeFormat(locale, {
    ...utc,
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
  const relative = new Intl.RelativeTimeFormat(locale);
  const plural = new Intl.PluralRules(locale);

  const all = Buffer.alloc(1_000_001);
  for (let n = 0; n <= 1_000_000; n++) all[n] = categories.indexOf(plural.select(n));

  out.locales[locale] = {
    int: ints.map((n) => [n, number.format(BigInt(n))]),
    decimal: decimals.map(([v, frac]) => [
      v,
      frac,
      new Intl.NumberFormat(locale, { maximumFractionDigits: frac }).format(Number(v)),
    ]),
    percent: ratios.map((v) => [v, percent.format(Number(v))]),
    compact: compacts.map((n) => [n, compact.format(BigInt(n))]),
    usd: cents.map(([c, whole]) => [c, whole, usd(whole).format(dollars(c))]),
    date: instants.map((t) => [t, date.format(t)]),
    monthDay: instants.map((t) => [t, monthDay.format(t)]),
    time: instants.map((t) => [t, time.format(t)]),
    dateTime: instants.map((t) => [t, dateTime.format(t)]),
    monthDayTime: instants.map((t) => [t, monthDayTime.format(t)]),
    relative: relatives.map(([v, unit, past]) => [v, unit, past, relative.format(past ? -v : v, unit)]),
    unit: units.map(([v, unit]) => [
      v,
      unit,
      new Intl.NumberFormat(locale, { style: 'unit', unit }).format(Number(v)),
    ]),
    pluralFirst: Array.from(all.subarray(0, 1001)).join(''),
    pluralDigest: createHash('sha256').update(all).digest('hex'),
    pluralDecimals: pluralDecimals.map((v) => {
      const frac = v.split('.')[1].length;
      const rules = new Intl.PluralRules(locale, {
        minimumFractionDigits: frac,
        maximumFractionDigits: frac,
      });
      return [v, rules.select(Number(v))];
    }),
  };
}

// dollars writes an amount in cents as an exact decimal string, which Intl
// formats without a detour through binary floating point.
function dollars(c) {
  const neg = c.startsWith('-');
  const digits = (neg ? c.slice(1) : c).padStart(3, '0');
  return `${neg ? '-' : ''}${digits.slice(0, -2)}.${digits.slice(-2)}`;
}

// Invisible characters (no-break spaces, bidi marks and isolates) are
// written as \u escapes, so the file reads and diffs as what it holds.
const json = (v) =>
  JSON.stringify(v).replace(
    /[\u00a0\u061c\u200b-\u200f\u202a-\u202f\u2066-\u2069]/g,
    (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`,
  );

// One entry per line, so a regenerated file diffs by the values that moved.
const lines = ['{'];
lines.push(`  "node": ${JSON.stringify(out.node)},`);
lines.push(`  "icu": ${JSON.stringify(out.icu)},`);
lines.push(`  "cldr": ${JSON.stringify(out.cldr)},`);
lines.push('  "locales": {');
locales.forEach((locale, i) => {
  lines.push(`    ${JSON.stringify(locale)}: {`);
  const entries = Object.entries(out.locales[locale]);
  entries.forEach(([key, value], j) => {
    const comma = j < entries.length - 1 ? ',' : '';
    if (Array.isArray(value)) {
      lines.push(`      ${JSON.stringify(key)}: [`);
      value.forEach((row, k) => {
        lines.push(`        ${json(row)}${k < value.length - 1 ? ',' : ''}`);
      });
      lines.push(`      ]${comma}`);
    } else {
      lines.push(`      ${JSON.stringify(key)}: ${json(value)}${comma}`);
    }
  });
  lines.push(`    }${i < locales.length - 1 ? ',' : ''}`);
});
lines.push('  }', '}', '');

const file = new URL('intl-golden.json', import.meta.url);
writeFileSync(file, lines.join('\n'));
