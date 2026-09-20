#!/usr/bin/env node
// Build the Go servers (lua-dap, lua-lsp) into vscode/bin/<target>/.
//   node scripts/build-go.mjs                    # host platform, also copied flat into vscode/bin/ for F5
//   node scripts/build-go.mjs --target linux-x64
//   node scripts/build-go.mjs --test             # go vet + unit/integration tests (-race where supported)
// tree-sitter is compiled through cgo, so a target can only be built on a matching host.
import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdirSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { buildNative } from './build-native.mjs';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const EXT = join(ROOT, 'vscode');
const GOOS = { darwin: 'darwin', linux: 'linux', win32: 'windows' };
const GOARCH = { x64: 'amd64', arm64: 'arm64' };
const HOST = `${process.platform}-${process.arch}`;
const VERSION = JSON.parse(readFileSync(join(ROOT, 'vscode', 'package.json'), 'utf8')).version;

const { values } = parseArgs({
  options: {
    target: { type: 'string', multiple: true },
    test: { type: 'boolean', default: false },
  },
});
const run = (args, env = {}) =>
  execFileSync('go', args, { cwd: ROOT, stdio: 'inherit', env: { ...process.env, ...env } });

if (values.test) {
  buildNative();
  run(['vet', './...']);
  // Go does not support the race detector on windows/arm64. Keep it on
  // every other published platform, including the Windows x64 job.
  const race = HOST === 'win32-arm64' ? [] : ['-race'];
  run(['test', ...race, '-tags=integration', '-count=1', '-timeout=60s', './...']);
  process.exit(0);
}

for (const target of values.target?.length ? values.target : [HOST]) {
  const [platform, arch] = target.split('-');
  if (!GOOS[platform] || !GOARCH[arch]) throw new Error(`unknown target ${target}`);
  if (target !== HOST) {
    console.warn(`warning: building ${target} on ${HOST}; cgo cross-compilation needs a matching C toolchain`);
  }
  const outDir = join(EXT, 'bin', target);
  mkdirSync(outDir, { recursive: true });
  console.log(`Building Go servers for ${target}`);
  buildNative(target);
  run(['build', '-trimpath', '-ldflags', `-s -w -X github.com/fqix/lua-devtools/internal/lsp.Version=${VERSION}`, '-o', outDir + '/', './cmd/...'], {
    GOOS: GOOS[platform],
    GOARCH: GOARCH[arch],
    CGO_ENABLED: '1',
  });
  if (target === HOST) {
    // Flat copies are what the extension loads during development (F5).
    for (const name of readdirSync(outDir)) copyFileSync(join(outDir, name), join(EXT, 'bin', name));
  }
}
