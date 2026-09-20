#!/usr/bin/env node
// Produce platform-specific VSIX files: `node scripts/package.mjs [--all | --target darwin-arm64 ...]`.
// Each VSIX carries its Go binaries and optional native polling module under bin/.
import { execFileSync } from 'node:child_process';
import { copyFileSync, existsSync, mkdirSync, readdirSync, rmSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const EXT = join(ROOT, 'vscode');
const ALL = ['darwin-arm64', 'darwin-x64', 'linux-x64', 'linux-arm64', 'win32-x64', 'win32-arm64'];
const { values } = parseArgs({
  options: {
    all: { type: 'boolean', default: false },
    target: { type: 'string', multiple: true },
  },
});
const targets = values.all ? ALL : values.target?.length ? values.target : [`${process.platform}-${process.arch}`];
const run = (command, args, options = {}) => execFileSync(command, args, { cwd: EXT, stdio: 'inherit', ...options });

run('node', ['esbuild.mjs']);
mkdirSync(join(EXT, 'bin'), { recursive: true });
// vsce expects LICENSE next to package.json; the canonical copy lives at the repository root.
copyFileSync(join(ROOT, 'LICENSE'), join(EXT, 'LICENSE'));
copyFileSync(join(ROOT, 'README.md'), join(EXT, 'README.md'));
for (const target of targets) {
  const source = join(EXT, 'bin', target);
  const helper = `lua-devtools-native.${target.startsWith('win32-') ? 'dll' : 'so'}`;
  const exe = target.startsWith('win32-') ? '.exe' : '';
  const required = [helper, `lua-dap${exe}`, `lua-lsp${exe}`];
  if (required.some((name) => !existsSync(join(source, name)))) {
    run('node', [join(ROOT, 'scripts', 'build-go.mjs'), '--target', target], { cwd: ROOT });
  }
  // Do not accidentally include a previously staged helper for a different OS.
  for (const suffix of ['so', 'dll']) rmSync(join(EXT, 'bin', `lua-devtools-native.${suffix}`), { force: true });
  const staged = [];
  try {
    for (const name of readdirSync(source)) {
      copyFileSync(join(source, name), join(EXT, 'bin', name));
      staged.push(join(EXT, 'bin', name));
    }
    // npx on Windows is npx.cmd; Node 18.17+ refuses to spawn .cmd without shell: true.
    run('npx', ['vsce', 'package', '--no-dependencies', '--target', target], { shell: true });
  } finally {
    for (const path of staged) rmSync(path, { force: true });
  }
}

// Restore the flat host binaries the extension uses during development (F5, smoke tests).
const host = join(EXT, 'bin', `${process.platform}-${process.arch}`);
if (existsSync(host)) {
  for (const name of readdirSync(host)) copyFileSync(join(host, name), join(EXT, 'bin', name));
}
