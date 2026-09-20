import * as vscode from 'vscode';

interface VariableContext {
  sessionId?: string;
  variable?: { name?: string; type?: string; variablesReference?: number };
}

// Use the clicked variable's owning session, rather than whichever session happens
// to be focused when the asynchronous snapshot request completes.
export function registerTableView(context: vscode.ExtensionContext): void {
  const sessions = new Map<string, vscode.DebugSession>();
  if (vscode.debug.activeDebugSession) sessions.set(vscode.debug.activeDebugSession.id, vscode.debug.activeDebugSession);
  const snapshots = new Map<string, string>();
  let serial = 0;
  context.subscriptions.push(
    vscode.debug.onDidStartDebugSession(session => sessions.set(session.id, session)),
    vscode.debug.onDidTerminateDebugSession(session => sessions.delete(session.id)),
    vscode.workspace.registerTextDocumentContentProvider('lua-table', {
      provideTextDocumentContent: uri => snapshots.get(uri.toString()) ?? '',
    }),
    vscode.workspace.onDidCloseTextDocument(document => snapshots.delete(document.uri.toString())),
    { dispose: () => { sessions.clear(); snapshots.clear(); } },
  );
  context.subscriptions.push(vscode.commands.registerCommand('luaDevtools.viewTableJSON', async (target?: VariableContext) => {
    const variable = target?.variable;
    const session = target?.sessionId ? sessions.get(target.sessionId) : undefined;
    if (!session || session.type !== 'lua' || variable?.type !== 'table' || !Number.isSafeInteger(variable.variablesReference) || variable.variablesReference! <= 0) {
      void vscode.window.showErrorMessage(vscode.l10n.t('Pause Lua debugging and right-click a table in Variables.'));
      return;
    }
    try {
      const result = await session.customRequest('lua/snapshot', { variablesReference: variable.variablesReference });
      if (typeof result?.json !== 'string') throw new Error(vscode.l10n.t('Invalid table snapshot response.'));
      const filename = (variable.name ?? 'table').replace(/[^a-zA-Z0-9_-]/g, '_').slice(0, 80) || 'table';
      const uri = vscode.Uri.from({ scheme: 'lua-table', path: `/${filename}-${++serial}.json` });
      snapshots.set(uri.toString(), result.json);
      try {
        const document = await vscode.workspace.openTextDocument(uri);
        await vscode.window.showTextDocument(document, { viewColumn: vscode.ViewColumn.Beside, preview: false });
      } catch (error) { snapshots.delete(uri.toString()); throw error; }
    } catch (error) {
      void vscode.window.showErrorMessage(vscode.l10n.t('Cannot view Lua table: {0}', error instanceof Error ? error.message : String(error)));
    }
  }));
}
