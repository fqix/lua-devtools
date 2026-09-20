import * as path from 'node:path';
import { realpath } from 'node:fs/promises';
import * as vscode from 'vscode';
import { probeLibraries, scanModules } from './libraries';
import { packageEnvironment } from './projectPackages';
import type { Interpreter } from './interpreters';
import { automaticInterpreter, discoverInterpreters, resolveInterpreter } from './interpreters';

class EnvironmentItem extends vscode.TreeItem {
  loadChildren?: () => Promise<EnvironmentItem[]>;
  children?: Promise<EnvironmentItem[]>;
  active = false;
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
  getParent(): undefined { return undefined; }
  getTreeItem(item: EnvironmentItem): vscode.TreeItem { return item; }

  async getChildren(item?: EnvironmentItem): Promise<EnvironmentItem[]> {
    if (!vscode.workspace.isTrusted) return [];
    if (item) {
      if (item.loadChildren) {
        try { return await (item.children ??= item.loadChildren()); }
        catch (error) {
          item.children = undefined;
          const warning = new EnvironmentItem(vscode.l10n.t('Cannot read libraries: {0}', error instanceof Error ? error.message : String(error)));
          warning.iconPath = new vscode.ThemeIcon('warning');
          return [warning];
        }
      }
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

  private async libraryChildren(interpreter: Interpreter): Promise<EnvironmentItem[]> {
    const location = new EnvironmentItem(interpreter.path);
    location.iconPath = new vscode.ThemeIcon('file');
    const folders = vscode.workspace.workspaceFolders ?? [];
    const cwd = folders[0]?.uri.fsPath ?? path.dirname(interpreter.path);
    let probe: ReturnType<typeof probeLibraries> | undefined;
    const inspect = () => probe ??= probeLibraries(interpreter.path, cwd);
    const group = (name: string, loader: () => Promise<EnvironmentItem[]>) => {
      const item = new EnvironmentItem(name);
      item.collapsibleState = vscode.TreeItemCollapsibleState.Collapsed;
      item.iconPath = new vscode.ThemeIcon('library');
      item.loadChildren = loader;
      return item;
    };
    const modules = async (luaPath: string, cPath: string, base: string): Promise<EnvironmentItem[]> => {
      const result = await scanModules(luaPath, cPath, base);
      const rows = result.modules.map(module => {
        const row = new EnvironmentItem(module.name);
        row.description = module.native ? vscode.l10n.t('Native C module') : vscode.l10n.t('Lua source');
        row.tooltip = module.file;
        row.iconPath = new vscode.ThemeIcon(module.native ? 'file-binary' : 'file-code');
        row.command = module.native
          ? { command: 'revealFileInOS', title: vscode.l10n.t('Reveal module'), arguments: [vscode.Uri.file(module.file)] }
          : { command: 'vscode.open', title: vscode.l10n.t('Open module'), arguments: [vscode.Uri.file(module.file)] };
        return row;
      });
      if (!rows.length) rows.push(new EnvironmentItem(vscode.l10n.t('No modules found.')));
      if (result.limited || result.unreadable) rows.push(new EnvironmentItem(vscode.l10n.t('Some paths were skipped or the scan limit was reached.')));
      return rows;
    };
    const standard = group(vscode.l10n.t('Standard libraries'), async () => (await inspect()).standard.map(library =>
      group(library.name, async () => library.members.map(member => {
        const row = new EnvironmentItem(member.name);
        row.description = member.kind === 'C' ? vscode.l10n.t('Native C function') : vscode.l10n.t('Lua function');
        row.iconPath = new vscode.ThemeIcon('symbol-method');
        return row;
      })),
    ));
    const thirdParty = group(vscode.l10n.t('Third-party modules'), async () => {
      if (!folders.length) {
        const paths = await inspect();
        return [group(vscode.l10n.t('Interpreter search paths'), () => modules(paths.luaPath, paths.cPath, cwd))];
      }
      const roots = folders.map(folder => ({ name: folder.name, cwd: folder.uri.fsPath }));
      const rows = roots.map(root => group(root.name, async () => {
        const paths = await inspect();
        const env = await packageEnvironment(root.cwd, interpreter);
        const lua = [path.join(env.tree, 'share', 'lua', env.version, '?.lua'), path.join(env.tree, 'share', 'lua', env.version, '?', 'init.lua')].join(';');
        const native = path.join(env.tree, 'lib', 'lua', env.version, process.platform === 'win32' ? '?.dll' : '?.so');
        return [
          group(vscode.l10n.t('Project packages'), () => modules(lua, native, root.cwd)),
          group(vscode.l10n.t('Interpreter search paths'), () => modules(paths.luaPath, paths.cPath, root.cwd)),
        ];
      }));
      return rows;
    });
    return [location, standard, thirdParty];
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
      row.active = active;
      row.collapsibleState = active ? vscode.TreeItemCollapsibleState.Expanded : vscode.TreeItemCollapsibleState.Collapsed;
      row.loadChildren = () => this.libraryChildren(interpreter);
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
      if (event.affectsConfiguration('luaDevtools.luaPath')) {
        provider.refresh();
        if (view.visible) void provider.getChildren().then(rows => {
          const selected = rows.find(row => row.active);
          if (selected) return view.reveal(selected, { expand: 1, select: true, focus: false });
        }).catch(() => {});
      }
    }),
    vscode.workspace.onDidChangeWorkspaceFolders(() => provider.refresh()),
    vscode.workspace.onDidGrantWorkspaceTrust(() => provider.refresh()),
  );
}
