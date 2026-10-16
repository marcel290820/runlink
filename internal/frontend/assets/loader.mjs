const approvedGUI = Object.freeze(["764d59b32e790f7de34df6022107364d23354ea9e8d2ec7afdaaa5bbd299f1d0"]);

// The gate shows where the link stands until the verified GUI replaces it.
const gate = document.getElementById('gate');
const form = document.getElementById('secret-form');
const field = document.getElementById('secret');
const fieldError = document.getElementById('secret-error');
const retry = document.getElementById('retry');
const note = document.getElementById('note');
const states = {
  secret: ['Enter the link secret', 'The owner sends it separately from the link.'],
  connecting: ['Connecting', "Reaching the owner's machine."],
  unreachable: ["Can't reach the owner's machine", "The connection didn't go through, so nothing was sent. " +
    'Check your network and try again.'],
  inactive: ['This link is not active', "The owner's machine isn't sharing this task right now, so nothing was sent. " +
    'Ask the person who sent you the link to share it again.'],
  rejected: ["This link doesn't match", "The owner's machine didn't accept the link's secret, so nothing was sent. " +
    'Ask for a new link.'],
  incomplete: ['This link is incomplete', 'Part of its secret is missing. Copy the whole link, including everything after #, ' +
    'and open it again.'],
  unverified: ["This task can't be opened safely", "The owner's machine sent a task page this version of Runlink doesn't approve, " +
    'so nothing ran. Ask the owner to update Runlink.'],
  unsupported: ["This task can't be shown", "The owner's machine described the task in a way this page can't show, " +
    'so nothing ran. Let the owner know.'],
  nolink: ['Open a task link', 'This page opens tasks that someone shares with you. Use the link they sent.'],
};
const reloadNote = 'You will need the secret again if you reload this page.';
const historyNote = 'This link contains its secret, so it can stay in your browser history and on synced devices. ' +
  'To keep it out, ask the owner to send the secret separately.';

function show(state, hint) {
  const [title, status] = states[state];
  gate.dataset.state = ['secret', 'connecting'].includes(state) ? state : 'unavailable';
  gate.dataset.reason = state;
  document.getElementById('gate-title').textContent = title;
  document.getElementById('status').textContent = status;
  form.hidden = state !== 'secret';
  retry.hidden = state !== 'unreachable';
  note.textContent = hint;
  note.hidden = !hint;
}

// The secret stays in this module, in memory, until the owner's machine has proven
// it holds the same one (ADR 0009).
const taskId = location.pathname.slice(1);
// 32 bytes in unpadded base64url: secrets, nonces, and proofs.
const KEY = /^[A-Za-z0-9_-]{43}$/;
let secret = window.runlinkSession.consume();
const separate = !secret;
const hint = separate ? reloadNote : historyNote;

function ask(problem) {
  show('secret', hint);
  fieldError.textContent = problem;
  if (problem) field.setAttribute('aria-invalid', 'true');
  field.focus();
}

async function open() {
  show('connecting', hint);
  try {
    await start();
    secret = '';
  } catch (error) {
    if (error.reason === 'rejected' && separate) ask("That secret wasn't accepted. Check it and paste it again.");
    else show(error.reason ?? 'unreachable', hint);
  }
}

if (!taskId) show('nolink', '');
else if (separate) ask('');
else if (!KEY.test(secret)) show('incomplete', historyNote);
else open();

field.addEventListener('input', () => {
  fieldError.textContent = '';
  field.removeAttribute('aria-invalid');
});
form.addEventListener('submit', event => {
  event.preventDefault();
  // Pasted secrets often carry stray whitespace; a generated secret never contains any.
  const value = field.value.trim();
  field.value = '';
  if (!KEY.test(value)) {
    ask(value ? "That isn't a whole Runlink secret. Paste all 43 characters the owner sent." : 'Paste the secret the owner sent you.');
    return;
  }
  secret = value;
  open();
});
retry.addEventListener('click', open);

const failure = reason => Object.assign(new Error(reason), { reason });
// A lost connection reads as disconnected to the GUI.
const lost = () => Object.assign(new Error(''), { code: 'disconnected', reason: 'unreachable' });

// Every control message is one small JSON object.
function parse(data) {
  if (typeof data !== 'string' || data.length > 65536) throw failure('unreachable');
  const message = JSON.parse(data);
  if (message === null || typeof message !== 'object' || Array.isArray(message)) throw failure('unreachable');
  return message;
}

// Connect through the service to the owner's machine and authenticate it, then
// show the task with the GUI it sends. Authentication and the GUI must arrive
// within 30 seconds of the channel opening.
async function start() {
  const link = await connect();
  const deadline = setTimeout(link.end, 30000);
  try {
    await authenticate(link);
    const gui = await link.next();
    const { definition } = parse(await link.next());
    if (!(gui instanceof ArrayBuffer)) throw failure('unverified');
    await verifyGUI(new Uint8Array(gui)).catch(() => { throw failure('unverified'); });
    await mountGUI(new Uint8Array(gui), { definition, run: runner(link, definition) })
      .catch(error => { throw error.reason ? error : failure('unsupported'); });
  } catch (error) {
    link.end();
    throw error;
  } finally {
    clearTimeout(deadline);
  }
}

// Signaling: the service hands out ICE servers, carries one offer with all its
// candidates to the owner's machine, and returns the answer. The socket closes
// once the answer arrives (ADR 0002).
function connect() {
  return new Promise((resolve, reject) => {
    const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
    const socket = new WebSocket(`${scheme}://${location.host}/api/v1/connect/${taskId}`);
    let peer, answered = false, settled = false;
    const timer = setTimeout(() => fail('unreachable'), 30000);
    function fail(reason) {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      socket.close();
      peer?.close();
      reject(failure(reason));
    }
    socket.onclose = () => { if (!answered) fail('unreachable'); };
    socket.onmessage = async ({ data }) => {
      try {
        const message = parse(data);
        if (message.error) return fail(message.error === 'unavailable' ? 'inactive' : 'unreachable');
        if (!peer && Array.isArray(message.ice) && message.ice.length <= 4) {
          peer = new RTCPeerConnection({ iceServers: message.ice });
          const channel = peer.createDataChannel('runlink');
          const link = session(peer, channel, () => fail('unreachable'));
          channel.onopen = () => {
            settled = true;
            clearTimeout(timer);
            resolve(link);
          };
          await peer.setLocalDescription();
          await gathered(peer);
          socket.send(JSON.stringify({ sdp: peer.localDescription.sdp }));
        } else if (peer && !answered && typeof message.sdp === 'string') {
          answered = true;
          await peer.setRemoteDescription({ type: 'answer', sdp: message.sdp });
          socket.close();
        } else fail('unreachable');
      } catch {
        fail('unreachable');
      }
    };
  });
}

// Wait for every candidate, or five seconds; the offer carries what has gathered.
function gathered(peer) {
  return new Promise(resolve => {
    const check = () => { if (peer.iceGatheringState === 'complete') resolve(); };
    peer.addEventListener('icegatheringstatechange', check);
    check();
    setTimeout(resolve, 5000);
  });
}

// The owner's machine reads messages of up to 64 KiB and sends at most 1 MiB of
// output per run. Every message counts as at least 1 KiB against those bounds, so
// empty messages cannot pile up either.
const MAX_INPUT = 65536, MAX_OUTPUT = 1 << 20, CHUNK = 16384;
const cost = data => Math.max(1024, typeof data === 'string' ? data.length : data.byteLength);

// session wraps the data channel. next() resolves to its messages in order;
// send() writes one control message; upload() sends a file's bytes; end() closes
// the connection and fails every pending read, as a failed connection does.
// Unread messages are bounded in bytes.
function session(peer, channel, failed) {
  const queue = [], waiting = [];
  let closed = false, queued = 0, drained = () => {};
  const close = () => {
    closed = true;
    drained();
    for (const { reject } of waiting.splice(0)) reject(lost());
  };
  // Closing the peer here fires no close event on the channel.
  const end = () => {
    queue.length = 0;
    peer.close();
    close();
  };
  peer.onconnectionstatechange = () => {
    if (peer.connectionState === 'failed') {
      failed();
      end();
    }
  };
  channel.binaryType = 'arraybuffer';
  channel.bufferedAmountLowThreshold = 16 * CHUNK;
  channel.onbufferedamountlow = () => drained();
  channel.onclose = close;
  channel.onmessage = ({ data }) => {
    if (waiting.length) return waiting.shift().resolve(data);
    queue.push(data);
    if ((queued += cost(data)) > 2 * MAX_OUTPUT) end();
  };
  const next = () => {
    if (!queue.length) return closed ? Promise.reject(lost()) : new Promise((resolve, reject) => waiting.push({ resolve, reject }));
    queued -= cost(queue[0]);
    return Promise.resolve(queue.shift());
  };
  const send = message => {
    const text = JSON.stringify(message);
    if (new TextEncoder().encode(text).length > MAX_INPUT) {
      throw Object.assign(new Error('Your input is larger than 64 KB, the most one run can take.'), { code: 'invalid' });
    }
    if (channel.readyState !== 'open') throw lost();
    try {
      channel.send(text);
    } catch {
      throw lost();
    }
  };
  // Waits while the channel holds more than the threshold, so a large file never
  // fills the browser's send buffer.
  const upload = async file => {
    for (let at = 0; at < file.size; at += CHUNK) {
      let bytes;
      try {
        bytes = await file.slice(at, at + CHUNK).arrayBuffer();
      } catch {
        end();
        throw Object.assign(new Error('This page could not read your file.'), { code: 'failed' });
      }
      while (!closed && channel.bufferedAmount > channel.bufferedAmountLowThreshold) await new Promise(resolve => { drained = resolve; });
      if (closed || channel.readyState !== 'open') throw lost();
      try {
        channel.send(bytes);
      } catch {
        throw lost();
      }
    }
  };
  return { peer, next, send, upload, end };
}

const base64url = bytes => bytes.toBase64({ alphabet: 'base64url', omitPadding: true });

// The single SHA-256 DTLS certificate fingerprint a description declares, as
// lowercase hex. The browser verifies the peer's certificate against it.
function fingerprint(description) {
  const sdp = description.sdp;
  const found = [...sdp.matchAll(/^a=fingerprint:sha-256 ((?:[0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2})\r?$/gm)]
    .map(match => match[1].replaceAll(':', '').toLowerCase());
  if (!found.length || found.length !== sdp.split('a=fingerprint:').length - 1 || new Set(found).size !== 1) {
    throw failure('unreachable');
  }
  return found[0];
}

// The browser's side of the challenge-response in ADR 0009: send a nonce, verify
// the runner's proof over both nonces, the task, and both peers' certificate
// fingerprints, then prove the same. A peer that relays the connection sees
// different fingerprints, so its proofs never verify.
async function authenticate({ peer, next, send }) {
  const key = await crypto.subtle.importKey('raw', Uint8Array.fromBase64(secret, { alphabet: 'base64url' }),
    { name: 'HMAC', hash: 'SHA-256' }, false, ['sign', 'verify']);
  const browserFP = fingerprint(peer.localDescription), runnerFP = fingerprint(peer.remoteDescription);
  const nonce = base64url(crypto.getRandomValues(new Uint8Array(32)));
  send({ nonce });
  const hello = parse(await next());
  if (typeof hello.nonce !== 'string' || !KEY.test(hello.nonce) || typeof hello.proof !== 'string' || !KEY.test(hello.proof)) {
    throw failure('rejected');
  }
  const transcript = role => new TextEncoder().encode(['runlink-auth-v1', role, taskId, browserFP, runnerFP, nonce, hello.nonce].join('\n'));
  if (!await crypto.subtle.verify('HMAC', key, Uint8Array.fromBase64(hello.proof, { alphabet: 'base64url' }), transcript('runner'))) {
    throw failure('rejected');
  }
  send({ proof: base64url(new Uint8Array(await crypto.subtle.sign('HMAC', key, transcript('browser')))) });
}

// task.run for the GUI: send the values, files as their name and size, pass on
// stages and progress, send the files once the run is accepted, collect the
// output's bytes, and settle with the outcome.
function runner({ next, send, upload, end }, definition) {
  return async (values, progress) => {
    const files = (definition.inputs ?? []).map(spec => values[spec.name]).filter(value => value instanceof File);
    send({ values: Object.fromEntries(Object.entries(values).map(([name, value]) =>
      [name, value instanceof File ? { name: value.name, size: value.size } : value])) });
    const chunks = [];
    let received = 0, updates = 0;
    for (;;) {
      const data = await next();
      // The owner's machine sends at most 500 progress lines and a few stages per run.
      if (typeof data === 'string' && ++updates > 1000) {
        end();
        throw Object.assign(new Error("The owner's machine sent more updates than this page accepts."), { code: 'failed' });
      }
      if (data instanceof ArrayBuffer) {
        if ((received += cost(data)) > MAX_OUTPUT) {
          end();
          throw Object.assign(new Error('The result is larger than 1 MB, more than this page accepts.'), { code: 'failed' });
        }
        chunks.push(data);
        continue;
      }
      const message = parse(data);
      if (typeof message.stage === 'string') {
        progress(message);
        if (message.stage === 'accepted') for (const file of files.splice(0)) await upload(file);
      }
      else if (message.error !== null && typeof message.error === 'object') {
        const { code, message: text, fields } = message.error;
        throw Object.assign(new Error(typeof text === 'string' ? text : ''), { code, fields });
      } else if (message.done === true) return outputs(definition.output, new Blob(chunks));
      else throw lost();
    }
  };
}

async function outputs(output, blob) {
  switch (output?.kind) {
    case 'text': return [{ kind: 'text', value: await blob.text() }];
    case 'json': return [{ kind: 'json', value: JSON.parse(await blob.text()) }];
    case 'file': return [{ kind: 'file', name: typeof output.name === 'string' && output.name ? output.name : 'output', blob }];
  }
  return [];
}

export async function verifyGUI(bytes) {
  if (!(bytes instanceof Uint8Array) || bytes.length > 65536) throw new Error('Invalid GUI bundle size');
  const hash = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes)),
    byte => byte.toString(16).padStart(2, '0')).join('');
  if (!approvedGUI.includes(hash)) throw new Error('GUI release is not approved');
  return hash;
}

// Runs verified GUI bytes, which register runlinkGUI, and mounts the task object
// that gui.js documents.
const urls = new Set();
const policy = window.trustedTypes?.createPolicy('runlink-gui', {
  createScriptURL(value) {
    if (!urls.has(value)) throw new Error('Unverified GUI URL');
    return value;
  }
});
export async function mountGUI(bytes, task) {
  await verifyGUI(bytes);
  const url = URL.createObjectURL(new Blob([bytes], { type: 'text/javascript' }));
  urls.add(url);
  try {
    await new Promise((resolve, reject) => {
      const script = document.createElement('script');
      script.src = policy ? policy.createScriptURL(url) : url;
      script.onload = () => { script.remove(); resolve(); };
      script.onerror = () => { script.remove(); reject(new Error('GUI loading failed')); };
      document.head.append(script);
    });
  } finally { urls.delete(url); URL.revokeObjectURL(url); }
  const gui = window.runlinkGUI;
  delete window.runlinkGUI;
  if (typeof gui?.mount !== 'function') throw new Error('GUI did not register');
  gui.mount(document.getElementById('task'), task);
  document.getElementById('gate-view').hidden = true;
}
