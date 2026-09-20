import * as path from 'node:path';
import * as vscode from 'vscode';
import { formatLua } from './formatter';

export function registerFormatting(context: vscode.ExtensionContext): void {
  const output = vscode.window.createOutputChannel('Lua DevTools: StyLua');
  context.subscriptions.push(output, vscode.languages.registerDocumentFormattingEditProvider(
    [{ language: 'lua', scheme: 'file' }, { language: 'lua', scheme: 'untitled' }], {
      async provideDocumentFormattingEdits(document, _options, token) {
        if (!vscode.workspace.isTrusted || token.isCancellationRequested) return [];
        const config = vscode.workspace.getConfiguration('luaDevtools', document.uri);
        const executable = config.get<string>('styluaPath')?.trim() || 'stylua';
        const folder = vscode.workspace.getWorkspaceFolder(document.uri) ?? (document.isUntitled ? vscode.workspace.workspaceFolders?.[0] : undefined);
        const cwd = folder?.uri.fsPath ?? (document.isUntitled ? context.extensionPath : path.dirname(document.uri.fsPath));
        const filename = document.isUntitled ? path.join(cwd, 'untitled.lua') : document.uri.fsPath;
        const source = document.getText();
        const version = document.version;
        const controller = new AbortController();
        const subscription = token.onCancellationRequested(() => controller.abort());
        try {
          const formatted = await formatLua(executable, source, filename, cwd, controller.signal);
          if (token.isCancellationRequested || document.isClosed || document.version !== version || formatted === source) return [];
          return [vscode.TextEdit.replace(new vscode.Range(document.positionAt(0), document.positionAt(source.length)), formatted)];
        } catch (error) {
          if (token.isCancellationRequested) return [];
          const message = error instanceof Error ? error.message : String(error);
          output.appendLine(`${document.uri.toString()}: ${message}`);
          void vscode.window.showErrorMessage(vscode.l10n.t('Lua formatting failed. Install StyLua or check luaDevtools.styluaPath and the StyLua output channel.'));
          return [];
        } finally { subscription.dispose(); }
      },
    },
  ));
}
