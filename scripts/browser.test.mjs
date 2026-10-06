// Drive the trusted frontend in Chromium against a hostile upstream.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { createInterface } from 'node:readline';
import { chromium } from 'playwright';

const TASK_PATH = '/12345678-1234-1234-1234-123456789abc';

function within(promise, ms, what) {
  let timer;
  const timeout = new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`${what} timed out`)), ms); });
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

// Start the frontend on an ephemeral port; it logs server_started with its address when ready.
function startFrontend(upstream) {
  const child = spawn('build/runlink-server',
    ['--role', 'frontend', '--listen', '127.0.0.1:0', '--upstream', upstream],
    { stdio: ['ignore', 'ignore', 'pipe'] });
  const logs = [];
  const origin = new Promise((resolve, reject) => {
    createInterface({ input: child.stderr }).on('line', line => {
      logs.push(line);
      if (logs.length > 1) return;
      const event = line.startsWith('{') ? JSON.parse(line) : {};
      if (event.msg !== 'server_started') return reject(new Error(`runlink-server did not start: ${line}`));
      const origin = `http://${event.address}`;
      fetch(`${origin}/readyz`).then(async response => {
        assert.equal(response.status, 200);
        assert.deepEqual(await response.json(), { status: 'ready' });
        resolve(origin);
      }).catch(reject);
    });
    child.once('exit', code => reject(new Error(`runlink-server exited with ${code}: ${logs.join('\n')}`)));
  });
  return { child, logs, origin };
}

// Require a clean SIGTERM exit from a still-running child, force-killing it if none comes.
async function stop(child, logs) {
  assert(child.exitCode === null && child.signalCode === null, `runlink-server exited early: ${logs.join('\n')}`);
  const exited = once(child, 'exit');
  child.kill('SIGTERM');
  try {
    const [code] = await within(exited, 5_000, 'runlink-server shutdown');
    assert.equal(code, 0);
  } finally {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
  }
}

// Every response would execute, persist, or install a worker if A relayed it.
const upstream = createServer((request, response) => {
  response.writeHead(200, {
    'content-type': 'text/javascript', 'service-worker-allowed': '/',
    'set-cookie': 'attacker=true', 'content-security-policy': "default-src * 'unsafe-inline'",
  });
  response.end('globalThis.pwned = true');
});
upstream.listen(0, '127.0.0.1');
await once(upstream, 'listening');

const frontend = startFrontend(`http://127.0.0.1:${upstream.address().port}`);
let browser;
try {
  const origin = await within(frontend.origin, 10_000, 'runlink-server startup');
  browser = await chromium.launch();
  const page = await browser.newPage();
  const errors = [];
  const requests = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('request', request => requests.push(request.url()));
  const status = () => page.locator('#status').textContent();

  // The fragment secret is captured, then removed before anything else runs.
  await page.goto(`${origin}${TASK_PATH}#fixture-secret`);
  await page.waitForFunction(() => document.getElementById('status').textContent.startsWith('Link captured'));
  assert.equal(new URL(page.url()).hash, '');
  assert.deepEqual(await page.evaluate(() => ({ state: history.state, local: { ...localStorage }, session: { ...sessionStorage } })),
    { state: null, local: {}, session: {} });
  assert(!requests.some(url => url.includes('fixture-secret')));

  // Reloading needs the secret again; entering it clears the field.
  await page.reload();
  await page.waitForFunction(() => document.getElementById('status').textContent.startsWith('Enter'));
  await page.locator('#secret').fill('separately-delivered-fixture');
  await page.locator('button').click();
  assert.equal(await page.locator('#secret').inputValue(), '');
  assert((await status()).startsWith('Secret captured'));

  // Only the approved GUI bytes execute.
  const gui = [...await readFile('internal/frontend/assets/gui.js')];
  const mounted = await page.evaluate(async data => {
    const { verifyGUI, mountGUI } = await import('/assets/loader.mjs');
    const bytes = new Uint8Array(data);
    await verifyGUI(bytes);
    let rejected = false;
    try { await mountGUI(new Uint8Array([...data, 32])); } catch { rejected = true; }
    await mountGUI(bytes);
    return { rejected, text: document.getElementById('task').textContent };
  }, gui);
  assert(mounted.rejected);
  assert(mounted.text.includes('Task workspace'));

  // The hostile body cannot run as a worker, a script, or a navigated document.
  assert(await page.evaluate(async () => {
    try { await navigator.serviceWorker.register('/api/v1/status'); return false; } catch { return true; }
  }));
  await page.evaluate(() => new Promise(resolve => {
    const script = document.createElement('script');
    script.onload = resolve;
    script.onerror = resolve;
    script.src = trustedTypes.createPolicy('not-allowed', { createScriptURL: value => value }).createScriptURL('/api/v1/status');
    document.head.append(script);
  })).catch(() => {}); // Trusted Types refuses the extra policy, which is the point
  assert.equal(await page.evaluate(() => globalThis.pwned), undefined);
  const response = await page.goto(`${origin}/api/v1/status`);
  assert.equal(response.status(), 502);
  assert.equal(response.headers()['content-type'], 'application/json');
  assert(!response.headers()['service-worker-allowed']);
  assert.equal(await page.evaluate(() => globalThis.pwned), undefined);
  assert.deepEqual(await browser.contexts()[0].cookies(), []);
  assert.equal((await fetch(`${origin}/assets/loader.mjs`, { headers: { 'Service-Worker': 'script' } })).status, 403);
  assert.deepEqual(errors, []);
  console.log('Chromium: fragment cleanup, reload, secret entry, GUI verification/execution, ingress and worker isolation passed');
} finally {
  // Every teardown step runs even when another fails; the first failure is reported.
  upstream.close();
  const teardown = await Promise.allSettled([browser?.close(), stop(frontend.child, frontend.logs)]);
  const failure = teardown.find(result => result.status === 'rejected');
  if (failure) throw failure.reason;
  assert(!frontend.logs.join('\n').includes('fixture-secret'));
}
