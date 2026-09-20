import * as fs from 'node:fs';
import * as path from 'node:path';
import * as vscode from 'vscode';
import { LanguageClient, TransportKind } from 'vscode-languageclient/node';

/**
 * Thin glue between VS Code and the Go binaries in bin/:
 *   bin/lua-dap  Debug Adapter Protocol server (spawned per debug session)
 *   bin/lua-lsp  Language Server Protocol server (started on activation)
 */

function binaryPath(context: vscode.ExtensionContext, name: string): string | undefined {
  const exe = process.platform === 'win32' ? `${name}.exe` : name;
  const p = path.join(context.extensionPath, 'bin', exe);
  return fs.existsSync(p) ? p : undefined;
}

export function activate(context: vscode.ExtensionContext): void {
  registerDebugger(context);
  registerRunCommands(context);
  startLanguageClient(context);
}

// Targets of the "Run" / "Debug" code lenses (served by lua-lsp) and editor title buttons.
function registerRunCommands(context: vscode.ExtensionContext): void {
  const launch = async (target: unknown, noDebug: boolean, testName?: string, framework?: string) => {
    const uri = target instanceof vscode.Uri ? target : typeof target === 'string' ? vscode.Uri.parse(target) : vscode.window.activeTextEditor?.document.uri;
    if (!uri || uri.scheme !== 'file') {
      void vscode.window.showErrorMessage(vscode.l10n.t('Open a Lua file first.'));
      return;
    }
    if (!await vscode.workspace.saveAll(false)) return;
    const folder = vscode.workspace.getWorkspaceFolder(uri);
    const busted = framework === 'busted';
    // Busted filters are Lua patterns, not regular expressions. Anchor and escape
    // every magic character so selecting one test never runs a similarly named one.
    const filter = testName !== undefined ? '^' + testName.replace(/[\^$()%.\[\]*+?\-]/g, '%$&') + '$' : '';
    await vscode.debug.startDebugging(
      folder,
      {
        type: 'lua', request: 'launch',
        name: testName ? `${noDebug ? vscode.l10n.t('Run Lua test') : vscode.l10n.t('Debug Lua test')}: ${testName}`
          : noDebug ? vscode.l10n.t('Run Lua file') : vscode.l10n.t('Debug Lua file'),
        program: busted ? path.join(context.extensionPath, 'lua', 'busted-runner.lua') : uri.fsPath,
        ...(busted ? { cwd: folder?.uri.fsPath ?? path.dirname(uri.fsPath), args: ['--ignore-lua', '--filter=' + filter, '--', uri.fsPath] }
          : testName ? { args: [testName] } : {}),
      },
      { noDebug },
    );
  };
  context.subscriptions.push(
    vscode.commands.registerCommand('luaDevtools.run', (target?: unknown) => launch(target, true)),
    vscode.commands.registerCommand('luaDevtools.debug', (target?: unknown) => launch(target, false)),
    ...(['runTest', 'debugTest'] as const).map(command => vscode.commands.registerCommand(`luaDevtools.${command}`,
      (target: unknown, testName: unknown, framework: unknown = 'luaunit') => {
        if (typeof testName !== 'string' || testName.includes('\0')) return;
        if (framework !== 'luaunit' && framework !== 'busted') return;
        if (framework === 'luaunit' && !/^[A-Za-z_]\w*(\.[A-Za-z_]\w*)?$/.test(testName)) return;
        return launch(target, command === 'runTest', testName, framework);
      })),
  );
}

export async function deactivate(): Promise<void> {
  await client?.stop();
}

let client: LanguageClient | undefined;

function startLanguageClient(context: vscode.ExtensionContext): void {
  const lsp = binaryPath(context, 'lua-lsp');
  if (!lsp) {
    void vscode.window.showErrorMessage(vscode.l10n.t('{0} not found. Run `npm run build:go` first.', 'bin/lua-lsp'));
    return;
  }
  client = new LanguageClient(
    'luaDevtools',
    vscode.l10n.t('Lua DevTools Language Server'),
    { command: lsp, transport: TransportKind.stdio },
    {
      documentSelector: [{ scheme: 'file', language: 'lua' }],
      initializationOptions: () => ({
        luaPath: vscode.workspace.getConfiguration('luaDevtools').get<string>('luaPath'),
        useInterpreter: vscode.workspace.isTrusted,
        workspaceRoots: vscode.workspace.workspaceFolders?.map(folder => folder.uri.fsPath) ?? [],
      }),
    },
  );
  // `luaDevtools.trace.server` in settings controls the JSON-RPC trace in the Output panel.
  void client.start();
  context.subscriptions.push(
    { dispose: () => void client?.stop() },
    vscode.workspace.onDidChangeConfiguration(event => {
      if (event.affectsConfiguration('luaDevtools.luaPath')) void client?.restart();
    }),
    vscode.workspace.onDidGrantWorkspaceTrust(() => void client?.restart()),
  );
}

function registerDebugger(context: vscode.ExtensionContext): void {
  context.subscriptions.push(
    vscode.debug.registerDebugAdapterDescriptorFactory('lua', {
      createDebugAdapterDescriptor() {
        const dap = binaryPath(context, 'lua-dap');
        if (!dap) {
          void vscode.window.showErrorMessage(vscode.l10n.t('{0} not found. Run `npm run build:go` first.', 'bin/lua-dap'));
          return undefined;
        }
        const args = ['-debugger-script', path.join(context.extensionPath, 'lua', 'debugger.lua')];
        const luaPath = vscode.workspace.getConfiguration('luaDevtools').get<string>('luaPath');
        if (luaPath) {
          args.push('-lua', luaPath);
        }
        return new vscode.DebugAdapterExecutable(dap, args);
      },
    }),
  );

  // Without a launch.json, F5 on the active .lua file just works.
  context.subscriptions.push(
    vscode.debug.registerDebugConfigurationProvider('lua', {
      resolveDebugConfiguration(_folder, config) {
        if (!config.type && !config.request && !config.name) {
          const editor = vscode.window.activeTextEditor;
          if (editor && editor.document.languageId === 'lua') {
            config.type = 'lua';
            config.name = vscode.l10n.t('Debug Lua file');
            config.request = 'launch';
            config.program = '${file}';
          }
        }
        if (!config.program) {
          void vscode.window.showErrorMessage(vscode.l10n.t('Open a Lua file first, or set "program" in launch.json.'));
          return undefined;
        }
        return config;
      },
    }),
  );
}
