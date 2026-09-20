import assert from 'node:assert/strict';
import { test } from 'node:test';
import { chmod, mkdtemp, mkdir, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
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

import { applyPackagePaths, installedPackages, installArguments, packageEnvironment, parseInstalledPackages, parsePackageSpec,
  removeArguments, validPackageName, validPackageVersion } from '../projectPackages';

test('project packages isolate interpreter identities and preserve explicit search paths', async () => {
  const root = await mkdtemp(path.join(tmpdir(), 'lua packages '));
  try {
    const executable = path.join(root, 'lua');
    await writeFile(executable, '');
    const lua = await packageEnvironment(root, { path: executable, version: 'Lua 5.4.9' });
    const jit = await packageEnvironment(root, { path: executable, version: 'LuaJIT 2.1.0' });
    const upgraded = await packageEnvironment(root, { path: executable, version: 'Lua 5.4.10' });
    assert.equal(jit.version, '5.1');
    assert.notEqual(lua.tree, jit.tree);
    assert.notEqual(lua.tree, upgraded.tree);
    assert.notEqual(lua.tree, (await packageEnvironment(path.join(root, 'other'), { path: executable, version: 'Lua 5.4.9' })).tree);
    const config = { env: { LUA_PATH_5_4: 'existing/?.lua;;', LUA_CPATH_5_4: 'native/?.so;;', KEEP: 'value' }, packagePath: ['explicit/?.lua'], packageCPath: ['explicit/?.so'] };
    await applyPackagePaths(config, lua);
    assert.deepEqual(config.packagePath, ['explicit/?.lua'], 'no changes before package tree exists');
    await mkdir(lua.tree, { recursive: true });
    await applyPackagePaths(config, lua);
    assert.equal(config.env.KEEP, 'value');
    assert.ok(config.env.LUA_PATH_5_4.startsWith('explicit/?.lua;'));
    assert.ok(config.env.LUA_PATH_5_4.includes(path.join(lua.tree, 'share', 'lua', '5.4', '?.lua')));
    assert.ok(config.env.LUA_PATH_5_4.endsWith(';existing/?.lua;;'));
    assert.ok(config.env.LUA_CPATH_5_4.startsWith('explicit/?.so;'));
    assert.equal(config.packagePath, undefined);
    const args = installArguments(lua, 'luasocket');
    assert.ok(args.includes(lua.tree));
    assert.ok(args.includes('LUA=' + lua.executable));
    assert.ok(args.includes(lua.luaDir));
    for (const invalid of ['--server=evil', 'x;echo', '../file.rockspec', 'x y', 'https://example.org/pkg']) {
      assert.equal(validPackageName(invalid), false);
      assert.throws(() => installArguments(lua, invalid));
    }
    assert.equal(validPackageName('owner/package-name'), true);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('package versions, namespaced listings and mutations stay scoped to one tree', () => {
  const environment = { executable: '/lua/bin/lua', version: '5.4', tree: path.resolve('isolated packages'), luaDir: '/lua' };
  const repository = path.join(environment.tree, 'lib', 'luarocks', 'rocks-5.4');
  assert.deepEqual(parsePackageSpec(' owner/pkg@1.2.3-1 '), { name: 'owner/pkg', version: '1.2.3-1' });
  assert.deepEqual(parsePackageSpec('luaunit'), { name: 'luaunit' });
  for (const value of ['pkg@', 'pkg@--force', 'pkg@1@2', '--server=x', 'pkg@1;command', '../pkg', 'pkg@1 2']) assert.equal(parsePackageSpec(value), undefined);
  for (const value of ['', '--force', '../1', '1/2', '1\n2']) {
    assert.equal(validPackageVersion(value), false);
    assert.throws(() => removeArguments(environment, 'pkg', value));
    assert.throws(() => installArguments(environment, 'pkg', value));
  }
  const output = [
    `pkg\t1.2-1\tinstalled\t${repository}\towner`,
    `pkg\t1.2-1\tinstalled\t${repository}\towner`,
    `pkg\t2.0-1\tinstalled\t${repository}`,
    `system\t1.0-1\tinstalled\t${path.resolve('system tree')}`,
    `remote\t1.0-1\trockspec\t${repository}`,
    `bad\t--force\tinstalled\t${repository}`,
  ].join('\r\n');
  assert.deepEqual(parseInstalledPackages(output, environment), [{ name: 'owner/pkg', version: '1.2-1' }, { name: 'pkg', version: '2.0-1' }]);
  assert.deepEqual(removeArguments(environment, 'pkg', '1.2-1').slice(-5), ['remove', '--deps-mode', 'one', 'pkg', '1.2-1']);
  assert.ok(installArguments(environment, 'pkg', '1.2-1').includes('1.2-1'));
  assert.ok(!installArguments(environment, 'pkg').includes('--force'));
});

test('LuaRocks installs a chosen version, upgrades and safely removes project packages', { skip: !process.env.LUAROCKS_TEST_BINARY }, async () => {
  const rocks = process.env.LUAROCKS_TEST_BINARY!;
  const executable = process.env.LUAROCKS_TEST_LUA || await automaticInterpreter();
  assert.ok(executable);
  const interpreter = await inspectInterpreter(executable);
  assert.ok(interpreter);
  const root = await mkdtemp(path.join(tmpdir(), 'lua rocks lifecycle '));
  const run = promisify(execFile);
  try {
    const environment = await packageEnvironment(root, interpreter);
    const repository = path.join(root, 'repository');
    await mkdir(repository);
    const common = ['--lua-version', environment.version, '--lua-dir', environment.luaDir, '--tree', path.join(root, 'build-tree')];
    for (const [name, version, dependency] of [['fixture_pkg', '1.0-1', ''], ['fixture_pkg', '2.0-1', ''], ['fixture_dependent', '1.0-1', '"fixture_pkg >= 1.0"']]) {
      await writeFile(path.join(repository, `${name}.lua`), `return {version="${version}"}`);
      const spec = `${name}-${version}.rockspec`;
      await writeFile(path.join(repository, spec), `package="${name}"\nversion="${version}"\nsource={url="file:///unused"}\ndependencies={${dependency}}\nbuild={type="builtin",modules={${name}="${name}.lua"}}`);
      await run(rocks, [...common, 'make', '--pack-binary-rock', '--deps-mode', 'none', spec], { cwd: repository });
    }
    await run(path.join(path.dirname(rocks), process.platform === 'win32' ? 'luarocks-admin.bat' : 'luarocks-admin'),
      ['--lua-version', environment.version, 'make_manifest', repository], { cwd: root });
    const server = ['--only-server', 'file://' + repository];
    await run(rocks, [...server, ...installArguments(environment, 'fixture_pkg', '1.0-1')], { cwd: root });
    assert.deepEqual(await installedPackages(rocks, environment, root), [{ name: 'fixture_pkg', version: '1.0-1' }]);
    await run(rocks, [...server, ...installArguments(environment, 'fixture_pkg')], { cwd: root });
    assert.deepEqual(await installedPackages(rocks, environment, root), [{ name: 'fixture_pkg', version: '2.0-1' }]);
    assert.match(await readFile(path.join(environment.tree, 'share', 'lua', environment.version, 'fixture_pkg.lua'), 'utf8'), /2\.0-1/);
    await run(rocks, [...server, ...installArguments(environment, 'fixture_dependent')], { cwd: root });
    await assert.rejects(run(rocks, removeArguments(environment, 'fixture_pkg', '2.0-1'), { cwd: root }));
    await run(rocks, removeArguments(environment, 'fixture_dependent', '1.0-1'), { cwd: root });
    await run(rocks, removeArguments(environment, 'fixture_pkg', '2.0-1'), { cwd: root });
    assert.deepEqual(await installedPackages(rocks, environment, root), []);
  } finally { await rm(root, { recursive: true, force: true }); }
});

import { scanModules } from '../libraries';

test('library scan finds Lua and native modules without executing module code', async () => {
  const root = await mkdtemp(path.join(tmpdir(), 'lua libraries '));
  try {
    await mkdir(path.join(root, 'pkg'));
    await writeFile(path.join(root, 'pkg', 'init.lua'), 'error("must not execute")');
    await writeFile(path.join(root, 'example.lua'), 'error("must not execute")');
    await writeFile(path.join(root, 'example.so'), 'native placeholder');
    await writeFile(path.join(root, 'cjson.so'), 'native placeholder');
    const result = await scanModules('./?.lua;./?/init.lua', './?.so', root);
    assert.equal(result.limited, false);
    assert.equal(result.unreadable, false);
    assert.ok(result.modules.some(item => item.name === 'pkg' && item.file.endsWith('init.lua')));
    assert.equal(result.modules.find(item => item.name === 'example')?.native, false);
    assert.equal(result.modules.find(item => item.name === 'cjson')?.native, true);
    assert.equal(result.modules.filter(item => item.name === 'example').length, 1);
  } finally { await rm(root, { recursive: true, force: true }); }
});
