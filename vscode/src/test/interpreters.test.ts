import assert from 'node:assert/strict';
import { test } from 'node:test';
import { chmod, mkdtemp, mkdir, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import * as path from 'node:path';
import { automaticInterpreter, discoverInterpreters, inspectInterpreter, resolveInterpreter } from '../interpreters';

test('discovers distinct PATH interpreters, deduplicates symlinks, validates versions and suppresses init', { skip: process.platform === 'win32' }, async () => {
  const root = await mkdtemp(path.join(tmpdir(), 'lua interpreters '));
  const original = { PATH: process.env.PATH, LUA_INIT: process.env.LUA_INIT, LUA_INIT_5_4: process.env.LUA_INIT_5_4 };
  const first = path.join(root, 'one');
  const second = path.join(root, 'two');
  await mkdir(first); await mkdir(second);
  const fake = async (directory: string, name: string, output: string) => {
    const file = path.join(directory, name);
    await writeFile(file, `#!/bin/sh\n[ "$1" = '-v' ] || exit 1\n[ -z "$LUA_INIT$LUA_INIT_5_4" ] || exit 1\necho '${output}' >&2\n`);
    await chmod(file, 0o755);
    return file;
  };
  try {
    process.env.PATH = first + path.delimiter + second;
    process.env.LUA_INIT = 'must not execute';
    process.env.LUA_INIT_5_4 = 'must not execute';
    const lua = await fake(first, 'lua5.4', 'Lua 5.4.9');
    const other = await fake(second, 'lua5.4', 'Lua 5.4.8');
    await symlink(lua, path.join(first, 'lua'));
    const jit = await fake(first, 'luajit', 'LuaJIT 2.1.0-beta3');
    const invalid = await fake(first, 'invalid', 'not Lua');
    assert.equal(await automaticInterpreter(), lua);
    assert.equal(await resolveInterpreter('lua5.4'), lua);
    assert.equal(await resolveInterpreter('./lua'), undefined);
    assert.equal(await inspectInterpreter(invalid), undefined);
    assert.equal(await inspectInterpreter('missing'), undefined);
    assert.deepEqual(await inspectInterpreter(lua), { path: lua, version: 'Lua 5.4.9' });
    const found = await discoverInterpreters(lua);
    assert.equal(found.filter(item => item.path === lua).length, 1);
    assert.ok(!found.some(item => item.path === path.join(first, 'lua')));
    assert.ok(found.some(item => item.path === other));
    assert.ok(found.some(item => item.path === jit && item.version === 'LuaJIT 2.1.0-beta3'));
  } finally {
    for (const [key, value] of Object.entries(original)) {
      if (value === undefined) delete process.env[key]; else process.env[key] = value;
    }
    await rm(root, { recursive: true, force: true });
  }
});
