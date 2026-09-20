// End-to-end: the built extension inside a real VS Code, talking to the real
// bin/lua-lsp and bin/lua-dap servers over a scratch workspace.
import assert from 'node:assert/strict';
import { join } from 'node:path';
import * as vscode from 'vscode';
import { formatLua } from '../formatter';
import { automaticInterpreter, inspectInterpreter } from '../interpreters';
import { packageEnvironment } from '../projectPackages';
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
    for (const name of ['luaDevtools.run', 'luaDevtools.debug', 'luaDevtools.selectInterpreter', 'luaDevtools.viewTableJSON', 'luaDevtools.refreshInterpreters', 'luaDevtools.enterInterpreterPath', 'luaDevtools.installPackage']) {
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

    test('hovers a local', async () => {
      const hovers = await vscode.commands.executeCommand<vscode.Hover[]>(
        'vscode.executeHoverProvider',
        doc.uri,
        positionOf(doc, 'result\n'),
      );
      const text = hovers.map((h) => h.contents.map((c) => (c as vscode.MarkdownString).value).join('')).join('');
      assert.match(text, /local result/);
    });

    test('completes visible symbols', async () => {
      const list = await vscode.commands.executeCommand<vscode.CompletionList>(
        'vscode.executeCompletionItemProvider',
        doc.uri,
        positionOf(doc, 'return result').translate(0, 'return '.length),
      );
      const labels = list.items.map((i) => (typeof i.label === 'string' ? i.label : i.label.label));
      for (const expected of ['result', 'a', 'b', 'add', 'print']) {
        assert.ok(labels.includes(expected), `completion offers ${expected}`);
      }
    });

    test('offers Run and Debug code lenses', async () => {
      const lenses = await vscode.commands.executeCommand<vscode.CodeLens[]>('vscode.executeCodeLensProvider', doc.uri, 10);
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
