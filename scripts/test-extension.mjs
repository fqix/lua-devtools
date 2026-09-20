import { build } from 'esbuild';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
const output = fileURLToPath(new URL('../vscode/out/interpreters.test.cjs', import.meta.url));
await build({ entryPoints: [fileURLToPath(new URL('../vscode/src/test/interpreters.test.ts', import.meta.url))],
  outfile: output, bundle: true, platform: 'node', format: 'cjs', target: 'node20' });
execFileSync(process.execPath, ['--test', output], { stdio: 'inherit' });
