import * as vscode from 'vscode';
import { mkdir } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { automaticInterpreter, inspectInterpreter, resolveInterpreter } from './interpreters';
import { applyPackagePaths, installedPackages, installArguments, packageEnvironment, parsePackageSpec, removeArguments,
  validPackageName, validPackageVersion, type PackageLaunch } from './projectPackages';

export interface PackageTarget { executable: string; project: string; name?: string; version?: string }
type PackageAction = 'install' | 'upgrade' | 'remove' | 'version';

export function registerPackageManagement(context: vscode.ExtensionContext): void {
  const busy = new Set<string>();
  const manage = async (action: PackageAction, item?: { executable?: string; packageTarget?: PackageTarget }) => {
    if (!vscode.workspace.isTrusted) { await vscode.commands.executeCommand('workbench.trust.manage'); return; }
    try {
      const folders = vscode.workspace.workspaceFolders;
      if (!folders?.length) { void vscode.window.showErrorMessage(vscode.l10n.t('Open a project folder to install Lua packages.')); return; }
      const target = item?.packageTarget;
      const folder = target ? folders.find(folder => folder.uri.fsPath === target.project)
        : folders.length === 1 ? folders[0] : await vscode.window.showWorkspaceFolderPick();
      if (!folder) return;
      const config = vscode.workspace.getConfiguration('luaDevtools');
      const selected = target?.executable || (typeof item?.executable === 'string' ? item.executable : config.get<string>('luaPath') || await automaticInterpreter());
      const interpreter = selected ? await inspectInterpreter(selected) : undefined;
      if (!interpreter) { void vscode.window.showErrorMessage(vscode.l10n.t('No Lua interpreter found.')); return; }
      const rocks = await resolveInterpreter(config.get<string>('luarocksPath') || 'luarocks');
      if (!rocks) {
        const choice = await vscode.window.showErrorMessage(vscode.l10n.t('LuaRocks was not found. Install LuaRocks or configure luaDevtools.luarocksPath.'), vscode.l10n.t('Configure LuaRocks'));
        if (choice) await vscode.commands.executeCommand('workbench.action.openSettings', 'luaDevtools.luarocksPath');
        return;
      }
      const environment = await packageEnvironment(folder.uri.fsPath, interpreter);
      let name = target?.name;
      let version: string | undefined;
      if (action === 'install') {
        const input = await vscode.window.showInputBox({ title: vscode.l10n.t('Install Lua Package'),
          prompt: vscode.l10n.t('Package for {0} in {1}: name or name@version (for example, luaunit@3.5-1)', interpreter.version, folder.name),
          validateInput: value => parsePackageSpec(value) ? undefined : vscode.l10n.t('Enter a package name or name@version.'),
        });
        if (input === undefined) return;
        const spec = parsePackageSpec(input);
        if (!spec) return;
        ({ name, version } = spec);
      } else {
        if (!name || !validPackageName(name) || typeof target?.version !== 'string' || !validPackageVersion(target.version)) return;
        const packages = await installedPackages(rocks, environment, folder.uri.fsPath);
        if (!packages.some(pkg => pkg.name === name && pkg.version === target.version)) {
          void vscode.window.showErrorMessage(vscode.l10n.t('This package is no longer installed. Refresh the environment.'));
          return;
        }
        if (action === 'remove') version = target.version;
        if (action === 'version') {
          const input = await vscode.window.showInputBox({ title: vscode.l10n.t('Install version of {0}', name), value: target.version,
            prompt: vscode.l10n.t('Enter a LuaRocks version (for example, 3.5-1).'),
            validateInput: value => validPackageVersion(value.trim()) ? undefined : vscode.l10n.t('Enter a valid package version.'),
          });
          if (input === undefined) return;
          version = input.trim();
          if (!validPackageVersion(version)) return;
        }
      }
      if (!name || !vscode.workspace.isTrusted) return;
      if (busy.has(environment.tree)) {
        void vscode.window.showWarningMessage(vscode.l10n.t('A package operation is already running for this environment.'));
        return;
      }
      busy.add(environment.tree);
      try {
        await mkdir(environment.tree, { recursive: true });
        const title = action === 'remove' ? vscode.l10n.t('Uninstall {0} ({1})', name, version!)
          : action === 'upgrade' ? vscode.l10n.t('Upgrade {0}', name)
          : vscode.l10n.t('Install {0} ({1})', name, version || interpreter.version);
        const args = action === 'remove' ? removeArguments(environment, name, version!) : installArguments(environment, name, version);
        const operationId = randomUUID();
        const task = new vscode.Task({ type: 'luaDevtools.package', tree: environment.tree, operationId }, folder,
          title, 'Lua DevTools', new vscode.ProcessExecution(rocks, args, { cwd: folder.uri.fsPath }), []);
        task.presentationOptions = { reveal: vscode.TaskRevealKind.Always, panel: vscode.TaskPanelKind.Dedicated, clear: true };
        const listeners: vscode.Disposable[] = [];
        const finish = () => {
          busy.delete(environment.tree);
          listeners.forEach(listener => listener.dispose());
          void vscode.commands.executeCommand('luaDevtools.refreshInterpreters');
        };
        const matches = (execution: vscode.TaskExecution) => execution.task.definition.operationId === operationId && execution.task.definition.tree === environment.tree;
        listeners.push(vscode.tasks.onDidEndTaskProcess(event => {
          if (!matches(event.execution)) return;
          finish();
          if (event.exitCode === 0) void vscode.window.showInformationMessage(vscode.l10n.t('Package operation completed: {0}', title));
          else void vscode.window.showErrorMessage(vscode.l10n.t('Package operation failed. See the task terminal for details.'));
        }), vscode.tasks.onDidEndTask(event => { if (matches(event.execution)) finish(); }));
        context.subscriptions.push(...listeners);
        try { await vscode.tasks.executeTask(task); }
        catch (error) { finish(); throw error; }
      } catch (error) { busy.delete(environment.tree); throw error; }
    } catch (error) {
      void vscode.window.showErrorMessage(vscode.l10n.t('Cannot manage Lua package: {0}', error instanceof Error ? error.message : String(error)));
    }
  };
  for (const [command, action] of [['installPackage', 'install'], ['upgradePackage', 'upgrade'], ['uninstallPackage', 'remove'], ['installPackageVersion', 'version']] as const) {
    context.subscriptions.push(vscode.commands.registerCommand(`luaDevtools.${command}`, item => manage(action, item)));
  }
}

export async function configureProjectPackages(folder: vscode.WorkspaceFolder | undefined, config: vscode.DebugConfiguration): Promise<vscode.DebugConfiguration> {
  if (!folder || !vscode.workspace.isTrusted) return config;
  const selected = config.luaPath || vscode.workspace.getConfiguration('luaDevtools').get<string>('luaPath') || await automaticInterpreter();
  const interpreter = selected ? await inspectInterpreter(selected) : undefined;
  if (!interpreter) return config;
  const environment = await packageEnvironment(folder.uri.fsPath, interpreter);
  await applyPackagePaths(config as PackageLaunch, environment);
  return config;
}
