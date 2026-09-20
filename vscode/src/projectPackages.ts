import { createHash } from 'node:crypto';
import { realpath, stat } from 'node:fs/promises';
import * as path from 'node:path';
import type { Interpreter } from './interpreters';

export interface PackageEnvironment {
  executable: string;
  version: string;
  tree: string;
  luaDir: string;
}

export async function packageEnvironment(project: string, interpreter: Interpreter): Promise<PackageEnvironment> {
  const executable = await realpath(interpreter.path);
  const version = interpreter.version.startsWith('LuaJIT') ? '5.1' : interpreter.version.match(/5\.[1-5]/)![0];
  const identity = createHash('sha256').update(executable + '\0' + interpreter.version).digest('hex').slice(0, 16);
  const bin = path.dirname(executable);
  const tree = path.join(project, '.lua-devtools', 'rocks', `${version}-${identity}`);
  // Semicolons are separators in Lua search paths and cannot represent a directory.
  if (/[;\r\n]/.test(tree)) throw new Error('Project path contains a Lua search-path separator.');
  return { executable, version, tree, luaDir: path.basename(bin).toLowerCase() === 'bin' ? path.dirname(bin) : bin };
}

export function validPackageName(name: string): boolean {
  return /^[a-zA-Z0-9][a-zA-Z0-9_.-]*(?:\/[a-zA-Z0-9][a-zA-Z0-9_.-]*)?$/.test(name);
}

export function installArguments(environment: PackageEnvironment, name: string): string[] {
  if (!validPackageName(name)) throw new Error('Invalid LuaRocks package name.');
  return ['--lua-version', environment.version, '--lua-dir', environment.luaDir, '--tree', environment.tree,
    'install', '--deps-mode', 'one', name, `LUA=${environment.executable}`, `LUA_BINDIR=${path.dirname(environment.executable)}`];
}

export interface PackageLaunch {
  env?: Record<string, string>;
  packagePath?: string[];
  packageCPath?: string[];
}

export async function applyPackagePaths(config: PackageLaunch, environment: PackageEnvironment): Promise<void> {
  if (!await stat(environment.tree).then(s => s.isDirectory(), () => false)) return;
  const env = { ...config.env };
  for (const [key, field, entries] of [
    ['LUA_PATH', 'packagePath', [path.join(environment.tree, 'share', 'lua', environment.version, '?.lua'), path.join(environment.tree, 'share', 'lua', environment.version, '?', 'init.lua')]],
    ['LUA_CPATH', 'packageCPath', [path.join(environment.tree, 'lib', 'lua', environment.version, process.platform === 'win32' ? '?.dll' : '?.so')]],
  ] as const) {
    const versionKey = `${key}_${environment.version.replace('.', '_')}`;
    const inherited = environment.version === '5.1' ? undefined : env[versionKey] ?? process.env[versionKey];
    const base = inherited ?? env[key] ?? process.env[key] ?? ';';
    const value = [...(config[field] ?? []), ...entries, base].join(';');
    env[key] = value;
    if (environment.version !== '5.1') env[versionKey] = value;
    // Already included above; prevent the adapter from replacing the merged env.
    delete config[field];
  }
  config.env = env;
}
