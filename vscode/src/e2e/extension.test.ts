// End-to-end: the built extension inside a real VS Code, talking to the real
// bin/lua-lsp and bin/lua-dap servers over a scratch workspace.
import assert from 'node:assert/strict';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { mkdtemp, rm } from 'node:fs/promises';
import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import * as vscode from 'vscode';
import { formatLua } from '../formatter';
import { automaticInterpreter, discoverInterpreters, inspectInterpreter } from '../interpreters';
import { packageEnvironment, removeArguments } from '../projectPackages';
import { languageEnvironment } from '../languageEnvironment';
import { InterpreterTreeProvider } from '../interpreterView';

function until<T>(probe: () => T | undefined | false | null, timeout = 20000, what = 'condition'): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const started = Date.now();
    const tick = () => {
      const value = probe();
      if (value) return resolve(value);
      if (Date.now() - started > timeout) return reject(new Error(`Timed out: ${what}`));
      setTimeout(tick, 50);
    };
    tick();
  });
}

/** Re-runs an async probe until its result passes `ok`. */
async function retry<T>(probe: () => Thenable<T | undefined>, ok: (value: T) => boolean, what: string, timeout = 30000): Promise<T> {
  const started = Date.now();
  for (;;) {
    const value = await probe();
    if (value && ok(value)) return value;
    if (Date.now() - started > timeout) throw new Error(`Timed out: ${what}`);
    await new Promise((r) => setTimeout(r, 200));
  }
}

function positionOf(doc: vscode.TextDocument, needle: string): vscode.Position {
  const index = doc.getText().indexOf(needle);
  assert.ok(index >= 0, `${JSON.stringify(needle)} found in ${doc.fileName}`);
  return doc.positionAt(index);
}

/** Records every DAP message of a session so tests can wait for events. */
class DapRecorder implements vscode.DebugAdapterTracker {
  readonly messages: any[] = [];
  onDidSendMessage(message: any): void {
    this.messages.push(message);
  }
  event(name: string): any | undefined {
    return this.messages.find((m) => m.type === 'event' && m.event === name);
  }
  outputs(): string {
    return this.messages
      .filter((m) => m.type === 'event' && m.event === 'output')
      .map((m) => m.body.output)
      .join('');
  }
}

suite('Lua DevTools end to end', function () {
  this.timeout(120000);
  const folder = () => vscode.workspace.workspaceFolders![0];
  const fileUri = (name: string) => vscode.Uri.file(join(folder().uri.fsPath, name));
  // One recorder per debug session, keyed by session id, so a test never reads
  // the messages of the previous test's session.
  const recorders = new Map<string, DapRecorder>();

  suiteSetup(async () => {
    const extension = vscode.extensions.getExtension('fqix.lua-devtools');
    assert.ok(extension, 'extension is installed');
    await extension.activate();
    vscode.debug.registerDebugAdapterTrackerFactory('lua', {
      createDebugAdapterTracker: (session) => {
        const recorder = new DapRecorder();
        recorders.set(session.id, recorder);
        return recorder;
      },
    });
  });

  /** Runs `start` and returns the session it created together with its recorder. */
  async function startSession(start: () => Thenable<unknown>): Promise<{ session: vscode.DebugSession; recorder: DapRecorder }> {
    const started = new Promise<vscode.DebugSession>((resolve) => {
      const listener = vscode.debug.onDidStartDebugSession((session) => {
        listener.dispose();
        resolve(session);
      });
    });
    await start();
    const session = await started;
    const recorder = await until(() => recorders.get(session.id), 10000, 'tracker for session');
    return { session, recorder };
  }

  test('registers its commands', async () => {
    const commands = await vscode.commands.getCommands(true);
    for (const name of ['luaDevtools.run', 'luaDevtools.debug', 'luaDevtools.selectInterpreter', 'luaDevtools.viewTableJSON', 'luaDevtools.refreshInterpreters', 'luaDevtools.enterInterpreterPath', 'luaDevtools.installPackage', 'luaDevtools.upgradePackage', 'luaDevtools.uninstallPackage', 'luaDevtools.installPackageVersion', 'luaDevtools.sendInput', 'luaDevtools.closeInput', 'luaDevtools.resumeCoroutine', 'luaDevtools.copyAttachSnippet']) {
      assert.ok(commands.includes(name), `${name} is registered`);
    }
  });

  test('environment tree selects interpreters and restores automatic detection', async () => {
    const config = vscode.workspace.getConfiguration('luaDevtools');
    const previous = config.inspect<string>('luaPath')?.workspaceValue;
    const provider = new InterpreterTreeProvider();
    let changes = 0;
    const listener = provider.onDidChangeTreeData(() => changes++);
    try {
      await vscode.commands.executeCommand('luaDevtools.environments.focus');
      const rows = await provider.getChildren();
      const interpreter = rows.find(row => row.contextValue === 'luaInterpreter');
      assert.ok(interpreter?.command, 'a discovered interpreter is available');
      const details = await provider.getChildren(interpreter);
      assert.equal(details[0].label, interpreter.executable);
      const standard = await provider.getChildren(details[1]);
      const osLibrary = standard.find(row => row.label === 'os');
      assert.ok(osLibrary);
      const members = await provider.getChildren(osLibrary);
      assert.ok(members.some(row => row.label === 'time' && row.description === 'Native C function'));
      await vscode.commands.executeCommand(interpreter.command.command, ...interpreter.command.arguments!);
      assert.equal(vscode.workspace.getConfiguration('luaDevtools').get('luaPath'), interpreter.executable);
      provider.refresh();
      const selected = await provider.getChildren();
      assert.equal(selected.find(row => row.id === interpreter.id)?.iconPath instanceof vscode.ThemeIcon, true);
      assert.equal((selected.find(row => row.id === interpreter.id)?.iconPath as vscode.ThemeIcon).id, 'check');
      const automatic = selected.find(row => row.id === 'automatic')!;
      await vscode.commands.executeCommand(automatic.command!.command, ...automatic.command!.arguments!);
      assert.equal(vscode.workspace.getConfiguration('luaDevtools').get('luaPath'), '');
      provider.refresh();
      assert.equal(((await provider.getChildren())[0].iconPath as vscode.ThemeIcon).id, 'check');
      await config.update('luaPath', join(folder().uri.fsPath, 'missing-lua'), vscode.ConfigurationTarget.Workspace);
      provider.refresh();
      assert.ok((await provider.getChildren()).some(row => (row.iconPath as vscode.ThemeIcon)?.id === 'warning'));
      assert.equal(changes, 3);
      await vscode.commands.executeCommand('luaDevtools.refreshInterpreters');
    } finally {
      listener.dispose();
      provider.dispose();
      await config.update('luaPath', previous, vscode.ConfigurationTarget.Workspace);
    }
  });

  test('run and debug automatically load project packages for the launch interpreter', async () => {
    const executable = await automaticInterpreter();
    assert.ok(executable);
    const interpreter = await inspectInterpreter(executable);
    assert.ok(interpreter);
    const environment = await packageEnvironment(folder().uri.fsPath, interpreter);
    const moduleDirectory = vscode.Uri.file(join(environment.tree, 'share', 'lua', environment.version));
    const module = vscode.Uri.joinPath(moduleDirectory, 'lua_devtools_package_test.lua');
    const program = fileUri('managed-package.lua');
    await vscode.workspace.fs.createDirectory(moduleDirectory);
    await vscode.workspace.fs.writeFile(module, Buffer.from('return {value=42}'));
    await vscode.workspace.fs.writeFile(program, Buffer.from('assert(require("lua_devtools_package_test").value==42); print("PACKAGE_OK")'));
    try {
      const provider = new InterpreterTreeProvider();
      try {
        const row = (await provider.getChildren()).find(item => item.executable === executable);
        assert.ok(row);
        const details = await provider.getChildren(row);
        const projects = await provider.getChildren(details[2]);
        const sources = await provider.getChildren(projects[0]);
        const moduleGroup = sources.find(item => item.label === 'Project modules');
        assert.ok(moduleGroup);
        const modules = await provider.getChildren(moduleGroup);
        assert.ok(modules.some(item => item.label === 'lua_devtools_package_test'));
      } finally { provider.dispose(); }
      for (const noDebug of [false, true]) {
        const { recorder } = await startSession(() => vscode.debug.startDebugging(folder(), {
          type: 'lua', request: 'launch', name: 'Project package test', program: program.fsPath, luaPath: executable,
        }, { noDebug }));
        await until(() => recorder.event('terminated'), 30000, 'package test termination');
        assert.equal(recorder.event('exited').body.exitCode, 0);
        assert.match(recorder.outputs(), /PACKAGE_OK/);
      }
    } finally {
      await vscode.workspace.fs.delete(module);
      await vscode.workspace.fs.delete(program);
    }
  });

  test('shows installed package versions and uninstalls from the environment tree', async function () {
    const rocks = process.env.LUAROCKS_TEST_BINARY;
    if (!rocks) { this.skip(); return; }
    const executable = process.env.LUAROCKS_TEST_LUA || await automaticInterpreter();
    assert.ok(executable);
    const interpreter = await inspectInterpreter(executable);
    assert.ok(interpreter);
    const config = vscode.workspace.getConfiguration('luaDevtools');
    const previous = config.inspect<string>('luarocksPath')?.workspaceValue;
    const env = await packageEnvironment(folder().uri.fsPath, interpreter);
    const source = await mkdtemp(join(tmpdir(), 'lua-tree-package-'));
    const run = promisify(execFile);
    const name = 'devtools_management_fixture';
    const version = '1.0-1';
    const provider = new InterpreterTreeProvider();
    const ended: vscode.TaskProcessEndEvent[] = [];
    const listener = vscode.tasks.onDidEndTaskProcess(event => ended.push(event));
    try {
      await vscode.workspace.fs.writeFile(vscode.Uri.file(join(source, `${name}.lua`)), Buffer.from('return {value=1}'));
      const spec = `${name}-${version}.rockspec`;
      await vscode.workspace.fs.writeFile(vscode.Uri.file(join(source, spec)), Buffer.from(`package="${name}"\nversion="${version}"\nsource={url="file:///unused"}\nbuild={type="builtin",modules={${name}="${name}.lua"}}`));
      await run(rocks, ['--lua-version', env.version, '--lua-dir', env.luaDir, '--tree', env.tree, 'make', '--deps-mode', 'none', spec], { cwd: source });
      await config.update('luarocksPath', rocks, vscode.ConfigurationTarget.Workspace);
      const row = (await provider.getChildren()).find(item => item.executable === interpreter.path);
      assert.ok(row);
      const groups = await provider.getChildren(row);
      const projects = await provider.getChildren(groups[2]);
      const sources = await provider.getChildren(projects[0]);
      const packages = sources.find(item => item.contextValue === 'luaPackageEnvironment');
      assert.ok(packages);
      const installed = (await provider.getChildren(packages)).find(item => item.label === name);
      assert.ok(installed);
      assert.equal(installed.description, version);
      assert.equal(installed.packageTarget?.project, folder().uri.fsPath);
      assert.equal(installed.packageTarget?.executable, interpreter.path);
      assert.equal(installed.contextValue, 'luaInstalledPackage');
      await vscode.commands.executeCommand('luaDevtools.uninstallPackage', installed);
      const event = await until(() => ended.find(event => event.execution.task.definition.type === 'luaDevtools.package' && event.execution.task.definition.tree === env.tree), 30000, 'package uninstall task');
      assert.equal(event.exitCode, 0);
      provider.refresh();
      const refreshedRow = (await provider.getChildren()).find(item => item.executable === interpreter.path)!;
      const refreshedGroups = await provider.getChildren(refreshedRow);
      const refreshedProjects = await provider.getChildren(refreshedGroups[2]);
      const refreshedSources = await provider.getChildren(refreshedProjects[0]);
      const refreshedPackages = refreshedSources.find(item => item.contextValue === 'luaPackageEnvironment')!;
      assert.ok(!(await provider.getChildren(refreshedPackages)).some(item => item.label === name));
    } finally {
      listener.dispose(); provider.dispose();
      await config.update('luarocksPath', previous, vscode.ConfigurationTarget.Workspace);
      await run(rocks, removeArguments(env, name, version), { cwd: source }).catch(() => {});
      await rm(source, { recursive: true, force: true });
    }
  });

  test('formats unsaved Lua with StyLua config and respects ignores', async function () {
    const binary = process.env.STYLUA_TEST_BINARY;
    if (!binary) { this.skip(); return; }
    const config = vscode.workspace.getConfiguration('luaDevtools');
    const previous = config.inspect<string>('styluaPath')?.workspaceValue;
    const directory = fileUri('formatting-test');
    const uri = vscode.Uri.joinPath(directory, 'buffer with spaces.lua');
    await vscode.workspace.fs.createDirectory(directory);
    await vscode.workspace.fs.writeFile(uri, Buffer.from(''));
    await vscode.workspace.fs.writeFile(vscode.Uri.joinPath(directory, 'stylua.toml'), Buffer.from('indent_type = "Spaces"\nindent_width = 2\n'));
    try {
      await config.update('styluaPath', binary, vscode.ConfigurationTarget.Workspace);
      const doc = await vscode.workspace.openTextDocument(uri);
      const source = '-- 中文 comment\nlocal function f(x)\nreturn {text=[=[  unchanged  ]=],value=x+1}\nend\n';
      const insert = new vscode.WorkspaceEdit(); insert.insert(uri, new vscode.Position(0, 0), source);
      await vscode.workspace.applyEdit(insert);
      assert.ok(doc.isDirty);
      const edits = await vscode.commands.executeCommand<vscode.TextEdit[]>('vscode.executeFormatDocumentProvider', uri, { tabSize: 8, insertSpaces: false });
      assert.ok(edits?.length);
      assert.equal(Buffer.from(await vscode.workspace.fs.readFile(uri)).toString(), '', 'formatter must not write the file');
      const apply = new vscode.WorkspaceEdit(); apply.set(uri, edits); await vscode.workspace.applyEdit(apply);
      assert.match(doc.getText(), /\n  return/);
      assert.ok(doc.getText().includes('[=[  unchanged  ]=]'));
      assert.ok(doc.getText().includes('-- 中文 comment'));
      const again = await vscode.commands.executeCommand<vscode.TextEdit[]>('vscode.executeFormatDocumentProvider', uri, { tabSize: 2, insertSpaces: true });
      assert.equal(again?.length ?? 0, 0, 'formatting is idempotent');
      const ignored = vscode.Uri.joinPath(directory, 'ignored.lua');
      await vscode.workspace.fs.writeFile(ignored, Buffer.from('local x=1'));
      await vscode.workspace.fs.writeFile(vscode.Uri.joinPath(directory, '.styluaignore'), Buffer.from('ignored.lua\n'));
      assert.equal(await formatLua(binary, 'local x=1', ignored.fsPath, directory.fsPath), 'local x=1');
      await assert.rejects(formatLua(binary, 'local =', uri.fsPath, directory.fsPath));
      await assert.rejects(formatLua(binary, source, uri.fsPath, directory.fsPath, AbortSignal.abort()));
    } finally {
      await config.update('styluaPath', previous, vscode.ConfigurationTarget.Workspace);
      await vscode.workspace.fs.delete(directory, { recursive: true });
    }
  });

  suite('language server', () => {
    let doc: vscode.TextDocument;

    suiteSetup(async () => {
      doc = await vscode.workspace.openTextDocument(fileUri('hello.lua'));
      await vscode.window.showTextDocument(doc);
    });

    test('provides document symbols', async () => {
      // The server may still be starting; retry until it answers.
      const symbols = await retry(
        () => vscode.commands.executeCommand<vscode.DocumentSymbol[]>('vscode.executeDocumentSymbolProvider', doc.uri),
        (result) => result.length > 0,
        'document symbols',
      );
      assert.deepEqual(symbols.map((s) => s.name), ['add', 'x', 'y', 'total']);
    });

    test('goes to definition', async () => {
      const locations = await vscode.commands.executeCommand<vscode.Location[]>(
        'vscode.executeDefinitionProvider',
        doc.uri,
        positionOf(doc, 'add(x, y)'),
      );
      assert.equal(locations.length, 1);
      assert.equal(locations[0].range.start.line, 0);
    });

    test('finds lexical references, renames safely and shows call signatures', async () => {
      const uri = fileUri('refactoring.lua');
      await vscode.workspace.fs.writeFile(uri, Buffer.from('local function add(left, right) return left+right end\nlocal value=add(1, 2)\ndo local value=3; print(value) end\nprint(value)\n'));
      try {
        const document = await vscode.workspace.openTextDocument(uri);
        const refs = await retry(
          () => vscode.commands.executeCommand<vscode.Location[]>('vscode.executeReferenceProvider', uri, positionOf(document, 'value=add')),
          values => values.length > 0, 'references',
        );
        assert.equal(refs.length, 2);
        const signature = await vscode.commands.executeCommand<vscode.SignatureHelp>('vscode.executeSignatureHelpProvider', uri, positionOf(document, '2)'));
        assert.equal(signature.signatures[0].label, 'add(left, right)');
        assert.equal(signature.activeParameter, 1);
        const edit = await vscode.commands.executeCommand<vscode.WorkspaceEdit>('vscode.executeDocumentRenameProvider', uri, positionOf(document, 'value=add'), 'total');
        assert.equal(edit.get(uri).length, 2);
        assert.ok(await vscode.workspace.applyEdit(edit));
        assert.match(document.getText(), /local total=add/);
        assert.match(document.getText(), /local value=3; print\(value\)/);
        assert.match(document.getText(), /print\(total\)/);
        await assert.rejects(Promise.resolve(vscode.commands.executeCommand('vscode.executeDocumentRenameProvider', uri, positionOf(document, 'total=add'), 'end')));
      } finally { await vscode.workspace.fs.delete(uri); }
    });

    test('rejects unsafe member aliases and global declaration captures', async () => {
      const cases = [
        { name: 'nested', source: 'local t={child={foo=1}}\nprint(t.child.foo)\nt.child={foo=2}\nprint(t.child.foo)\n', cursor: 'foo=1', replacement: 'bar', error: /rename is unsafe/ },
        { name: 'alias', source: 'local t={foo=1}\nif flag then t={foo=2} end\nlocal alias=t\nprint(alias.foo)\n', cursor: 'foo=1', replacement: 'bar', error: /rename is unsafe/ },
        { name: 'global', source: 'local review_local=1\nfunction review_global() end\n', cursor: 'review_global', replacement: 'review_local', error: /binding/ },
      ];
      for (const item of cases) {
        const uri = fileUri(`unsafe-rename-${item.name}.lua`);
        await vscode.workspace.fs.writeFile(uri, Buffer.from(item.source));
        try {
          const document = await vscode.workspace.openTextDocument(uri);
          await vscode.window.showTextDocument(document);
          await retry(
            () => vscode.commands.executeCommand<vscode.Location[]>('vscode.executeReferenceProvider', uri, positionOf(document, item.cursor)),
            values => values.length > 0, `unsafe rename ${item.name} ready`,
          );
          await assert.rejects(
            Promise.resolve(vscode.commands.executeCommand('vscode.executeDocumentRenameProvider', uri, positionOf(document, item.cursor), item.replacement)),
            item.error,
          );
          assert.equal(document.getText(), item.source);
          assert.equal(document.isDirty, false);
        } finally { await vscode.workspace.fs.delete(uri); }
      }
    });

    test('finds and renames module members and globals across files', async () => {
      const moduleDir = fileUri('xref');
      const module = vscode.Uri.joinPath(moduleDir, 'store.lua');
      const consumer = fileUri('xref-consumer.lua');
      const main = fileUri('xref-main.lua');
      await vscode.workspace.fs.createDirectory(moduleDir);
      await vscode.workspace.fs.writeFile(module, Buffer.from('local M = {}\nfunction M.put(key) end\nM.size = 0\nreturn M\n'));
      await vscode.workspace.fs.writeFile(consumer, Buffer.from('local store = require("xref.store")\nstore.put("a")\nshared_flag = true\n'));
      await vscode.workspace.fs.writeFile(main, Buffer.from('local s = require("xref.store")\ns.put("b")\nprint(s.size, shared_flag)\n'));
      try {
        const document = await vscode.workspace.openTextDocument(main);
        await vscode.window.showTextDocument(document);
        const refs = await retry(
          () => vscode.commands.executeCommand<vscode.Location[]>('vscode.executeReferenceProvider', main, positionOf(document, 'put("b")')),
          values => values.length === 3, 'cross-file member references',
        );
        assert.deepEqual(refs.map(ref => [ref.uri.fsPath, ref.range.start.line]).sort(), [[consumer.fsPath, 1], [main.fsPath, 1], [module.fsPath, 1]].sort());
        const globals = await vscode.commands.executeCommand<vscode.Location[]>('vscode.executeReferenceProvider', main, positionOf(document, 'shared_flag'));
        assert.deepEqual(globals.map(ref => ref.uri.fsPath).sort(), [consumer.fsPath, main.fsPath]);
        const edit = await vscode.commands.executeCommand<vscode.WorkspaceEdit>('vscode.executeDocumentRenameProvider', main, positionOf(document, 'put("b")'), 'insert');
        assert.equal(edit.entries().length, 3);
        assert.ok(await vscode.workspace.applyEdit(edit));
        assert.match(document.getText(), /s\.insert\("b"\)/);
        assert.match((await vscode.workspace.openTextDocument(module)).getText(), /function M\.insert\(key\)/);
        assert.match((await vscode.workspace.openTextDocument(consumer)).getText(), /store\.insert\("a"\)/);
        await assert.rejects(Promise.resolve(vscode.commands.executeCommand('vscode.executeDocumentRenameProvider', main, positionOf(document, 'insert("b")'), 'size')));
        await vscode.workspace.saveAll(false);
      } finally {
        await vscode.workspace.fs.delete(moduleDir, { recursive: true });
        await vscode.workspace.fs.delete(consumer);
        await vscode.workspace.fs.delete(main);
      }
    });

    test('navigates installed Lua packages and unsaved dependency sources', async () => {
      const executable = await automaticInterpreter();
      assert.ok(executable);
      const interpreter = await inspectInterpreter(executable);
      assert.ok(interpreter);
      const config = vscode.workspace.getConfiguration('luaDevtools');
      const previous = config.inspect<string>('luaPath')?.workspaceValue;
      const env = await packageEnvironment(folder().uri.fsPath, interpreter);
      const directory = vscode.Uri.file(join(env.tree, 'share', 'lua', env.version, 'navigation_fixture'));
      const module = vscode.Uri.joinPath(directory, 'init.lua');
      const main = fileUri('package-navigation.lua');
      await vscode.workspace.fs.createDirectory(directory);
      await vscode.workspace.fs.writeFile(module, Buffer.from('local M={}\nfunction M.run() end\nreturn M\n'));
      await vscode.workspace.fs.writeFile(main, Buffer.from('local m=require("navigation_fixture")\nm.run()\n'));
      try {
        await config.update('luaPath', interpreter.path, vscode.ConfigurationTarget.Workspace);
        const document = await vscode.workspace.openTextDocument(main);
        await vscode.window.showTextDocument(document);
        const definitions = () => vscode.commands.executeCommand<vscode.Location[]>('vscode.executeDefinitionProvider', main, positionOf(document, 'run()'));
        const locations = await retry(definitions, values => values.some(value => value.uri.fsPath === module.fsPath), 'installed package definition');
        assert.equal(locations[0].range.start.line, 1);
        const dependency = await vscode.workspace.openTextDocument(module);
        const edit = new vscode.WorkspaceEdit();
        edit.insert(module, new vscode.Position(0, 0), '-- unsaved dependency\n');
        await vscode.workspace.applyEdit(edit);
        assert.ok(dependency.isDirty);
        await retry(definitions, values => values[0]?.uri.fsPath === module.fsPath && values[0].range.start.line === 2, 'unsaved dependency definition');
        const options = await languageEnvironment(interpreter.path, true, [folder().uri.fsPath]);
        assert.equal(options.modulePaths[0].templates[0], join(env.tree, 'share', 'lua', env.version, '?.lua'));
        const untrusted = await languageEnvironment('/must-not-execute', false, [folder().uri.fsPath]);
        assert.deepEqual(untrusted.modulePaths, []);
      } finally {
        await config.update('luaPath', previous, vscode.ConfigurationTarget.Workspace);
        await vscode.workspace.fs.delete(main);
        await vscode.workspace.fs.delete(directory, { recursive: true });
      }
    });

    test('resolves modules through launch.json packagePath', async () => {
      const launchDir = fileUri('.vscode');
      const launch = vscode.Uri.joinPath(launchDir, 'launch.json');
      const moduleDir = fileUri('launch_paths');
      const module = vscode.Uri.joinPath(moduleDir, 'launch_fixture.lua');
      const main = fileUri('launch-navigation.lua');
      await vscode.workspace.fs.createDirectory(launchDir);
      await vscode.workspace.fs.createDirectory(moduleDir);
      await vscode.workspace.fs.writeFile(module, Buffer.from('local M={}\nfunction M.go() end\nreturn M\n'));
      await vscode.workspace.fs.writeFile(main, Buffer.from('local m=require("launch_fixture")\nm.go()\n'));
      // launch.json with comments, as VS Code writes it: the extension must read it through the configuration API.
      await vscode.workspace.fs.writeFile(launch, Buffer.from('{\n  // launch.json packagePath feeds language analysis\n  "version": "0.2.0",\n  "configurations": [\n    { "type": "lua", "request": "launch", "name": "Fixture", "program": "${file}", "packagePath": ["${workspaceFolder}/launch_paths/?.lua"] }\n  ]\n}\n'));
      try {
        const document = await vscode.workspace.openTextDocument(main);
        await vscode.window.showTextDocument(document);
        const definitions = () => vscode.commands.executeCommand<vscode.Location[]>('vscode.executeDefinitionProvider', main, positionOf(document, 'go()'));
        const locations = await retry(definitions, values => values.some(value => value.uri.fsPath === module.fsPath), 'launch.json packagePath definition');
        assert.equal(locations[0].range.start.line, 1);
        const options = await languageEnvironment('', true, [folder().uri.fsPath], () => ({ lua: ['/from-launch/?.lua'], native: ['/from-launch/?.so'] }));
        assert.equal(options.modulePaths[0].templates[0], '/from-launch/?.lua');
        assert.equal(options.modulePaths[0].ctemplates[0], '/from-launch/?.so');
      } finally {
        await vscode.workspace.fs.delete(launchDir, { recursive: true });
        await vscode.workspace.fs.delete(moduleDir, { recursive: true });
        await vscode.workspace.fs.delete(main);
      }
    });

    test('switches package definitions with interpreters and reads external search paths', async function () {
      const interpreters = await discoverInterpreters('');
      if (interpreters.length < 2) { this.skip(); return; }
      const config = vscode.workspace.getConfiguration('luaDevtools');
      const previous = config.inspect<string>('luaPath')?.workspaceValue;
      const external = await mkdtemp(join(tmpdir(), 'lua-navigation-'));
      const externalModule = vscode.Uri.file(join(external, 'external_navigation.lua'));
      const main = fileUri('switch-navigation.lua');
      const created: vscode.Uri[] = [];
      const envKeys = ['LUA_PATH', ...['1', '2', '3', '4', '5'].map(version => `LUA_PATH_5_${version}`)];
      const previousEnv = envKeys.map(key => process.env[key]);
      try {
        for (const key of envKeys) process.env[key] = join(external, '?.lua') + ';;';
        await vscode.workspace.fs.writeFile(externalModule, Buffer.from('local M={}\nfunction M.external() end\nreturn M'));
        await vscode.workspace.fs.writeFile(main, Buffer.from('local m=require("switch_navigation")\nm.run()\nlocal e=require("external_navigation")\ne.external()'));
        const document = await vscode.workspace.openTextDocument(main);
        for (const interpreter of interpreters.slice(0, 2)) {
          const env = await packageEnvironment(folder().uri.fsPath, interpreter);
          const directory = vscode.Uri.file(join(env.tree, 'share', 'lua', env.version));
          const module = vscode.Uri.joinPath(directory, 'switch_navigation.lua');
          await vscode.workspace.fs.createDirectory(directory);
          await vscode.workspace.fs.writeFile(module, Buffer.from('local M={}\nfunction M.run() end\nreturn M'));
          created.push(module);
          await config.update('luaPath', interpreter.path, vscode.ConfigurationTarget.Workspace);
          await retry(
            () => vscode.commands.executeCommand<vscode.Location[]>('vscode.executeDefinitionProvider', main, positionOf(document, 'run()')),
            values => values[0]?.uri.fsPath === module.fsPath,
            `definition for ${interpreter.path}`,
          );
          await retry(
            () => vscode.commands.executeCommand<vscode.Location[]>('vscode.executeDefinitionProvider', main, positionOf(document, 'external()')),
            values => values[0]?.uri.fsPath === externalModule.fsPath,
            'external interpreter search path',
          );
        }
      } finally {
        envKeys.forEach((key, index) => {
          if (previousEnv[index] === undefined) delete process.env[key];
          else process.env[key] = previousEnv[index];
        });
        await config.update('luaPath', previous, vscode.ConfigurationTarget.Workspace);
        for (const module of created) await vscode.workspace.fs.delete(module);
        await vscode.workspace.fs.delete(main);
        await rm(external, { recursive: true, force: true });
      }
    });

    test('hovers a local', async () => {
      const hovers = await retry(
        () => vscode.commands.executeCommand<vscode.Hover[]>('vscode.executeHoverProvider', doc.uri, positionOf(doc, 'result\n')),
        values => values.length > 0,
        'hover after interpreter change',
      );
      const text = hovers.map((h) => h.contents.map((c) => (c as vscode.MarkdownString).value).join('')).join('');
      assert.match(text, /local result/);
    });

    test('completes visible symbols', async () => {
      const list = await retry(
        () => vscode.commands.executeCommand<vscode.CompletionList>(
          'vscode.executeCompletionItemProvider', doc.uri,
          positionOf(doc, 'return result').translate(0, 'return '.length),
        ),
        value => value.items.some(item => (typeof item.label === 'string' ? item.label : item.label.label) === 'result'),
        'completion after interpreter change',
      );
      const labels = list.items.map((i) => (typeof i.label === 'string' ? i.label : i.label.label));
      for (const expected of ['result', 'a', 'b', 'add', 'print']) {
        assert.ok(labels.includes(expected), `completion offers ${expected}`);
      }
    });

    test('offers Run and Debug code lenses', async () => {
      // Interpreter changes restart the language client and briefly unregister providers.
      const lenses = await retry(
        () => vscode.commands.executeCommand<vscode.CodeLens[]>('vscode.executeCodeLensProvider', doc.uri, 10),
        values => values.length > 0,
        'code lenses after interpreter change',
      );
      const commands = lenses.map((l) => l.command?.command);
      assert.ok(commands.includes('luaDevtools.run') && commands.includes('luaDevtools.debug'), `lenses: ${commands}`);
    });

    test('reports syntax errors and clears them again', async () => {
      // Edits stay in the buffer (never saved) and use their own copy of the file.
      const editDoc = await vscode.workspace.openTextDocument(fileUri('edit.lua'));
      const editor = await vscode.window.showTextDocument(editDoc);
      const endLine = editDoc.lineAt(3); // "end" closing add()
      await editor.edit((b) => b.delete(endLine.range));
      await until(() => vscode.languages.getDiagnostics(editDoc.uri).length > 0, 20000, 'diagnostic after edit');
      await editor.edit((b) => b.insert(new vscode.Position(3, 0), 'end'));
      await until(() => vscode.languages.getDiagnostics(editDoc.uri).length === 0, 20000, 'diagnostics cleared');
      await vscode.commands.executeCommand('workbench.action.revertAndCloseActiveEditor');
    });
  });

  suite('debugger', () => {
    suiteTeardown(async () => {
      vscode.debug.removeBreakpoints(vscode.debug.breakpoints);
    });

    test('hits a breakpoint, inspects locals, evaluates and continues', async () => {
      const uri = fileUri('hello.lua');
      vscode.debug.addBreakpoints([new vscode.SourceBreakpoint(new vscode.Location(uri, new vscode.Position(1, 0)))]);

      const { session, recorder } = await startSession(() =>
        vscode.debug.startDebugging(folder(), { type: 'lua', request: 'launch', name: 'e2e', program: uri.fsPath }),
      );

      const stopped = await until(() => recorder.event('stopped'), 30000, 'stopped event');
      assert.equal(stopped.body.reason, 'breakpoint');

      const { stackFrames } = await session.customRequest('stackTrace', { threadId: 1 });
      assert.equal(stackFrames[0].name, 'add');
      assert.equal(stackFrames[0].line, 2);

      const { scopes } = await session.customRequest('scopes', { frameId: stackFrames[0].id });
      const { variables } = await session.customRequest('variables', { variablesReference: scopes[0].variablesReference });
      const byName = Object.fromEntries(variables.map((v: any) => [v.name, v.value]));
      assert.equal(byName.a, '10');
      assert.equal(byName.b, '20');

      const evaluated = await session.customRequest('evaluate', { expression: 'a + b', frameId: stackFrames[0].id, context: 'repl' });
      assert.equal(evaluated.result, '30');

      const table = await session.customRequest('evaluate', { expression: '{a=a,b=b,nested={true,"中文"}}', frameId: stackFrames[0].id, context: 'repl' });
      await vscode.commands.executeCommand('luaDevtools.viewTableJSON', {
        sessionId: session.id, variable: { name: 'sample', type: 'table', variablesReference: table.variablesReference },
      });
      const snapshot = await until(() => vscode.window.visibleTextEditors.find(editor => editor.document.uri.scheme === 'lua-table'), 10000, 'table JSON view');
      assert.equal(snapshot.document.languageId, 'json');
      assert.deepEqual(JSON.parse(snapshot.document.getText()), {a:10,b:20,nested:[true,"中文"]});

      const complex = await session.customRequest('evaluate', { expression: '(function() local t={[2]="two", bytes=string.char(255), n=math.huge}; t.self=t; return t end)()', frameId: stackFrames[0].id });
      await vscode.commands.executeCommand('luaDevtools.viewTableJSON', {
        sessionId: session.id, variable: { name: 'complex', type: 'table', variablesReference: complex.variablesReference },
      });
      const taggedEditor = await until(() => vscode.window.visibleTextEditors.find(editor => editor.document.uri.scheme === 'lua-table' && editor.document.uri.path.includes('complex')), 10000, 'complex table view');
      const tagged = JSON.parse(taggedEditor.document.getText());
      assert.equal(tagged.$format, 'lua-table-v1');
      assert.equal(tagged.root.entries.find((entry: any) => entry.key === 'self').value.$ref, tagged.root.$id);
      assert.equal(tagged.root.entries.find((entry: any) => entry.key === 'bytes').value.hex, 'ff');

      await session.customRequest('continue', { threadId: 1 });
      await until(() => recorder.event('terminated'), 30000, 'terminated event');
      assert.equal(recorder.event('exited').body.exitCode, 0);
      assert.match(recorder.outputs(), /total:\t30/);
    });

    test('runs without debugging', async () => {
      const uri = fileUri('hello.lua');
      vscode.debug.addBreakpoints([new vscode.SourceBreakpoint(new vscode.Location(uri, new vscode.Position(1, 0)))]);
      const { recorder } = await startSession(() => vscode.commands.executeCommand('luaDevtools.run', uri));
      await until(() => recorder.event('terminated'), 30000, 'terminated event');
      assert.equal(recorder.event('stopped'), undefined, 'breakpoints are ignored without debugging');
      assert.match(recorder.outputs(), /total:\t30/);
    });

    test('keeps interactive stdin separate from debugger commands', async () => {
      const uri = fileUri('interactive.lua');
      await vscode.workspace.fs.writeFile(uri, Buffer.from('print("INPUT_READY")\nlocal input=io.read("*a")\nassert(input=="中文\\n")\nprint("INPUT_OK")'));
      try {
        const { session, recorder } = await startSession(() => vscode.debug.startDebugging(folder(), {
          type: 'lua', request: 'launch', name: 'Interactive e2e', program: uri.fsPath, interactive: true,
        }));
        await until(() => recorder.outputs().includes('INPUT_READY'), 15000, 'program input prompt');
        await session.customRequest('lua/input', { text: '中文\n', eof: true });
        await until(() => recorder.event('terminated'), 15000, 'interactive exit');
        assert.equal(recorder.event('exited').body.exitCode, 0, recorder.outputs());
        assert.match(recorder.outputs(), /INPUT_OK/);
      } finally { await vscode.workspace.fs.delete(uri); }
    });

    test('attaches to an embedded-style host and leaves it alive on disconnect', async () => {
      const lua = await automaticInterpreter(); assert.ok(lua);
      const uri = fileUri('attach-host.lua');
      const bootstrap = join(vscode.extensions.getExtension('fqix.lua-devtools')!.extensionPath, 'lua', 'lua-devtools.lua').replace(/\\/g, '/');
      await vscode.workspace.fs.writeFile(uri, Buffer.from(`local before=coroutine.create\nlocal dbg=dofile(${JSON.stringify(bootstrap)}).listen {port=0, token="e2e", ready=function(_, port) print("PORT:"..port); io.stdout:flush() end}\nlocal value=40\nvalue=value+2\ndbg.stop()\nassert(coroutine.create==before)\nprint("HOST_OK:"..value)`));
      const host = spawn(lua, [uri.fsPath], { cwd: folder().uri.fsPath });
      let output = ''; host.stdout.on('data', data => { output += data; }); host.stderr.on('data', data => { output += data; });
      const exited = new Promise<number | null>(resolve => host.once('exit', resolve));
      const breakpoint = new vscode.SourceBreakpoint(new vscode.Location(uri, new vscode.Position(3, 0)));
      vscode.debug.addBreakpoints([breakpoint]);
      try {
        const port = await until(() => { const match = output.match(/PORT:(\d+)/); return match && Number(match[1]); }, 10000, 'attach host port');
        const { session, recorder } = await startSession(() => vscode.debug.startDebugging(folder(), {
          type: 'lua', request: 'attach', name: 'Attach e2e', port, token: 'e2e', cwd: folder().uri.fsPath,
        }));
        await until(() => recorder.event('stopped'), 15000, 'attached breakpoint');
        const { stackFrames } = await session.customRequest('stackTrace', { threadId: 1 });
        const result = await session.customRequest('evaluate', { expression: 'value', frameId: stackFrames[0].id });
        assert.equal(result.result, '40');
        await vscode.debug.stopDebugging(session);
        await until(() => output.includes('HOST_OK:42'), 15000, 'host survives detach');
        assert.equal(await exited, 0, output);
      } finally { host.kill(); vscode.debug.removeBreakpoints([breakpoint]); await vscode.workspace.fs.delete(uri); }
    });

    test('resumes a suspended coroutine and inspects caught coroutine errors', async () => {
      const uri = fileUri('coroutine-control.lua');
      await vscode.workspace.fs.writeFile(uri, Buffer.from('local co=coroutine.create(function() coroutine.yield(); _G.resumed=42 end)\nassert(coroutine.resume(co))\nlocal stop=1\nassert(resumed==42)\nlocal failed=coroutine.create(function() local secret=7; error("CAUGHT") end)\nlocal ok=coroutine.resume(failed)\nassert(not ok)'));
      const breakpoint = new vscode.SourceBreakpoint(new vscode.Location(uri, new vscode.Position(2, 0)));
      vscode.debug.addBreakpoints([breakpoint]);
      try {
        const { session, recorder } = await startSession(() => vscode.debug.startDebugging(folder(), {
          type: 'lua', request: 'launch', name: 'Coroutine e2e', program: uri.fsPath, breakOnCoroutineErrors: true,
        }));
        await until(() => recorder.event('stopped'), 15000, 'caller breakpoint');
        const { threads } = await session.customRequest('threads');
        const suspended = threads.find((thread: any) => thread.name.includes('(suspended)')); assert.ok(suspended);
        const stops = () => recorder.messages.filter(message => message.event === 'stopped');
        await session.customRequest('lua/resumeCoroutine', { threadId: suspended.id });
        await until(() => stops().length >= 2, 15000, 'coroutine resumed');
        await session.customRequest('continue', { threadId: 1 });
        const failed = await until(() => stops().find(message => message.body.reason === 'exception'), 15000, 'caught coroutine error');
        const { stackFrames } = await session.customRequest('stackTrace', { threadId: failed.body.threadId });
        const result = await session.customRequest('evaluate', { expression: 'secret', frameId: stackFrames[0].id });
        assert.equal(result.result, '7');
        await session.customRequest('continue', { threadId: failed.body.threadId });
        await until(() => recorder.event('terminated'), 15000, 'coroutine exit');
        assert.equal(recorder.event('exited').body.exitCode, 0, recorder.outputs());
      } finally { vscode.debug.removeBreakpoints([breakpoint]); await vscode.workspace.fs.delete(uri); }
    });

    for (const framework of [
      { name: 'luaunit', file: 'test_luaunit.lua', test: 'TestExample.testSelected', variable: 'self.value' },
      { name: 'busted', file: 'example_spec.lua', test: 'Example (a+b)? nested selected [1]', variable: 'value' },
    ]) {
      for (const debug of [false, true]) {
        test(`${debug ? 'debugs' : 'runs'} one ${framework.name} test from its CodeLens`, async () => {
          vscode.debug.removeBreakpoints(vscode.debug.breakpoints);
          const uri = fileUri(framework.file);
          const doc = await vscode.workspace.openTextDocument(uri);
          await vscode.window.showTextDocument(doc);
          const command = debug ? 'luaDevtools.debugTest' : 'luaDevtools.runTest';
          const lenses = await retry(
            () => vscode.commands.executeCommand<vscode.CodeLens[]>('vscode.executeCodeLensProvider', uri),
            result => result.some(lens => lens.command?.command === command && lens.command.arguments?.[1] === framework.test),
            'test CodeLens',
          );
          const lens = lenses.find(lens => lens.command?.command === command && lens.command.arguments?.[1] === framework.test)!;
          assert.equal(lens.command!.arguments![2], framework.name);
          const position = positionOf(doc, 'local actual');
          const breakpoint = new vscode.SourceBreakpoint(new vscode.Location(uri, position));
          vscode.debug.addBreakpoints([breakpoint]);
          try {
            const { session, recorder } = await startSession(() => vscode.commands.executeCommand(command, ...lens.command!.arguments!));
            if (debug) {
              const stopped = await until(() => recorder.event('stopped'), 30000, 'test breakpoint');
              assert.equal(stopped.body.reason, 'breakpoint');
              const { stackFrames } = await session.customRequest('stackTrace', { threadId: stopped.body.threadId });
              assert.equal(stackFrames[0].line, position.line + 1);
              const result = await session.customRequest('evaluate', { expression: framework.variable, frameId: stackFrames[0].id });
              assert.equal(result.result, '41');
              await session.customRequest('continue', { threadId: stopped.body.threadId });
            }
            await until(() => recorder.event('terminated'), 30000, 'test completion');
            assert.equal(recorder.event('exited')?.body.exitCode, 0, recorder.outputs());
            if (!debug) assert.equal(recorder.event('stopped'), undefined);
            assert.match(recorder.outputs(), /SETUP/);
            assert.match(recorder.outputs(), /SELECTED/);
            assert.match(recorder.outputs(), /TEARDOWN/);
            assert.doesNotMatch(recorder.outputs(), /OTHER/);
          } finally {
            vscode.debug.removeBreakpoints([breakpoint]);
          }
        });
      }
    }

    test('pauses on a runtime error', async () => {
      const uri = fileUri('error.lua');
      const { session, recorder } = await startSession(() =>
        vscode.debug.startDebugging(folder(), { type: 'lua', request: 'launch', name: 'e2e', program: uri.fsPath }),
      );
      const stopped = await until(() => recorder.event('stopped'), 30000, 'stopped event');
      assert.equal(stopped.body.reason, 'exception');
      assert.match(stopped.body.text, /division by zero/);
      await session.customRequest('continue', { threadId: 1 });
      await until(() => recorder.event('terminated'), 30000, 'terminated event');
      assert.equal(recorder.event('exited').body.exitCode, 1);
    });
  });
});
