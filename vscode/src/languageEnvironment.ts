import * as path from 'node:path';
import { automaticInterpreter, inspectInterpreter } from './interpreters';
import { probeLibraries } from './libraries';
import { packageEnvironment } from './projectPackages';

export interface LanguageEnvironment {
  luaPath: string;
  useInterpreter: boolean;
  workspaceRoots: string[];
  modulePaths: { root: string; templates: string[]; ctemplates: string[] }[];
  /** Opt-in: the language server may load C modules to list their members. */
  probeNativeModules: boolean;
}

export interface LaunchPaths { lua: string[]; native: string[] }

/**
 * Match run/debug precedence without loading any project or package code:
 * launch `packagePath` first, then managed packages, then the interpreter's own paths.
 * `launchPaths` supplies the resolved launch templates of one workspace folder.
 * Native templates are only used when `probeNativeModules` is on.
 */
export async function languageEnvironment(luaPath: string, trusted: boolean, roots: string[], launchPaths?: (root: string) => LaunchPaths, probeNativeModules = false): Promise<LanguageEnvironment> {
  const options: LanguageEnvironment = { luaPath, useInterpreter: trusted, workspaceRoots: roots, modulePaths: [], probeNativeModules: trusted && probeNativeModules };
  if (!trusted) return options;
  const executable = luaPath || await automaticInterpreter();
  const interpreter = executable ? await inspectInterpreter(executable) : undefined;
  if (!interpreter) return options;
  options.luaPath = interpreter.path;
  options.modulePaths = await Promise.all((roots.length ? roots : ['']).map(async root => {
    const templates: string[] = [];
    const ctemplates: string[] = [];
    if (root && launchPaths) {
      const launch = launchPaths(root);
      templates.push(...launch.lua);
      ctemplates.push(...launch.native);
    }
    if (root) {
      try {
        const env = await packageEnvironment(root, interpreter);
        templates.push(path.join(env.tree, 'share', 'lua', env.version, '?.lua'), path.join(env.tree, 'share', 'lua', env.version, '?', 'init.lua'));
        ctemplates.push(path.join(env.tree, 'lib', 'lua', env.version, process.platform === 'win32' ? '?.dll' : '?.so'));
      } catch { /* An unusable package directory must not disable local analysis. */ }
    }
    try {
      const probe = await probeLibraries(interpreter.path, root || path.dirname(interpreter.path));
      templates.push(...probe.luaPath.split(';').filter(Boolean));
      ctemplates.push(...probe.cPath.split(';').filter(Boolean));
    } catch { /* Keep managed packages and local analysis when probing fails. */ }
    return { root, templates, ctemplates };
  }));
  return options;
}
