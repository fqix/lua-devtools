// Bundle every vscode/src/test/*.test.ts with esbuild and run it under node:test.
import { build } from 'esbuild';
import { execFileSync } from 'node:child_process';
import { readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
const tests = fileURLToPath(new URL('../vscode/src/test/', import.meta.url));
const out = fileURLToPath(new URL('../vscode/out/', import.meta.url));
const entryPoints = readdirSync(tests).filter(name => name.endsWith('.test.ts')).map(name => tests + name);
await build({ entryPoints, outdir: out, outExtension: { '.js': '.cjs' }, bundle: true, platform: 'node', format: 'cjs', target: 'node20' });
execFileSync(process.execPath, ['--test', ...entryPoints.map(entry => out + entry.slice(tests.length).replace(/\.ts$/, '.cjs'))], { stdio: 'inherit' });
