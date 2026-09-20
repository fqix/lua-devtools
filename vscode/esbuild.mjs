#!/usr/bin/env node
// Bundle the extension (and vscode-languageclient) into dist/extension.js.
//   node esbuild.mjs            # one-off build
//   node esbuild.mjs --watch    # rebuild on change
import { build, context } from 'esbuild';

const options = {
  entryPoints: ['src/extension.ts'],
  bundle: true,
  outfile: 'dist/extension.js',
  platform: 'node',
  target: 'node20',
  format: 'cjs',
  external: ['vscode'],
  sourcemap: true,
  minify: false,
  logLevel: 'info',
};

if (process.argv.includes('--watch')) {
  const ctx = await context(options);
  await ctx.watch();
} else {
  await build(options);
}
