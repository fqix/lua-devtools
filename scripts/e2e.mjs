#!/usr/bin/env node
// Build the end-to-end tests and their scratch workspace, then run them in VS Code:
//   node scripts/e2e.mjs [--build-only]
// Requires `npm run build` (vscode/dist and bin/) and Lua 5.4 with luaunit 3.4 and Busted 2.2.0.
import { build } from 'esbuild';
import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdirSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const EXT = join(ROOT, 'vscode');
const OUT = join(EXT, 'out');
const tests = readdirSync(join(EXT, 'src', 'e2e')).filter((f) => f.endsWith('.test.ts'));

rmSync(OUT, { recursive: true, force: true });
mkdirSync(OUT, { recursive: true });
// Build before launching VS Code so compilation is outside the test timeout.
await build({
  entryPoints: tests.map((f) => join(EXT, 'src', 'e2e', f)),
  outdir: join(OUT, 'e2e'),
  bundle: true,
  platform: 'node',
  target: 'node20',
  format: 'cjs',
  external: ['vscode', 'mocha'],
  sourcemap: 'inline',
  logLevel: 'info',
});

// Scratch workspace with copies of the example scripts.
const workspace = join(OUT, 'e2e-workspace');
mkdirSync(workspace, { recursive: true });
for (const name of ['hello.lua', 'error.lua', 'step.lua', 'test_luaunit.lua', 'example_spec.lua']) {
  copyFileSync(join(ROOT, 'examples', name), join(workspace, name));
}
// The CodeLens must keep the selected interpreter even if .busted requests another.
writeFileSync(join(workspace, '.busted'), 'return { default = { lua = "lua-devtools-must-not-spawn" } }\n');
// A separate copy for the test that edits the buffer, so debugger runs see a valid hello.lua.
copyFileSync(join(ROOT, 'examples', 'hello.lua'), join(workspace, 'edit.lua'));

if (!process.argv.includes('--build-only')) {
  execFileSync('npx', ['vscode-test'], { cwd: EXT, stdio: 'inherit', shell: true });
}
