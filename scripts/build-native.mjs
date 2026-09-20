// Build one optional polling module per OS/architecture, independent of Lua version.
import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
export function buildNative(target = `${process.platform}-${process.arch}`) {
  const [platform, arch] = target.split('-');
  if (platform !== process.platform || arch !== process.arch) {
    throw new Error('The native polling module requires a matching host C toolchain');
  }
  const dir = join(root, 'vscode', 'bin', target);
  mkdirSync(dir, { recursive: true });
  const name = `lua-devtools-native.${platform === 'win32' ? 'dll' : 'so'}`;
  const flags = ['-O2', '-Wall', '-Wextra', '-shared'];
  if (platform === 'darwin') flags.push('-fPIC', '-mmacosx-version-min=11.0', '-undefined', 'dynamic_lookup');
  if (platform === 'linux') flags.push('-fPIC');
  flags.push(join(root, 'native', 'poll.c'), '-o', join(dir, name));
  if (platform === 'win32') flags.push('-lpsapi', '-static-libgcc');
  execFileSync(process.env.CC || (platform === 'win32' ? 'gcc' : 'cc'), flags, { stdio: 'inherit' });
  copyFileSync(join(dir, name), join(root, 'vscode', 'bin', name));
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) buildNative();
