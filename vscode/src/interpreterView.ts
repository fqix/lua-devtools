import * as path from 'node:path';
import { realpath } from 'node:fs/promises';
import * as vscode from 'vscode';
import { automaticInterpreter, discoverInterpreters, resolveInterpreter } from './interpreters';

class EnvironmentItem extends vscode.TreeItem {
  constructor(label: string, readonly executable?: string) {
    super(label, executable ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None);
  }
}

export class InterpreterTreeProvider implements vscode.TreeDataProvider<EnvironmentItem>, vscode.Disposable {
  private readonly changed = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this.changed.event;
  private pending?: Promise<EnvironmentItem[]>;
  private revision = 0;

  dispose(): void { this.revision++; this.changed.dispose(); }
  refresh(): void { this.revision++; this.pending = undefined; this.changed.fire(); }
  getTreeItem(item: EnvironmentItem): vscode.TreeItem { return item; }

  async getChildren(item?: EnvironmentItem): Promise<EnvironmentItem[]> {
    if (item) {
      if (!item.executable) return [];
      const child = new EnvironmentItem(item.executable);
      child.iconPath = new vscode.ThemeIcon('file');
      child.tooltip = item.executable;
      return [child];
    }
    if (!vscode.workspace.isTrusted) return [];
    const revision = this.revision;
    this.pending ??= this.load();
    const items = await this.pending;
    // Ignore discovery that finished after a configuration change or refresh.
    return revision === this.revision ? items : this.getChildren();
  }

  private async load(): Promise<EnvironmentItem[]> {
    const configured = vscode.workspace.getConfiguration('luaDevtools').get<string>('luaPath') ?? '';
    const [interpreters, selected] = await Promise.all([
      discoverInterpreters(configured),
      configured ? resolveInterpreter(configured) : automaticInterpreter(),
    ]);
    const canonical = async (file: string) => realpath(file).catch(() => file);
    const selectedPath = selected ? await canonical(selected) : undefined;
    const automatic = new EnvironmentItem(vscode.l10n.t('Use automatic detection'));
    automatic.id = 'automatic';
    automatic.description = configured ? undefined : vscode.l10n.t('Currently selected');
    automatic.iconPath = new vscode.ThemeIcon(configured ? 'wand' : 'check');
    automatic.tooltip = 'lua5.4 → Homebrew lua@5.4 → lua';
    automatic.command = { command: 'luaDevtools.selectInterpreter', title: vscode.l10n.t('Select Lua Interpreter'), arguments: [''] };
    let foundSelected = false;
    const rows = await Promise.all(interpreters.map(async interpreter => {
      const active = await canonical(interpreter.path) === selectedPath;
      if (active) foundSelected = true;
      const row = new EnvironmentItem(interpreter.version, interpreter.path);
      row.id = interpreter.path;
      row.description = active ? vscode.l10n.t('Currently selected') : path.dirname(interpreter.path);
      row.tooltip = `${interpreter.version}\n${interpreter.path}`;
      row.iconPath = new vscode.ThemeIcon(active ? 'check' : 'terminal');
      row.contextValue = 'luaInterpreter';
      row.command = { command: 'luaDevtools.selectInterpreter', title: vscode.l10n.t('Select Lua Interpreter'), arguments: [interpreter.path] };
      return row;
    }));
    if (configured && !foundSelected) {
      const missing = new EnvironmentItem(vscode.l10n.t('Lua interpreter unavailable: {0}', configured));
      missing.iconPath = new vscode.ThemeIcon('warning');
      missing.command = { command: 'luaDevtools.enterInterpreterPath', title: vscode.l10n.t('Enter interpreter path…') };
      rows.unshift(missing);
    }
    if (!interpreters.length) {
      const empty = new EnvironmentItem(vscode.l10n.t('No Lua interpreter found.'));
      empty.iconPath = new vscode.ThemeIcon('info');
      rows.push(empty);
    }
    return [automatic, ...rows];
  }
}

export function registerInterpreterView(context: vscode.ExtensionContext, refreshStatus: () => Promise<void>): void {
  const provider = new InterpreterTreeProvider();
  const view = vscode.window.createTreeView('luaDevtools.environments', { treeDataProvider: provider });
  context.subscriptions.push(provider, view,
    vscode.commands.registerCommand('luaDevtools.refreshInterpreters', () => { provider.refresh(); return refreshStatus(); }),
    vscode.workspace.onDidChangeConfiguration(event => {
      if (event.affectsConfiguration('luaDevtools.luaPath')) provider.refresh();
    }),
    vscode.workspace.onDidGrantWorkspaceTrust(() => provider.refresh()),
  );
}
