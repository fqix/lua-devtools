import { execFile } from 'node:child_process';

/** Format an editor buffer via stdin; never let the formatter write the source file. */
export function formatLua(executable: string, source: string, filename: string, cwd: string, signal?: AbortSignal): Promise<string> {
  return new Promise((resolve, reject) => {
    const child = execFile(executable, ['--respect-ignores', '--stdin-filepath', filename, '-'],
      { cwd, signal, timeout: 10000, maxBuffer: 16 * 1024 * 1024, windowsHide: true, encoding: 'utf8' },
      (error, stdout, stderr) => {
        if (error) { reject(new Error(stderr.trim() || error.message)); return; }
        // Ignored inputs can produce no stdout. Never replace a nonempty document with it.
        resolve(stdout || source);
      });
    // A process that fails at startup may close stdin before the buffer is written.
    child.stdin?.on('error', () => {});
    child.stdin?.end(source);
  });
}
