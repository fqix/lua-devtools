// DAP smoke test outside VS Code: hand-written Content-Length framing driving vscode/bin/lua-dap.
//   node scripts/dap-smoke.mjs [script.lua] [breakpointLine]
// Walks through initialize -> launch -> setBreakpoints -> configurationDone -> stopped ->
// threads/stackTrace/scopes/variables/evaluate -> next -> continue -> exited/terminated.
import { spawn } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const program = path.resolve(process.argv[2] ?? path.join(root, 'examples', 'hello.lua'));
const bpLine = Number(process.argv[3] ?? 2);

const adapter = spawn(path.join(root, 'vscode', 'bin', 'lua-dap'), [], { stdio: ['pipe', 'pipe', 'inherit'] });

let seq = 1;
const pending = new Map();
const eventWaiters = [];

function send(command, args = {}) {
  const msg = { seq: seq++, type: 'request', command, arguments: args };
  const json = JSON.stringify(msg);
  // Framing shared by DAP and LSP: Content-Length counts bytes, then \r\n\r\n, then the JSON body.
  adapter.stdin.write(`Content-Length: ${Buffer.byteLength(json, 'utf8')}\r\n\r\n${json}`);
  return new Promise((resolve, reject) => pending.set(msg.seq, { resolve, reject, command }));
}

function waitEvent(name) {
  return new Promise((resolve) => eventWaiters.push({ name, resolve }));
}

let buffer = Buffer.alloc(0);
adapter.stdout.on('data', (chunk) => {
  buffer = Buffer.concat([buffer, chunk]);
  for (;;) {
    const headerEnd = buffer.indexOf('\r\n\r\n');
    if (headerEnd < 0) return;
    const header = buffer.subarray(0, headerEnd).toString('utf8');
    const length = Number(/Content-Length: (\d+)/.exec(header)?.[1]);
    const bodyStart = headerEnd + 4;
    if (buffer.length < bodyStart + length) return;
    const body = JSON.parse(buffer.subarray(bodyStart, bodyStart + length).toString('utf8'));
    buffer = buffer.subarray(bodyStart + length);
    handle(body);
  }
});

function handle(msg) {
  if (msg.type === 'response') {
    const p = pending.get(msg.request_seq);
    pending.delete(msg.request_seq);
    console.log(`<- response ${msg.command}${msg.success ? '' : ' FAILED: ' + msg.message}`, JSON.stringify(msg.body ?? {}));
    msg.success ? p.resolve(msg.body) : p.reject(new Error(msg.message));
  } else if (msg.type === 'event') {
    console.log(`<- event ${msg.event}`, JSON.stringify(msg.body ?? {}));
    for (let i = eventWaiters.length - 1; i >= 0; i--) {
      if (eventWaiters[i].name === msg.event) {
        eventWaiters.splice(i, 1)[0].resolve(msg.body);
      }
    }
  }
}

const terminated = waitEvent('terminated');
const initialized = waitEvent('initialized');
await send('initialize', { adapterID: 'lua', linesStartAt1: true, columnsStartAt1: true, pathFormat: 'path' });
await initialized;
const launchDone = send('launch', { program, stopOnEntry: false });
await send('setBreakpoints', { source: { path: program }, breakpoints: [{ line: bpLine }] });
// Events can arrive before the response that triggers them: register waiters first.
let stoppedEvent = waitEvent('stopped');
await send('configurationDone');
await launchDone;

const stopped = await stoppedEvent;
console.log(`== stopped: ${stopped.reason}`);
await send('threads');
const { stackFrames } = await send('stackTrace', { threadId: 1 });
const { scopes } = await send('scopes', { frameId: stackFrames[0].id });
for (const scope of scopes) {
  await send('variables', { variablesReference: scope.variablesReference });
}
await send('evaluate', { expression: 'a + b', frameId: stackFrames[0].id, context: 'repl' });
stoppedEvent = waitEvent('stopped');
await send('next', { threadId: 1 });
await stoppedEvent;
await send('stackTrace', { threadId: 1 });
await send('continue', { threadId: 1 });
await terminated;
await send('disconnect');
adapter.stdin.end();
console.log('== done');
