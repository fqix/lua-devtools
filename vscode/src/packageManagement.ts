import * as vscode from 'vscode';
import { mkdir } from 'node:fs/promises';
import { automaticInterpreter, inspectInterpreter, resolveInterpreter } from './interpreters';
import { applyPackagePaths, installArguments, packageEnvironment, validPackageName, type PackageLaunch } from './projectPackages';

export function registerPackageManagement(context: vscode.ExtensionContext): void {
  context.subscriptions.push(vscode.commands.registerCommand('luaDevtools.installPackage', async (item?: { executable?: string }) => {
    if (!vscode.workspace.isTrusted) { await vscode.commands.executeCommand('workbench.trust.manage'); return; }
    try {
      const folders = vscode.workspace.workspaceFolders;
      if (!folders?.length) { void vscode.window.showErrorMessage(vscode.l10n.t('Open a project folder to install Lua packages.')); return; }
      const folder = folders.length === 1 ? folders[0] : await vscode.window.showWorkspaceFolderPick();
      if (!folder) return;
      const config = vscode.workspace.getConfiguration('luaDevtools');
      const selected = typeof item?.executable === 'string' ? item.executable : config.get<string>('luaPath') || await automaticInterpreter();
      const interpreter = selected ? await inspectInterpreter(selected) : undefined;
      if (!interpreter) { void vscode.window.showErrorMessage(vscode.l10n.t('No Lua interpreter found.')); return; }
      const rocks = await resolveInterpreter(config.get<string>('luarocksPath') || 'luarocks');
      if (!rocks) {
        const action = await vscode.window.showErrorMessage(vscode.l10n.t('LuaRocks was not found. Install LuaRocks or configure luaDevtools.luarocksPath.'), vscode.l10n.t('Configure LuaRocks'));
        if (action) await vscode.commands.executeCommand('workbench.action.openSettings', 'luaDevtools.luarocksPath');
        return;
      }
      const environment = await packageEnvironment(folder.uri.fsPath, interpreter);
      const name = await vscode.window.showInputBox({ title: vscode.l10n.t('Install Lua Package'),
        prompt: vscode.l10n.t('Package for {0} in {1} (for example, luasocket)', interpreter.version, folder.name),
        validateInput: value => validPackageName(value.trim()) ? undefined : vscode.l10n.t('Enter a package name, such as luasocket or luaunit.'),
      });
      if (!name?.trim() || !vscode.workspace.isTrusted) return;
      await mkdir(environment.tree, { recursive: true });
      const task = new vscode.Task({ type: 'luaDevtools.installPackage', tree: environment.tree }, folder,
        vscode.l10n.t('Install {0} ({1})', name.trim(), interpreter.version), 'Lua DevTools',
        new vscode.ProcessExecution(rocks, installArguments(environment, name.trim()), { cwd: folder.uri.fsPath }), []);
      task.presentationOptions = { reveal: vscode.TaskRevealKind.Always, panel: vscode.TaskPanelKind.Dedicated, clear: true };
      let execution: vscode.TaskExecution | undefined;
      const listener = vscode.tasks.onDidEndTaskProcess(event => {
        if (event.execution !== execution) return;
        listener.dispose();
        if (event.exitCode === 0) void vscode.window.showInformationMessage(vscode.l10n.t('Installed {0} for {1}. New run and debug sessions will load it.', name.trim(), interpreter.version));
        else void vscode.window.showErrorMessage(vscode.l10n.t('Lua package installation failed. See the task terminal for details.'));
      });
      context.subscriptions.push(listener);
      try { execution = await vscode.tasks.executeTask(task); }
      catch (error) { listener.dispose(); throw error; }
    } catch (error) {
      void vscode.window.showErrorMessage(vscode.l10n.t('Cannot install Lua package: {0}', error instanceof Error ? error.message : String(error)));
    }
  }));
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
