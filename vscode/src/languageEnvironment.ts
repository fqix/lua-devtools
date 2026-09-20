import * as path from 'node:path';
import { automaticInterpreter, inspectInterpreter } from './interpreters';
import { probeLibraries } from './libraries';
import { packageEnvironment } from './projectPackages';

export interface LanguageEnvironment {
  luaPath: string;
  useInterpreter: boolean;
  workspaceRoots: string[];
  modulePaths: { root: string; templates: string[] }[];
}

/** Match managed package precedence without loading any project or package code. */
export async function languageEnvironment(luaPath: string, trusted: boolean, roots: string[]): Promise<LanguageEnvironment> {
  const options: LanguageEnvironment = { luaPath, useInterpreter: trusted, workspaceRoots: roots, modulePaths: [] };
  if (!trusted) return options;
  const executable = luaPath || await automaticInterpreter();
  const interpreter = executable ? await inspectInterpreter(executable) : undefined;
  if (!interpreter) return options;
  options.luaPath = interpreter.path;
  options.modulePaths = await Promise.all((roots.length ? roots : ['']).map(async root => {
    const templates: string[] = [];
    if (root) {
      try {
        const env = await packageEnvironment(root, interpreter);
        templates.push(path.join(env.tree, 'share', 'lua', env.version, '?.lua'), path.join(env.tree, 'share', 'lua', env.version, '?', 'init.lua'));
      } catch { /* An unusable package directory must not disable local analysis. */ }
    }
    try {
      const probe = await probeLibraries(interpreter.path, root || path.dirname(interpreter.path));
      templates.push(...probe.luaPath.split(';').filter(Boolean));
    } catch { /* Keep managed packages and local analysis when probing fails. */ }
    return { root, templates };
  }));
  return options;
}
