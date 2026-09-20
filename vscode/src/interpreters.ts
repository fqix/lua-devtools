import { execFile } from 'node:child_process';
import { constants } from 'node:fs';
import { access, realpath } from 'node:fs/promises';
import * as path from 'node:path';

export interface Interpreter { path: string; version: string }

const names = ['lua5.4', 'lua', 'lua5.5', 'lua5.3', 'lua5.2', 'lua5.1', 'luajit', 'luajit-2.1'];
const brew54 = ['/opt/homebrew', '/usr/local'].map(root => `${root}/opt/lua@5.4/bin/lua5.4`);

async function executable(file: string): Promise<boolean> {
  try {
    await access(file, process.platform === 'win32' ? constants.F_OK : constants.X_OK);
    return true;
  } catch { return false; }
}

export async function resolveInterpreter(value: string): Promise<string | undefined> {
  if (path.isAbsolute(value)) return await executable(value) ? value : undefined;
  // Relative paths are not searched against an arbitrary extension-host cwd.
  if (value.includes('/') || value.includes('\\')) return undefined;
  for (const directory of (process.env.PATH ?? '').split(path.delimiter).filter(p => path.isAbsolute(p))) {
    for (const suffix of process.platform === 'win32' && !value.endsWith('.exe') ? ['.exe', ''] : ['']) {
      const file = path.join(directory, value + suffix);
      if (await executable(file)) return file;
    }
  }
  return undefined;
}

export async function automaticInterpreter(): Promise<string | undefined> {
  for (const candidate of ['lua5.4', ...brew54, 'lua']) {
    const file = await resolveInterpreter(candidate);
    if (file) return file;
  }
  return undefined;
}

export async function inspectInterpreter(value: string): Promise<Interpreter | undefined> {
  const file = await resolveInterpreter(value);
  if (!file) return undefined;
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !/^LUA_INIT(?:_|$)/i.test(key)));
  return new Promise(resolve => {
    execFile(file, ['-v'], { env, timeout: 2000, maxBuffer: 16384, windowsHide: true }, (error, stdout, stderr) => {
      const version = `${stdout}\n${stderr}`.match(/\b(?:Lua 5\.[1-5](?:\.\d+)?|LuaJIT 2\.1[\w.-]*)\b/)?.[0];
      resolve(!error && version ? { path: file, version } : undefined);
    });
  });
}

export async function discoverInterpreters(configured?: string): Promise<Interpreter[]> {
  const candidates = [configured, ...names, ...brew54,
    ...(process.env.PATH ?? '').split(path.delimiter).filter(directory => path.isAbsolute(directory))
      .flatMap(directory => names.map(name => path.join(directory, name + (process.platform === 'win32' ? '.exe' : '')))),
    ...['/opt/homebrew', '/usr/local'].flatMap(root => ['lua', 'lua@5.1', 'lua@5.2', 'lua@5.3', 'lua@5.4', 'lua@5.5', 'luajit']
      .flatMap(formula => [ `${root}/opt/${formula}/bin/lua`, `${root}/opt/${formula}/bin/${formula.replace('@', '')}` ])),
  ].filter((p): p is string => !!p);
  const seen = new Set<string>();
  const files: string[] = [];
  for (const candidate of candidates) {
    const file = await resolveInterpreter(candidate);
    if (!file) continue;
    const canonical = await realpath(file).catch(() => file);
    if (!seen.has(canonical)) { seen.add(canonical); files.push(file); }
  }
  const results = await Promise.all(files.map(inspectInterpreter));
  return results.filter((result): result is Interpreter => !!result);
}
