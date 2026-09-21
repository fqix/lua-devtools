import assert from 'node:assert/strict';
import { test } from 'node:test';
import * as os from 'node:os';
import * as path from 'node:path';
import { launchPackagePaths } from '../launchPaths';

const root = path.join(os.tmpdir(), 'project');

test('collects packagePath templates of Lua launch configurations in order, without duplicates', () => {
  const configurations = [
    { type: 'lua', request: 'launch', packagePath: ['${workspaceFolder}/src/?.lua', 'lib/?/init.lua'] },
    { type: 'node', request: 'launch', packagePath: ['${workspaceFolder}/ignored/?.lua'] },
    { type: 'lua', request: 'attach', packagePath: ['lib/?/init.lua', '${workspaceFolder}/vendor/?.lua'] },
    { type: 'lua', request: 'launch' },
  ];
  assert.deepEqual(launchPackagePaths(configurations, root), [
    path.join(root, 'src', '?.lua'),
    path.normalize('lib/?/init.lua'),
    path.join(root, 'vendor', '?.lua'),
  ]);
});

test('substitutes folder, home and environment variables and skips editor-dependent ones', () => {
  const configurations = [{
    type: 'lua',
    packagePath: [
      '${workspaceRoot}/?.lua',
      '${workspaceFolderBasename}/?.lua',
      '${userHome}/.luarocks/share/lua/5.4/?.lua',
      '${env:LUA_DEVTOOLS_TEST_ROCKS}/?.lua',
      '${env:LUA_DEVTOOLS_TEST_UNSET}/?.lua',
      '${workspaceFolder:other}/?.lua',
      '${fileDirname}/?.lua',
      '',
      42,
    ],
  }];
  const env = { LUA_DEVTOOLS_TEST_ROCKS: '/opt/rocks' };
  assert.deepEqual(launchPackagePaths(configurations, root, env), [
    path.join(root, '?.lua'),
    path.normalize('project/?.lua'),
    path.join(os.homedir(), '.luarocks', 'share', 'lua', '5.4', '?.lua'),
    path.normalize('/opt/rocks/?.lua'),
    path.normalize('/?.lua'),
  ]);
});

test('tolerates missing or malformed launch configurations', () => {
  assert.deepEqual(launchPackagePaths(undefined, root), []);
  assert.deepEqual(launchPackagePaths('not a list', root), []);
  assert.deepEqual(launchPackagePaths([null, 'x', { type: 'lua', packagePath: 'src/?.lua' }], root), []);
});

test('reads packageCPath with the same substitution rules', () => {
  const configurations = [{ type: 'lua', packagePath: ['${workspaceFolder}/?.lua'], packageCPath: ['${workspaceFolder}/lib/?.so', '${file}/?.so'] }];
  assert.deepEqual(launchPackagePaths(configurations, root, {}, 'packageCPath'), [path.join(root, 'lib', '?.so')]);
  assert.deepEqual(launchPackagePaths(configurations, root, {}), [path.join(root, '?.lua')]);
});

test('resolves relative templates against the configuration cwd', () => {
  const configurations = [
    { type: 'lua', cwd: '${workspaceFolder}/app', packagePath: ['lib/?.lua', '${workspaceFolder}/shared/?.lua'], packageCPath: ['lib/?.so'] },
    { type: 'lua', cwd: 'services', packagePath: ['lib/?.lua'] },
    { type: 'lua', packagePath: ['lib/?.lua'] },
    { type: 'lua', cwd: '${fileDirname}', packagePath: ['lib/?.lua', '/abs/?.lua'] },
  ];
  assert.deepEqual(launchPackagePaths(configurations, root), [
    path.join(root, 'app', 'lib', '?.lua'),
    path.join(root, 'shared', '?.lua'),
    path.join('services', 'lib', '?.lua'),
    path.normalize('lib/?.lua'),
    path.normalize('/abs/?.lua'),
  ]);
  assert.deepEqual(launchPackagePaths(configurations, root, {}, 'packageCPath'), [path.join(root, 'app', 'lib', '?.so')]);
});
