// LSP smoke test outside VS Code: drives vscode/bin/lua-lsp over stdio.
//   node scripts/lsp-smoke.mjs
// initialize -> didOpen hello.lua -> documentSymbol / definition / hover / completion
// -> didChange (introduce a syntax error) -> wait for publishDiagnostics -> shutdown/exit.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const file = path.join(root, 'examples', 'hello.lua');
const uri = pathToFileURL(file).href;
const text = fs.readFileSync(file, 'utf8');

const server = spawn(path.join(root, 'vscode', 'bin', 'lua-lsp'), [], { stdio: ['pipe', 'pipe', 'inherit'] });

let nextId = 1;
const pending = new Map();
const notificationWaiters = [];

function write(msg) {
  const json = JSON.stringify({ jsonrpc: '2.0', ...msg });
  server.stdin.write(`Content-Length: ${Buffer.byteLength(json, 'utf8')}\r\n\r\n${json}`);
}
function request(method, params) {
  const id = nextId++;
  write({ id, method, params });
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject, method }));
}
function notify(method, params) {
  write({ method, params });
}
function waitNotification(method) {
  return new Promise((resolve) => notificationWaiters.push({ method, resolve }));
}

let buffer = Buffer.alloc(0);
server.stdout.on('data', (chunk) => {
  buffer = Buffer.concat([buffer, chunk]);
  for (;;) {
    const headerEnd = buffer.indexOf('\r\n\r\n');
    if (headerEnd < 0) return;
    const length = Number(/Content-Length: (\d+)/i.exec(buffer.subarray(0, headerEnd).toString())?.[1]);
    const bodyStart = headerEnd + 4;
    if (buffer.length < bodyStart + length) return;
    const msg = JSON.parse(buffer.subarray(bodyStart, bodyStart + length).toString('utf8'));
    buffer = buffer.subarray(bodyStart + length);
    if (msg.id !== undefined && pending.has(msg.id)) {
      const p = pending.get(msg.id);
      pending.delete(msg.id);
      console.log(`<- ${p.method}`, JSON.stringify(msg.error ?? msg.result));
      msg.error ? p.reject(new Error(msg.error.message)) : p.resolve(msg.result);
    } else if (msg.method) {
      console.log(`<- notification ${msg.method}`, JSON.stringify(msg.params));
      for (let i = notificationWaiters.length - 1; i >= 0; i--) {
        if (notificationWaiters[i].method === msg.method) notificationWaiters.splice(i, 1)[0].resolve(msg.params);
      }
    }
  }
});

function positionOf(needle, nth = 0) {
  let from = 0;
  for (let i = 0; i <= nth; i++) {
    const idx = text.indexOf(needle, from);
    if (idx < 0) throw new Error(`not found: ${needle}`);
    from = idx + (i < nth ? needle.length : 0);
  }
  const before = text.slice(0, from);
  const line = before.split('\n').length - 1;
  const character = from - (before.lastIndexOf('\n') + 1);
  return { line, character };
}

const td = { uri };
await request('initialize', { processId: process.pid, rootUri: pathToFileURL(root).href, capabilities: {} });
notify('initialized', {});
const firstDiagnostics = waitNotification('textDocument/publishDiagnostics');
notify('textDocument/didOpen', { textDocument: { uri, languageId: 'lua', version: 1, text } });
await firstDiagnostics;

await request('textDocument/codeLens', { textDocument: td });
await request('textDocument/documentSymbol', { textDocument: td });
await request('textDocument/definition', { textDocument: td, position: positionOf('add(x, y)') });
await request('textDocument/hover', { textDocument: td, position: positionOf('result\n') });
await request('textDocument/hover', { textDocument: td, position: positionOf('print(') });
const completion = await request('textDocument/completion', { textDocument: td, position: positionOf('return result') });
console.log('== completion labels:', completion.items.slice(0, 8).map((i) => i.label).join(', '), '...');

// Break the file: delete the closing "end" of add() (incremental change).
const endPos = positionOf('\nend\n');
const diagnostics = waitNotification('textDocument/publishDiagnostics');
notify('textDocument/didChange', {
  textDocument: { uri, version: 2 },
  contentChanges: [{ range: { start: { line: endPos.line + 1, character: 0 }, end: { line: endPos.line + 1, character: 3 } }, text: '' }],
});
const diag = await diagnostics;
console.log(`== diagnostics after edit: ${diag.diagnostics.length}`);

await request('shutdown');
notify('exit');
console.log('== done');
