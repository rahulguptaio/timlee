/**
 * measure.mjs — report the shipped size of the component library.
 *
 * Bundles `web/components/index.ts` and `components.css` exactly as
 * `cmd/timlee-build` does (esbuild, ESM, es2022, full minification, @imports
 * inlined) and compresses them with the same brotli settings the Go pipeline
 * uses, so the numbers here are the numbers the browser downloads.
 *
 *   node web/components/scripts/measure.mjs
 *   node web/components/scripts/measure.mjs --json
 */

import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { brotliCompress, constants, gzip } from 'node:zlib';
import { build } from 'esbuild';

const brotli = promisify(brotliCompress);
const gz = promisify(gzip);

const here = dirname(fileURLToPath(import.meta.url));
const componentsDir = resolve(here, '..');

async function bundle(entry) {
  const result = await build({
    entryPoints: [entry],
    bundle: true,
    format: 'esm',
    platform: 'browser',
    target: 'es2022',
    minify: true,
    treeShaking: true,
    charset: 'utf8',
    legalComments: 'none',
    define: { __DEV__: 'false' },
    // The same inline tsconfig cmd/timlee-build passes to esbuild, so the
    // class-field semantics here match production exactly.
    tsconfigRaw: '{"compilerOptions":{"useDefineForClassFields":true,"target":"es2022"}}',
    write: false,
    logLevel: 'silent',
  });
  return Buffer.from(result.outputFiles[0].contents);
}

async function sizes(label, bytes) {
  const [brotlied, gzipped] = await Promise.all([
    // Quality 11 and a window large enough for the whole file, matching
    // brotli.BestCompression in cmd/timlee-build.
    brotli(bytes, {
      params: {
        [constants.BROTLI_PARAM_QUALITY]: 11,
        [constants.BROTLI_PARAM_SIZE_HINT]: bytes.length,
      },
    }),
    gz(bytes, { level: 9 }),
  ]);
  return { label, raw: bytes.length, brotli: brotlied.length, gzip: gzipped.length };
}

const kb = (n) => `${(n / 1024).toFixed(1)} kB`;

async function main() {
  const js = await bundle(join(componentsDir, 'index.ts'));
  const css = await bundle(join(componentsDir, 'components.css'));

  const rows = [
    await sizes('JavaScript (index.ts, minified)', js),
    await sizes('CSS (components.css)', css),
    await sizes('Total', Buffer.concat([js, css])),
  ];

  if (process.argv.includes('--json')) {
    console.log(JSON.stringify(rows, null, 2));
    return;
  }

  const width = Math.max(...rows.map((r) => r.label.length));
  console.log(
    `${'asset'.padEnd(width)}  ${'raw'.padStart(9)}  ${'gzip'.padStart(9)}  ${'brotli'.padStart(9)}`,
  );
  console.log('-'.repeat(width + 33));
  for (const row of rows) {
    console.log(
      `${row.label.padEnd(width)}  ${kb(row.raw).padStart(9)}  ${kb(row.gzip).padStart(9)}  ${kb(row.brotli).padStart(9)}`,
    );
  }
}

await main();
