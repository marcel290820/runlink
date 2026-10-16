// Drive Chromium WebRTC through the local stack's TURN stand-in, then share a task
// with the built CLI and complete it from the shared link, direct and relayed.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHmac } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createInterface } from 'node:readline';
import { chromium } from 'playwright';

function within(promise, ms, what) {
  let timer;
  const timeout = new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`${what} timed out`)), ms); });
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

// coturn's TURN REST credential, as B will issue it from the shared secret.
function restCredential(secret, name, lifetimeSeconds = 600) {
  const username = `${Math.floor(Date.now() / 1000) + lifetimeSeconds}:${name}`;
  return { username, credential: createHmac('sha1', secret).update(username).digest('base64') };
}

// In the page: two peers that may only use the relay exchange one message, then
// report the selected pair. ICE candidates are exchanged directly, as signaling would.
async function relayProbe({ url, username, credential }) {
  const config = { iceServers: [{ urls: url, username, credential }], iceTransportPolicy: 'relay' };
  const offerer = new RTCPeerConnection(config);
  const answerer = new RTCPeerConnection(config);
  offerer.onicecandidate = ({ candidate }) => candidate && answerer.addIceCandidate(candidate);
  answerer.onicecandidate = ({ candidate }) => candidate && offerer.addIceCandidate(candidate);
  const received = new Promise(resolve => {
    answerer.ondatachannel = ({ channel }) => { channel.onmessage = ({ data }) => resolve(data); };
  });
  const channel = offerer.createDataChannel('probe');
  channel.onopen = () => channel.send('ping');
  await offerer.setLocalDescription();
  await answerer.setRemoteDescription(offerer.localDescription);
  await answerer.setLocalDescription();
  await offerer.setRemoteDescription(answerer.localDescription);
  const message = await received;
  const stats = [...(await offerer.getStats()).values()];
  const byId = id => stats.find(report => report.id === id);
  const pair = byId(stats.find(report => report.type === 'transport').selectedCandidatePairId);
  const [local, remote] = [byId(pair.localCandidateId), byId(pair.remoteCandidateId)];
  offerer.close();
  answerer.close();
  return { message, local: local.candidateType, remote: remote.candidateType, protocol: local.relayProtocol };
}

// In the page: gather with a rejected credential and count relay candidates.
async function rejectedProbe({ url, username, credential }) {
  const peer = new RTCPeerConnection({ iceServers: [{ urls: url, username, credential }], iceTransportPolicy: 'relay' });
  let relays = 0, errors = 0;
  peer.onicecandidate = ({ candidate }) => { if (candidate?.type === 'relay') relays++; };
  peer.onicecandidateerror = () => { errors++; };
  const gathered = new Promise(resolve => {
    peer.onicegatheringstatechange = () => peer.iceGatheringState === 'complete' && resolve();
  });
  peer.createDataChannel('probe');
  await peer.setLocalDescription();
  await gathered;
  peer.close();
  return { relays, rejected: errors > 0 };
}

const state = await mkdtemp(join(tmpdir(), 'runlink-stack-'));
const child = spawn('build/runlink-dev',
  ['--frontend', '127.0.0.1:0', '--app', '127.0.0.1:0', '--turn', '127.0.0.1:0', '--state-dir', state],
  { stdio: ['ignore', 'ignore', 'pipe'] });
const logs = [];
const ready = new Promise((resolve, reject) => {
  createInterface({ input: child.stderr }).on('line', line => {
    logs.push(line);
    const event = line.startsWith('{') ? JSON.parse(line) : {};
    if (event.msg === 'stack_ready') resolve(event);
  });
  child.once('exit', code => reject(new Error(`runlink-dev exited with ${code}: ${logs.join('\n')}`)));
});

// Run runlink share on a task file; resolves its link lines once printed.
function share(service, file, ...options) {
  const owner = spawn('build/runlink', ['share', ...options, file],
    { env: { ...process.env, RUNLINK_SERVICE: service }, stdio: ['ignore', 'pipe', 'pipe'] });
  const events = [];
  createInterface({ input: owner.stderr }).on('line', line => events.push(line));
  const printed = [];
  const link = new Promise((resolve, reject) => {
    createInterface({ input: owner.stdout }).on('line', line => {
      printed.push(line);
      const text = printed.join('\n');
      const url = text.match(/http:\/\/\S+/)?.[0];
      const key = text.match(/^ {2}Secret {2}(\S+)$/m)?.[1];
      if (url && (url.includes('#') || key)) resolve({ url, key });
    });
    owner.once('exit', code => reject(new Error(`runlink share exited with ${code}: ${events.join('\n')}`)));
  });
  return { owner, events, link };
}

// A page whose RTCPeerConnection the test can inspect and, for the relay path,
// restrict to TURN, as a recipient behind a strict network would be.
async function recipient(url, relay = false) {
  const context = await browser.newContext();
  await context.addInitScript(relay => {
    const Native = RTCPeerConnection;
    window.RTCPeerConnection = function (config) {
      window.peer = new Native(relay ? { ...config, iceTransportPolicy: 'relay' } : config);
      return window.peer;
    };
  }, relay);
  const page = await context.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => message.type() === 'error' && errors.push(message.text()));
  await page.goto(url);
  return { page, errors };
}

// Fill in the greeting form and return the result and the progress shown.
async function greet(page, name) {
  await page.waitForSelector('.title', { timeout: 20_000 });
  assert.equal(await page.locator('.title').textContent(), 'Greet someone');
  await page.getByLabel('Name').fill(name);
  await page.locator('.run').click();
  await page.waitForFunction(() => document.querySelector('.bench').dataset.stage === 'done', null, { timeout: 15_000 });
  return { result: await page.getByLabel('Result text').textContent(), log: await page.locator('.log li').allTextContents() };
}

// The candidate types of the selected ICE pair: local, then remote.
const path = page => page.evaluate(async () => {
  const stats = [...(await window.peer.getStats()).values()];
  const byId = id => stats.find(report => report.id === id);
  const pair = byId(stats.find(report => report.type === 'transport').selectedCandidatePairId);
  return [byId(pair.localCandidateId).candidateType, byId(pair.remoteCandidateId).candidateType];
});

const reason = page => page.locator('#gate').getAttribute('data-reason');

// In a page at the loader origin: register as a hostile owner and return the
// link. A silent owner opens the channel and never proves anything; a flooding
// owner authenticates, then answers a run with more than 1 MB of output, or with
// endless empty messages ('empty') or progress updates ('chatty'), or sends empty
// messages unasked before any run ('idle').
async function hostileOwner({ mode, gui }) {
  const socket = new WebSocket(`ws://${location.host}/api/v1/share`);
  const messages = [];
  let wake;
  socket.onmessage = ({ data }) => { messages.push(JSON.parse(data)); wake?.(); };
  const next = async () => {
    while (!messages.length) await new Promise(resolve => { wake = resolve; });
    return messages.shift();
  };
  const { task } = await next();
  const key = crypto.getRandomValues(new Uint8Array(32));
  const base64url = bytes => bytes.toBase64({ alphabet: 'base64url', omitPadding: true });
  (async () => {
    const offer = await next();
    const peer = new RTCPeerConnection({ iceServers: offer.ice });
    window[mode] = peer;
    peer.ondatachannel = ({ channel }) => {
      if (mode === 'silent') return;
      channel.onmessage = async ({ data }) => {
        const message = JSON.parse(data);
        if (message.nonce) {
          const fp = description => description.sdp.match(/a=fingerprint:sha-256 (\S+)/)[1].replaceAll(':', '').toLowerCase();
          const nonce = base64url(crypto.getRandomValues(new Uint8Array(32)));
          const hmac = await crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
          const transcript = ['runlink-auth-v1', 'runner', task, fp(peer.remoteDescription), fp(peer.localDescription), message.nonce, nonce].join('\n');
          const proof = new Uint8Array(await crypto.subtle.sign('HMAC', hmac, new TextEncoder().encode(transcript)));
          channel.send(JSON.stringify({ nonce, proof: base64url(proof) }));
        } else if (message.proof) {
          channel.send(new Uint8Array(gui));
          channel.send(JSON.stringify({ definition: { title: 'Flood', output: { kind: 'text' } } }));
          if (mode === 'idle') for (let i = 0; i < 5000; i++) channel.send(new Uint8Array(0));
        } else if (message.values && mode !== 'idle') {
          channel.send(JSON.stringify({ stage: 'running' }));
          if (mode === 'chatty') for (let i = 0; i < 5000; i++) channel.send(JSON.stringify({ stage: 'running', message: 'busy' }));
          else for (let i = 0; i < (mode === 'empty' ? 5000 : 80); i++) channel.send(new Uint8Array(mode === 'empty' ? 0 : 16384));
        }
      };
    };
    await peer.setRemoteDescription({ type: 'offer', sdp: offer.sdp });
    await peer.setLocalDescription();
    await new Promise(resolve => {
      const check = () => { if (peer.iceGatheringState === 'complete') resolve(); };
      peer.onicegatheringstatechange = check;
      check();
    });
    socket.send(JSON.stringify({ session: offer.session, sdp: peer.localDescription.sdp }));
  })();
  return `${location.origin}/${task}#${base64url(key)}`;
}

let browser, secret;
const keys = [];
const owners = [];
try {
  const stack = await within(ready, 10_000, 'runlink-dev startup');
  secret = (await readFile(join(state, 'turn-secret'), 'utf8')).trim();
  // Raw host candidates, as on a network without mDNS, make the direct path deterministic.
  browser = await chromium.launch({ args: ['--disable-features=WebRtcHideLocalIpsWithMdns'] });
  const page = await browser.newPage();
  // The loader origin and CSP the owner-served GUI will run WebRTC under.
  await page.goto(`${stack.frontend}/12345678-1234-1234-1234-123456789abc`);

  for (const transport of ['udp', 'tcp']) {
    const url = `turn:${stack.turn}?transport=${transport}`;
    const result = await within(page.evaluate(relayProbe, { url, ...restCredential(secret, `probe-${transport}`) }),
      15_000, `TURN ${transport} relay`);
    assert.deepEqual(result, { message: 'ping', local: 'relay', remote: 'relay', protocol: transport });
  }
  const url = `turn:${stack.turn}?transport=udp`;
  const forged = { ...restCredential('0'.repeat(64), 'forged') };
  const expired = restCredential(secret, 'expired', -60);
  for (const credential of [forged, expired]) {
    assert.deepEqual(await within(page.evaluate(rejectedProbe, { url, ...credential }), 30_000, 'rejected TURN credential'),
      { relays: 0, rejected: true });
  }

  // One-click link: the recipient opens it and runs the task, directly and through TURN.
  const shared = share(stack.frontend, 'examples/greet.json');
  owners.push(shared.owner);
  const { url: link } = await within(shared.link, 10_000, 'runlink share');
  keys.push(new URL(link).hash.slice(1));
  for (const relay of [false, true]) {
    const { page, errors } = await recipient(link, relay);
    assert.equal(new URL(page.url()).hash, '');
    const { result, log } = await within(greet(page, relay ? 'Relay' : 'Ada'), 40_000, 'running the shared task');
    assert.equal(result, `Hi ${relay ? 'Relay' : 'Ada'}, good to see you!\n`);
    assert.deepEqual(log, [`Choosing words for ${relay ? 'Relay' : 'Ada'}`]);
    const [local, remote] = await path(page);
    if (relay) assert.equal(local, 'relay');
    else assert(local !== 'relay' && remote !== 'relay', `direct path used ${local}/${remote}`);
    assert.deepEqual(await page.evaluate(() => ({ local: { ...localStorage }, session: { ...sessionStorage } })), { local: {}, session: {} });
    assert.deepEqual(errors, []);
    await page.context().close();
  }

  // A file in and a file out, over the relay path: the recipient's file reaches the
  // owner's script, and the sorted list downloads.
  const sorter = share(stack.frontend, 'examples/sort.json');
  owners.push(sorter.owner);
  const { url: sortLink } = await within(sorter.link, 10_000, 'runlink share sort');
  keys.push(new URL(sortLink).hash.slice(1));
  const sorting = await recipient(sortLink, true);
  await sorting.page.waitForSelector('.title', { timeout: 20_000 });
  const numbers = Array.from({ length: 100_000 }, (_, i) => String(99_999 - i).padStart(5, '0'));
  await sorting.page.locator('input[type=file]').setInputFiles({ name: 'numbers.txt', mimeType: 'text/plain', buffer: Buffer.from(`${numbers.join('\n')}\n`) });
  await sorting.page.locator('.run').click();
  await sorting.page.waitForFunction(() => document.querySelector('.bench').dataset.stage === 'done', null, { timeout: 30_000 });
  const [download] = await Promise.all([sorting.page.waitForEvent('download'), sorting.page.locator('a.download').click()]);
  assert.equal(download.suggestedFilename(), 'sorted.txt');
  assert.equal(await readFile(await download.path(), 'utf8'), `${numbers.toReversed().join('\n')}\n`);
  assert.deepEqual(await sorting.page.locator('.log li').allTextContents(), ['Sorting 100000 lines']);
  assert.equal((await path(sorting.page))[0], 'relay');
  assert.deepEqual(sorting.errors, []);
  await sorting.page.context().close();

  // Separate secret: a wrong secret is refused by the owner's machine, the right one opens the task.
  const apart = share(stack.frontend, 'examples/greet.json', '--separate-secret');
  owners.push(apart.owner);
  const { url: bare, key } = await within(apart.link, 10_000, 'runlink share --separate-secret');
  keys.push(key);
  assert(!bare.includes('#'));
  const separate = await recipient(bare);
  await separate.page.locator('#secret').fill('A'.repeat(43));
  await separate.page.locator('#secret-form button').click();
  await separate.page.waitForFunction(() => document.getElementById('secret-error').textContent.includes("wasn't accepted"), null, { timeout: 20_000 });
  assert.equal(await reason(separate.page), 'secret');
  await separate.page.locator('#secret').fill(key);
  await separate.page.locator('#secret-form button').click();
  assert.equal((await within(greet(separate.page, 'Bo'), 40_000, 'separate-secret task')).result, 'Hi Bo, good to see you!\n');
  assert.deepEqual(separate.errors, []);

  // Stopping the share ends the link: a new visitor learns it is not active.
  const stopped = once(shared.owner, 'exit');
  shared.owner.kill('SIGINT');
  assert.deepEqual(await within(stopped, 5_000, 'runlink share shutdown'), [0, null]);
  assert(shared.events.some(line => line.endsWith('A recipient connected.')), shared.events.join('\n'));
  const late = await recipient(link);
  await late.page.waitForFunction(() => document.getElementById('gate').dataset.reason === 'inactive', null, { timeout: 10_000 });

  // Input over 64 KB is refused before sending, and the session stays usable. When the
  // owner's machine vanishes mid-run, the page reports the lost connection.
  const task = join(state, 'echo.json');
  await writeFile(task, JSON.stringify({
    title: 'Echo', command: ['sh', '-c', 'sleep "$1"; cat', 'sh', '{wait}'], output: { kind: 'text' },
    inputs: [{ name: 'text', type: 'multiline', label: 'Text' }, { name: 'wait', type: 'number', label: 'Wait', min: 0, default: 0 }],
  }));
  const echo = share(stack.frontend, task);
  owners.push(echo.owner);
  const { url: echoLink } = await within(echo.link, 10_000, 'runlink share echo');
  keys.push(new URL(echoLink).hash.slice(1));
  const { page: echoPage } = await recipient(echoLink);
  await echoPage.waitForSelector('.title', { timeout: 20_000 });
  const fault = () => echoPage.locator('.bench').getAttribute('data-fault');
  await echoPage.getByLabel('Text').fill('x'.repeat(70_000));
  await echoPage.locator('.run').click();
  await echoPage.waitForFunction(() => document.querySelector('.bench').dataset.fault === 'invalid');
  assert.match(await echoPage.locator('.fault .detail').textContent(), /larger than 64 KB/);
  await echoPage.getByLabel('Text').fill('hi');
  await echoPage.locator('.run').click();
  await echoPage.waitForFunction(() => document.querySelector('.bench').dataset.stage === 'done', null, { timeout: 15_000 });
  assert.equal(await echoPage.getByLabel('Result text').textContent(), '{"text":"hi","wait":0}');
  await echoPage.getByLabel('Wait').fill('30');
  await echoPage.locator('.run').click();
  await echoPage.waitForFunction(() => document.querySelector('.bench').dataset.stage === 'running', null, { timeout: 15_000 });
  echo.owner.kill('SIGKILL');
  await echoPage.waitForFunction(() => document.querySelector('.bench').dataset.fault === 'disconnected', null, { timeout: 60_000 });
  assert.equal(await fault(), 'disconnected');

  // A hostile owner's machine cannot hold the page or flood it: a silent one is
  // dropped at the setup deadline, and output past 1 MB ends the connection.
  const hostile = await browser.newPage();
  await hostile.goto(`${stack.frontend}/`);
  const gui = [...await readFile('internal/frontend/assets/gui.js')];
  const silent = await recipient(await hostile.evaluate(hostileOwner, { mode: 'silent', gui }));
  for (const [mode, detail] of [['flood', /larger than 1 MB/], ['empty', /larger than 1 MB/], ['chatty', /more updates/]]) {
    const flood = await recipient(await hostile.evaluate(hostileOwner, { mode, gui }));
    await flood.page.waitForSelector('.title', { timeout: 20_000 });
    await flood.page.locator('.run').click();
    await flood.page.waitForFunction(() => document.querySelector('.bench').dataset.fault === 'failed', null, { timeout: 20_000 });
    assert.match(await flood.page.locator('.fault .detail').textContent(), detail);
  }
  // Unasked messages past the bound end the connection; the next run reports it.
  const idle = await recipient(await hostile.evaluate(hostileOwner, { mode: 'idle', gui }));
  await idle.page.waitForSelector('.title', { timeout: 20_000 });
  await idle.page.waitForTimeout(500);
  await idle.page.locator('.run').click();
  await idle.page.waitForFunction(() => document.querySelector('.bench').dataset.fault === 'disconnected', null, { timeout: 20_000 });
  await silent.page.waitForFunction(() => document.getElementById('gate').dataset.reason === 'unreachable', null, { timeout: 45_000 });
  console.log('Chromium: WebRTC data over TURN UDP and TCP relays of the local stack; forged and expired credentials rejected; ' +
    'a shared task completed from its link over direct and relay-only paths and with a separate secret; a file sorted and downloaded over a relay; ' +
    'a wrong secret refused; a stopped share reported inactive; oversized input refused; a vanished owner reported mid-run; ' +
    'a silent owner dropped at the setup deadline and a flooding one cut off past 1 MB');
} finally {
  for (const owner of owners) if (owner.exitCode === null) owner.kill('SIGKILL');
  const exited = once(child, 'exit');
  child.kill('SIGTERM');
  await browser?.close();
  const [code] = await within(exited, 5_000, 'runlink-dev shutdown').catch(error => { child.kill('SIGKILL'); throw error; });
  await rm(state, { recursive: true });
  assert.equal(code, 0, logs.join('\n'));
  for (const line of logs) {
    assert(line.startsWith('{') && ![secret, ...keys].some(key => key && line.includes(key)), `unexpected log line: ${line}`);
  }
}
