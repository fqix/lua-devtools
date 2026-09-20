import { execFile } from 'node:child_process';
import { readdir } from 'node:fs/promises';
import * as path from 'node:path';

export interface StandardLibrary { name: string; members: { name: string; kind: string }[] }
export interface LibraryProbe { standard: StandardLibrary[]; luaPath: string; cPath: string }
export interface LibraryModule { name: string; file: string; native: boolean }

// Query only built-in tables. Do not require any third-party modules or run LUA_INIT.
const probe = `
local function hex(s) return (s:gsub('.', function(c) return string.format('%02x', string.byte(c)) end)) end
for _,name in ipairs({'_G','coroutine','debug','io','math','os','package','string','table','utf8','bit32','bit','jit'}) do
 local lib = name == '_G' and _G or rawget(_G,name)
 if type(lib)=='table' then
  io.write('S\\t',name,'\\n')
  for key,value in next,lib do
   if type(key)=='string' and key:match('^[%w_]+$') and type(value)=='function' then
    io.write('M\\t',name,'\\t',key,'\\t',debug.getinfo(value,'S').what,'\\n')
   end
  end
 end
end
io.write('P\\t',hex(package.path),'\\nC\\t',hex(package.cpath),'\\n')
`;

export function probeLibraries(executable: string, cwd: string): Promise<LibraryProbe> {
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !/^LUA_INIT(?:_|$)/i.test(key)));
  return new Promise((resolve, reject) => {
    execFile(executable, ['-e', probe], { cwd, env, timeout: 3000, maxBuffer: 1024 * 1024, windowsHide: true }, (error, stdout, stderr) => {
      if (error) { reject(new Error(stderr.trim() || error.message)); return; }
      const libraries = new Map<string, StandardLibrary>();
      let luaPath = '', cPath = '';
      for (const line of stdout.split(/\r?\n/)) {
        const [tag, name, member, kind] = line.split('\t');
        if (tag === 'S') libraries.set(name, { name, members: [] });
        if (tag === 'M') libraries.get(name)?.members.push({ name: member, kind });
        if (tag === 'P') luaPath = Buffer.from(name, 'hex').toString();
        if (tag === 'C') cPath = Buffer.from(name, 'hex').toString();
      }
      const standard = [...libraries.values()].sort((a, b) => a.name.localeCompare(b.name));
      for (const library of standard) library.members.sort((a, b) => a.name.localeCompare(b.name));
      resolve({ standard, luaPath, cPath });
    });
  });
}

/** Enumerate ordinary module templates without executing modules or following directory symlinks. */
export async function scanModules(luaPath: string, cPath: string, cwd: string): Promise<{ modules: LibraryModule[]; limited: boolean; unreadable: boolean }> {
  const modules = new Map<string, LibraryModule>();
  let visited = 0, limited = false, unreadable = false;
  // Preserve search order: Lua source paths precede native paths, as in package.searchers.
  for (const [templates, native] of [[luaPath, false], [cPath, true]] as const) {
    for (const template of templates.split(';').filter(Boolean)) {
      if (visited >= 5000) { limited = true; break; }
      if ((template.match(/\?/g) ?? []).length !== 1) { limited = true; continue; }
      const absolute = path.resolve(cwd, template);
      const question = absolute.indexOf('?');
      const prefix = absolute.slice(0, question), suffix = absolute.slice(question + 1);
      const root = path.dirname(prefix + 'placeholder');
      const walk = async (directory: string, depth: number): Promise<void> => {
        if (depth > 8 || visited >= 5000) { limited = true; return; }
        let entries;
        try { entries = await readdir(directory, { withFileTypes: true }); }
        catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') unreadable = true; return; }
        entries.sort((a, b) => a.name.localeCompare(b.name));
        for (const entry of entries) {
          if (++visited > 5000) { limited = true; return; }
          if (entry.name.startsWith('.')) continue;
          const file = path.join(directory, entry.name);
          if (entry.isDirectory()) { await walk(file, depth + 1); continue; }
          if (!entry.isFile() && !entry.isSymbolicLink()) continue;
          if (!file.startsWith(prefix) || !file.endsWith(suffix)) continue;
          const relative = file.slice(prefix.length, suffix ? -suffix.length : undefined);
          const name = relative.split(path.sep).join('.');
          if (!name || !/^[\w.-]+$/.test(name) || modules.has(name)) continue;
          modules.set(name, { name, file, native });
        }
      };
      await walk(root, 0);
    }
  }
  return { modules: [...modules.values()].sort((a, b) => a.name.localeCompare(b.name)), limited, unreadable };
}
