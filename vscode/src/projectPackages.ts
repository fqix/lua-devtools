import { createHash } from 'node:crypto';
import { execFile } from 'node:child_process';
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

export function validPackageVersion(version: string): boolean {
  return /^[a-zA-Z0-9][a-zA-Z0-9._+-]*$/.test(version);
}

export function parsePackageSpec(value: string): { name: string; version?: string } | undefined {
  const [name, version, extra] = value.trim().split('@');
  if (!validPackageName(name) || extra !== undefined || (version !== undefined && !validPackageVersion(version))) return undefined;
  return { name, ...(version ? { version } : {}) };
}

function treeArguments(environment: PackageEnvironment): string[] {
  return ['--lua-version', environment.version, '--lua-dir', environment.luaDir, '--tree', environment.tree];
}

export function installArguments(environment: PackageEnvironment, name: string, version?: string): string[] {
  if (!validPackageName(name)) throw new Error('Invalid LuaRocks package name.');
  if (version !== undefined && !validPackageVersion(version)) throw new Error('Invalid LuaRocks package version.');
  return [...treeArguments(environment), 'install', '--deps-mode', 'one', name, ...(version ? [version] : []),
    `LUA=${environment.executable}`, `LUA_BINDIR=${path.dirname(environment.executable)}`];
}

export function removeArguments(environment: PackageEnvironment, name: string, version: string): string[] {
  if (!validPackageName(name) || !validPackageVersion(version)) throw new Error('Invalid LuaRocks package or version.');
  return [...treeArguments(environment), 'remove', '--deps-mode', 'one', name, version];
}

export interface InstalledPackage { name: string; version: string }

export function parseInstalledPackages(output: string, environment: PackageEnvironment): InstalledPackage[] {
  const repository = path.resolve(environment.tree, 'lib', 'luarocks', `rocks-${environment.version}`);
  const packages = new Map<string, InstalledPackage>();
  for (const line of output.split(/\r?\n/)) {
    const [packageName, version, state, directory, namespace] = line.split('\t');
    if (state !== 'installed' || !directory || path.resolve(directory) !== repository) continue;
    const name = namespace ? `${namespace}/${packageName}` : packageName;
    if (!validPackageName(name) || !validPackageVersion(version)) continue;
    packages.set(`${name}@${version}`, { name, version });
  }
  return [...packages.values()].sort((a, b) => a.name.localeCompare(b.name) || b.version.localeCompare(a.version, undefined, { numeric: true }));
}

export async function installedPackages(rocks: string, environment: PackageEnvironment, cwd: string): Promise<InstalledPackage[]> {
  if (!await stat(environment.tree).then(s => s.isDirectory(), () => false)) return [];
  return new Promise((resolve, reject) => {
    execFile(rocks, [...treeArguments(environment), 'list', '--porcelain'],
      { cwd, timeout: 10000, maxBuffer: 1024 * 1024, windowsHide: true }, (error, stdout, stderr) => {
        if (error) { reject(new Error(stderr.trim() || error.message)); return; }
        resolve(parseInstalledPackages(stdout, environment));
      });
  });
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
