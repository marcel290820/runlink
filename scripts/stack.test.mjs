// Drive Chromium WebRTC through the local stack's TURN stand-in, relay paths only.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHmac } from 'node:crypto';
import { once } from 'node:events';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
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

let browser, secret;
try {
  const stack = await within(ready, 10_000, 'runlink-dev startup');
  secret = (await readFile(join(state, 'turn-secret'), 'utf8')).trim();
  browser = await chromium.launch();
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
  console.log('Chromium: WebRTC data over TURN UDP and TCP relays of the local stack; forged and expired credentials rejected');
} finally {
  const exited = once(child, 'exit');
  child.kill('SIGTERM');
  await browser?.close();
  const [code] = await within(exited, 5_000, 'runlink-dev shutdown').catch(error => { child.kill('SIGKILL'); throw error; });
  await rm(state, { recursive: true });
  assert.equal(code, 0, logs.join('\n'));
  for (const line of logs) assert(line.startsWith('{') && !(secret && line.includes(secret)), `unexpected log line: ${line}`);
}
