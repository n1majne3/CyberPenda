import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import http from 'node:http';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
// The Pi entry is plain JavaScript in a .ts file. Load it on CI's Node 20 too.
const source = await readFile(new URL('../internal/runner/assets/hosted-control.ts', import.meta.url));
const { default: extension } = await import(`data:text/javascript;base64,${source.toString('base64')}`);

test('Runtime control waits for execution to settle after stop', async (t) => {
  const handlers = new Map();
  const bus = new EventEmitter();
  const events = { on(name, fn) { bus.on(name, fn); return () => bus.off(name, fn); }, emit: (...args) => bus.emit(...args) };
  let settle;
  const record = { id: 'native-12', status: 'running', promise: new Promise(resolve => { settle = resolve; }) };
  globalThis[Symbol.for('pi-subagents:manager')] = { getRecord: id => id === record.id ? record : undefined };
  bus.on('subagents:rpc:stop', ({ requestId, agentId }) => {
    assert.equal(agentId, record.id);
    record.status = 'stopped';
    events.emit(`subagents:rpc:stop:reply:${requestId}`, { success: true });
  });
  extension({ events, on: (name, fn) => handlers.set(name, fn) });
  await handlers.get('session_start')({}, { cwd: process.cwd() });
  t.after(async () => {
    settle();
    await handlers.get('session_shutdown')();
    delete globalThis[Symbol.for('pi-subagents:manager')];
  });
  const request = (action, id = record.id) => new Promise((resolve, reject) => {
    const req = http.request({ socketPath: process.env.CYBERPENDA_PI_CONTROL_SOCKET,
      path: `/${action}?id=${id}`, method: action === 'stop' ? 'POST' : 'GET' }, res => {
      let body = '';
      res.on('data', chunk => body += chunk);
      res.on('end', () => resolve({ code: res.statusCode, ...JSON.parse(body) }));
    });
    req.on('error', reject);
    req.end();
  });
  assert.equal((await request('status')).settled, false);
  const stopped = await request('stop');
  assert.equal(stopped.status, 'stopped');
  assert.equal(stopped.settled, false, 'stop acknowledgement is not execution completion');
  settle();
  await record.promise;
  assert.equal((await request('status')).settled, true);
  assert.equal((await request('status', 'unknown')).code, 404);
});

test('Hosted tool calls have a shell deadline and never block on one Execute', async () => {
  const handlers = new Map();
  extension({ on: (name, fn) => handlers.set(name, fn) });
  const call = handlers.get('tool_call');
  assert.ok(call, 'Hosted Runtime must enforce tool bounds');
  for (const timeout of [undefined, 0, -1, 999999]) {
    const event = { toolName: 'bash', input: { command: 'long-command', timeout } };
    await call(event);
    assert.equal(event.input.timeout, 300);
  }
  const short = { toolName: 'bash', input: { command: 'quick-command', timeout: 2 } };
  await call(short);
  assert.equal(short.input.timeout, 2);
  const wait = { toolName: 'get_subagent_result', input: { agent_id: 'native-12', wait: true } };
  await call(wait);
  assert.equal(wait.input.wait, false);
  const spawn = { toolName: 'Agent', input: { subagent_type: 'execute', run_in_background: false } };
  await call(spawn);
  assert.equal(spawn.input.run_in_background, true);
});
