import * as os from 'node:os';
import * as path from 'node:path';

/**
 * `packagePath` (or `packageCPath`) templates of the Lua launch configurations in
 * one workspace folder, so language analysis resolves the same modules a debug
 * session loads. Input is the raw `launch.configurations` value; it is never executed.
 *
 * Relative templates are resolved the way the debuggee resolves them: against the
 * configuration's `cwd`. Without a `cwd` the session runs in the program's
 * directory, which differs per launch, so such templates stay relative and the
 * language server anchors them at the workspace folder.
 */
export function launchPackagePaths(configurations: unknown, root: string, env: NodeJS.ProcessEnv = process.env, field: 'packagePath' | 'packageCPath' = 'packagePath'): string[] {
  if (!Array.isArray(configurations)) return [];
  const templates: string[] = [];
  const seen = new Set<string>();
  for (const configuration of configurations) {
    if (!configuration || typeof configuration !== 'object' || (configuration as { type?: unknown }).type !== 'lua') continue;
    const { cwd, [field]: entries } = configuration as Record<string, unknown>;
    if (!Array.isArray(entries)) continue;
    // A cwd that depends on the active editor makes its relative templates unknowable.
    const base = typeof cwd === 'string' && cwd.trim() ? substituteVariables(cwd, root, env) : root;
    for (const entry of entries) {
      if (typeof entry !== 'string' || !entry.trim()) continue;
      let resolved = substituteVariables(entry, root, env);
      if (resolved === undefined) continue;
      if (!path.isAbsolute(resolved)) {
        if (base === undefined) continue;
        if (base !== root) resolved = path.join(base, resolved);
      }
      if (seen.has(resolved)) continue;
      seen.add(resolved);
      templates.push(resolved);
    }
  }
  return templates;
}

// Only variables with a single value per workspace folder are substituted.
// Entries depending on the active editor (${file}, ${selectedText}, …) or on
// another folder (${workspaceFolder:name}) are skipped rather than guessed.
function substituteVariables(template: string, root: string, env: NodeJS.ProcessEnv): string | undefined {
  let unresolved = false;
  const value = template.replace(/\$\{([^}]*)\}/g, (match, name: string) => {
    switch (name) {
      case 'workspaceFolder':
      case 'workspaceRoot':
        return root;
      case 'workspaceFolderBasename':
        return path.basename(root);
      case 'userHome':
        return os.homedir();
      case 'pathSeparator':
      case '/':
        return path.sep;
    }
    if (name.startsWith('env:')) return env[name.slice('env:'.length)] ?? '';
    unresolved = true;
    return match;
  });
  return unresolved ? undefined : path.normalize(value);
}
