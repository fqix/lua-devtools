import * as path from 'node:path';
import * as vscode from 'vscode';
import { registerInterpreterView } from './interpreterView';
import { automaticInterpreter, discoverInterpreters, inspectInterpreter } from './interpreters';

export function registerInterpreterSelection(context: vscode.ExtensionContext): void {
  const status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 100);
  status.command = 'luaDevtools.selectInterpreter';
  status.name = vscode.l10n.t('Lua Interpreter');
  let revision = 0;
  const refresh = async () => {
    const current = ++revision;
    if (vscode.window.activeTextEditor?.document.languageId !== 'lua') { status.hide(); return; }
    const configured = vscode.workspace.getConfiguration('luaDevtools').get<string>('luaPath') ?? '';
    status.text = `$(terminal) ${configured ? path.basename(configured) : vscode.l10n.t('Select Lua Interpreter')}`;
    status.tooltip = configured || vscode.l10n.t('Select Lua Interpreter');
    status.show();
    if (!vscode.workspace.isTrusted) return;
    const file = configured || await automaticInterpreter();
    const interpreter = file ? await inspectInterpreter(file) : undefined;
    if (current !== revision) return;
    status.text = `$(terminal) ${interpreter?.version ?? vscode.l10n.t('Select Lua Interpreter')}`;
    status.tooltip = interpreter?.path ?? (configured ? vscode.l10n.t('Lua interpreter unavailable: {0}', configured) : vscode.l10n.t('No Lua interpreter found.'));
  };
  registerInterpreterView(context, refresh);
  context.subscriptions.push(status,
    vscode.window.onDidChangeActiveTextEditor(() => void refresh()),
    vscode.workspace.onDidChangeConfiguration(event => { if (event.affectsConfiguration('luaDevtools.luaPath')) void refresh(); }),
    vscode.workspace.onDidGrantWorkspaceTrust(() => void refresh()),
    vscode.commands.registerCommand('luaDevtools.enterInterpreterPath', () => select(true)),
    vscode.commands.registerCommand('luaDevtools.selectInterpreter', (value?: unknown) => select(false, typeof value === 'string' ? value : undefined)),
  );
  async function select(manual: boolean, requested?: string): Promise<void> {
    if (!vscode.workspace.isTrusted) {
      await vscode.commands.executeCommand('workbench.trust.manage');
      return;
    }
    const config = vscode.workspace.getConfiguration('luaDevtools');
    const configured = config.get<string>('luaPath') ?? '';
    let value = requested;
    if (!manual && value === undefined) {
      const interpreters = await vscode.window.withProgress({ location: vscode.ProgressLocation.Window, title: vscode.l10n.t('Finding Lua interpreters…') }, () => discoverInterpreters(configured));
      const selected = await vscode.window.showQuickPick([
        { label: vscode.l10n.t('Enter interpreter path…'), value: undefined },
        { label: vscode.l10n.t('Use automatic detection'), description: 'lua5.4 → Homebrew lua@5.4 → lua', value: '' },
        ...interpreters.map(interpreter => ({ label: interpreter.version, description: interpreter.path,
          detail: interpreter.path === configured ? vscode.l10n.t('Currently selected') : undefined, value: interpreter.path })),
      ], { title: vscode.l10n.t('Select Lua Interpreter'), matchOnDescription: true });
      if (!selected) return;
      value = selected.value;
    }
    if (value === undefined) {
      value = await vscode.window.showInputBox({ title: vscode.l10n.t('Enter interpreter path…'),
        prompt: vscode.l10n.t('Enter an absolute executable path or a command on PATH.'), value: configured });
      if (!value?.trim()) return;
    }
    if (!vscode.workspace.isTrusted) return;
    if (value) {
      const interpreter = await inspectInterpreter(value.trim());
      if (!interpreter) { void vscode.window.showErrorMessage(vscode.l10n.t('Lua interpreter unavailable: {0}', value)); return; }
      value = interpreter.path;
    }
    await config.update('luaPath', value, vscode.workspace.workspaceFolders?.length ? vscode.ConfigurationTarget.Workspace : vscode.ConfigurationTarget.Global);
  }
  void refresh();
}
