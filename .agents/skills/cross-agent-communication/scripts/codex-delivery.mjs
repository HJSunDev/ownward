import net from 'node:net';
import fs from 'node:fs';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { fileURLToPath } from 'node:url';

const INACTIVE = /^(?:Conversation .+ is not being streamed\.|Cannot steer conversation .+ because its active turn already ended)$/;
const frame = value => {
  const body = Buffer.from(JSON.stringify(value));
  const result = Buffer.alloc(body.length + 4);
  result.writeUInt32LE(body.length);
  body.copy(result, 4);
  return result;
};

export function resolveDefaultQueueExe({
  platform = process.platform,
  localAppData = process.env.LOCALAPPDATA,
} = {}) {
  if (platform !== 'win32' || !localAppData) return null;
  const binRoot = path.join(localAppData, 'OpenAI', 'Codex', 'bin');
  try {
    const candidates = fs.readdirSync(binRoot, { withFileTypes: true })
      .filter(entry => entry.isDirectory())
      .map(entry => {
        const exe = path.join(binRoot, entry.name, 'codex.exe');
        try {
          const stat = fs.statSync(exe);
          return stat.isFile() ? { exe, mtimeMs: stat.mtimeMs } : null;
        } catch {
          return null;
        }
      })
      .filter(Boolean)
      .sort((a, b) => b.mtimeMs - a.mtimeMs);
    return candidates[0]?.exe || null;
  } catch {
    return null;
  }
}

function queueIdle(exe, threadId, message) {
  return new Promise(resolve => {
    let settled = false;
    const child = spawn(exe, ['queue', '--thread', threadId, '--message', message], {
      windowsHide: true, stdio: 'ignore', shell: false,
    });
    const finish = result => { if (settled) return; settled = true; clearTimeout(timer); resolve(result); };
    const timer = setTimeout(() => { child.kill(); finish({ phase: 'unknown', code: 'idle-queue-timeout' }); }, 20000);
    child.once('error', error => finish({ phase: 'failed', code: error.code || 'idle-queue-spawn-error' }));
    child.once('exit', code => finish(code === 0
      ? { phase: 'submitted', mode: 'queue-idle' }
      : { phase: 'failed', code: `idle-queue-exit-${code}` }));
  });
}

/** Append to the existing desktop owner's active turn. Queue only after an explicit idle rejection. */
export async function deliverCodex({ threadId, message, cwd, queueExe, deliveryId,
  pipePath = process.platform === 'win32' ? '\\\\.\\pipe\\codex-ipc' : null,
  timeoutMs = 6000, queueMessage = queueIdle } = {}) {
  if (!threadId || !message || !cwd) return { phase: 'failed', code: 'missing-delivery-input' };
  if (!pipePath) return { phase: 'failed', code: 'desktop-ipc-platform-unverified' };
  const id = deliveryId || createHash('sha256').update(`${threadId}\0${message}`).digest('hex');
  const socket = net.createConnection(pipePath);
  let incoming = Buffer.alloc(0), clientId = 'initializing-client', activeRequest = null;
  let steerWritten = false, steerRejected = false;
  const failPending = error => activeRequest?.reject(error);
  socket.on('error', failPending);
  socket.on('close', () => failPending(new Error('ipc-connection-closed')));
  socket.on('data', chunk => {
    incoming = Buffer.concat([incoming, chunk]);
    try {
      while (incoming.length >= 4) {
        const size = incoming.readUInt32LE(0);
        if (size === 0 || size > 8388608) throw new Error('invalid-ipc-frame');
        if (incoming.length < size + 4) break;
        const reply = JSON.parse(incoming.subarray(4, size + 4).toString('utf8'));
        incoming = incoming.subarray(size + 4);
        if (reply.type === 'client-discovery-request') {
          socket.write(frame({ type: 'client-discovery-response', requestId: reply.requestId, response: { canHandle: false } }));
        } else if (reply.type === 'response' && reply.requestId === activeRequest?.id) {
          activeRequest.resolve(reply);
        }
      }
    } catch (error) { failPending(error); socket.destroy(); }
  });
  const request = (method, params, version, targetClientId) => new Promise((resolve, reject) => {
    const requestId = randomUUID();
    const finish = (callback, value) => { clearTimeout(timer); activeRequest = null; callback(value); };
    const timer = setTimeout(() => finish(reject, new Error('ipc-request-timeout')), timeoutMs);
    activeRequest = { id: requestId, resolve: value => finish(resolve, value), reject: error => finish(reject, error) };
    if (method === 'thread-follower-steer-turn') steerWritten = true;
    socket.write(frame({ type: 'request', requestId, sourceClientId: clientId, method,
      params, version, targetClientId, timeoutMs }));
  });
  try {
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => { socket.destroy(); reject(new Error('ipc-connect-timeout')); }, timeoutMs);
      socket.once('connect', () => { clearTimeout(timer); resolve(); });
      socket.once('error', error => { clearTimeout(timer); reject(error); });
    });
    const initialized = await request('initialize', { clientType: 'agent-mailbox' }, 0);
    if (initialized.resultType !== 'success' || !initialized.result?.clientId) throw new Error('ipc-initialize-rejected');
    clientId = initialized.result.clientId;
    const owner = await request('thread-owner-discovery', { hostId: 'local', conversationId: threadId }, 1);
    if (owner.resultType !== 'success' || !owner.handledByClientId) {
      return { phase: 'failed', code: owner.error || 'desktop-owner-unavailable' };
    }
    const reply = await request('thread-follower-steer-turn', {
      conversationId: threadId, clientUserMessageId: id,
      input: [{ type: 'text', text: message, text_elements: [] }],
      restoreMessage: { id, cwd, context: { workspaceRoots: [cwd], commentAttachments: [] } },
    }, 1, owner.handledByClientId);
    if (reply.resultType !== 'success') {
      steerRejected = true;
      socket.destroy();
      const resolvedQueueExe = queueExe || resolveDefaultQueueExe();
      if (INACTIVE.test(reply.error || '') && resolvedQueueExe) {
        return await queueMessage(resolvedQueueExe, threadId, message);
      }
      return { phase: 'failed', code: reply.error || 'steer-rejected' };
    }
    const turnId = reply.result?.result?.turnId;
    if (!turnId) return { phase: 'unknown', code: 'steer-reply-missing-turn-id' };
    return { phase: 'submitted', mode: 'steer', turnId, clientUserMessageId: id };
  } catch (error) {
    return { phase: steerWritten && !steerRejected ? 'unknown' : 'failed', code: error.code || error.message || 'desktop-ipc-error' };
  } finally { socket.destroy(); }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2), options = {};
  for (let i = 0; i < args.length; i += 2) {
    if (!args[i].startsWith('--') || args[i + 1] === undefined) throw new Error('Use --name value arguments');
    options[args[i].slice(2)] = args[i + 1];
  }
  const message = options.file ? fs.readFileSync(options.file, 'utf8') : options.message;
  const rawQueueExe = options['queue-exe'];
  const queueExe = rawQueueExe && fs.existsSync(rawQueueExe) ? rawQueueExe : resolveDefaultQueueExe();
  const result = await deliverCodex({ threadId: options.thread, message, cwd: options.cwd,
    queueExe, deliveryId: options.id });
  console.log(JSON.stringify(result));
  if (result.phase !== 'submitted') process.exitCode = 1;
}
