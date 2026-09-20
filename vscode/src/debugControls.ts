import * as path from 'node:path';
import * as vscode from 'vscode';

export function registerDebugControls(context: vscode.ExtensionContext): void {
  const input = async (eof: boolean) => {
    const session = vscode.debug.activeDebugSession;
    if (session?.type !== 'lua' || session.configuration.request !== 'launch' || !session.configuration.interactive) {
      void vscode.window.showErrorMessage(vscode.l10n.t('Start a Lua launch session with interactive: true to send input.'));
      return;
    }
    const text = eof ? '' : await vscode.window.showInputBox({ title: vscode.l10n.t('Send Lua program input'), prompt: vscode.l10n.t('Send a line to the program’s standard input.') });
    if (text === undefined) return;
    try { await session.customRequest('lua/input', { text: eof ? '' : text + '\n', eof }); }
    catch (error) { void vscode.window.showErrorMessage(String(error)); }
  };
  context.subscriptions.push(
    vscode.commands.registerCommand('luaDevtools.sendInput', () => input(false)),
    vscode.commands.registerCommand('luaDevtools.closeInput', () => input(true)),
    vscode.commands.registerCommand('luaDevtools.copyAttachSnippet', async () => {
      const bootstrap = path.join(context.extensionPath, 'lua', 'lua-devtools.lua').replace(/\\/g, '/');
      await vscode.env.clipboard.writeText(`local debugger = dofile(${JSON.stringify(bootstrap)}).listen { host = "127.0.0.1", port = 8172 }\n`);
      void vscode.window.showInformationMessage(vscode.l10n.t('Lua attach bootstrap copied. Run it in a host with LuaSocket, then start an attach configuration.'));
    }),
    vscode.commands.registerCommand('luaDevtools.resumeCoroutine', async () => {
      const session = vscode.debug.activeDebugSession;
      if (session?.type !== 'lua') return;
      try {
        const response = await session.customRequest('threads');
        const suspended = (response.threads as { id: number; name: string }[]).filter(thread => thread.name.includes('(suspended)'));
        if (!suspended.length) { void vscode.window.showInformationMessage(vscode.l10n.t('No suspended Lua coroutines.')); return; }
        const selected = await vscode.window.showQuickPick(suspended.map(thread => ({ label: thread.name, id: thread.id })), { title: vscode.l10n.t('Resume a suspended coroutine') });
        if (selected) await session.customRequest('lua/resumeCoroutine', { threadId: selected.id });
      } catch (error) { void vscode.window.showErrorMessage(String(error)); }
    }),
  );
}
