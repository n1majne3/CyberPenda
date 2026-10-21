// Hosted Pi control. The Runtime owns execution; the dispatcher owns scheduling.
import { createServer } from 'node:http';
import { mkdtemp, chmod, rm } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { join } from 'node:path';

const ownerKey = Symbol.for('cyberpenda:hosted-control');
const managerKey = Symbol.for('pi-subagents:manager');
const terminal = new Set(['completed', 'steered', 'error', 'aborted', 'stopped']);

export default function hostedControl(pi) {
  let server;
  let directory;
  const runtimeID = randomUUID();
  const tracked = new Map();

  pi.on('tool_call', event => {
    if (event.toolName === 'bash') {
      const requested = event.input.timeout;
      event.input.timeout = Number.isFinite(requested) && requested > 0 ? Math.min(requested, 300) : 300;
    }
    if (event.toolName === 'get_subagent_result') event.input.wait = false;
    if (event.toolName === 'Agent' && event.input.subagent_type === 'execute') {
      event.input.run_in_background = true;
    }
  });

  async function status(id) {
    const record = globalThis[managerKey]?.getRecord(id);
    let entry = tracked.get(id);
    if (record && (!entry || entry.promise !== record.promise)) {
      entry = { record, promise: record.promise, settled: false };
      tracked.set(id, entry);
      if (record.promise) {
        // abort() marks stopped before this promise settles. Never equate them.
        record.promise.then(() => { entry.settled = true; }, () => { entry.settled = true; });
      }
    }
    if (!entry) return undefined;
    await Promise.resolve();
    const current = entry.record;
    return { agent_id: id, runtime_id: runtimeID, status: current.status,
      settled: terminal.has(current.status) && (entry.settled || (!entry.promise && !!current.completedAt)) };
  }

  function stop(id) {
    return new Promise((resolve, reject) => {
      const requestId = randomUUID();
      const reply = `subagents:rpc:stop:reply:${requestId}`;
      const timer = setTimeout(() => { off(); reject(new Error('Runtime stop RPC timed out')); }, 2000);
      const off = pi.events.on(reply, result => {
        clearTimeout(timer);
        off();
        if (result.success) resolve();
        else reject(new Error(result.error || 'Runtime stop failed'));
      });
      pi.events.emit('subagents:rpc:stop', { requestId, agentId: id });
    });
  }

  pi.on('session_start', async () => {
    // Child extensions share a process. Only the first bound session owns control.
    if (globalThis[ownerKey]) return;
    globalThis[ownerKey] = runtimeID;
    directory = await mkdtemp('/tmp/cyberpenda-pi-');
    const socket = join(directory, 'control.sock');
    server = createServer(async (req, res) => {
      try {
        const url = new URL(req.url, 'http://localhost');
        const id = url.searchParams.get('id');
        if (!id || id.length > 256 || !['/status', '/stop'].includes(url.pathname) ||
            req.method !== (url.pathname === '/stop' ? 'POST' : 'GET')) {
          res.writeHead(400).end(JSON.stringify({ error: 'Invalid Runtime control request' }));
          return;
        }
        let state = await status(id);
        if (!state) {
          res.writeHead(404).end(JSON.stringify({ error: 'Unknown Execute in this Runtime' }));
          return;
        }
        if (url.pathname === '/stop' && ['running', 'queued'].includes(state.status)) {
          await stop(id);
          state = await status(id);
        }
        res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify(state));
      } catch (error) {
        res.writeHead(503).end(JSON.stringify({ error: String(error) }));
      }
    });
    server.requestTimeout = 5000;
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(socket, resolve);
    });
    await chmod(socket, 0o600);
    process.env.CYBERPENDA_PI_CONTROL_SOCKET = socket;
  });

  pi.on('session_shutdown', async () => {
    if (!server) return;
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
    await rm(directory, { recursive: true, force: true });
    if (globalThis[ownerKey] === runtimeID) delete globalThis[ownerKey];
    delete process.env.CYBERPENDA_PI_CONTROL_SOCKET;
  });
}
