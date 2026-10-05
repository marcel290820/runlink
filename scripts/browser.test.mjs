import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { chromium } from 'playwright';

async function start(args) {
  const process = spawn('./build/runlink-server', args, { stdio: ['ignore', 'ignore', 'pipe'] });
  let logs = '';
  process.stderr.on('data', value => { logs += value; });
  const url = 'http://127.0.0.1:18080';
  for (let i = 0; i < 100; i++) {
    if (process.exitCode !== null) throw new Error(`Server exited: ${logs}`);
    try { if ((await fetch(`${url}/readyz`)).ok) return { process, url, logs: () => logs }; } catch {}
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  process.kill('SIGTERM');
  throw new Error('Server readiness timed out');
}
const upstream = createServer((request, response) => {
  response.writeHead(200, { 'content-type': 'text/javascript', 'service-worker-allowed': '/',
    'set-cookie': 'attacker=true', 'content-security-policy': "default-src * 'unsafe-inline'" });
  response.end('globalThis.pwned = true');
});
upstream.listen(0, '127.0.0.1');
await once(upstream, 'listening');
let server;
let browser;
try {
  server = await start(['--role', 'frontend', '--listen', '127.0.0.1:18080', '--upstream', `http://127.0.0.1:${upstream.address().port}`]);
  browser = await chromium.launch();
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const requests = [];
  page.on('request', request => requests.push(request.url()));
  const task = `${server.url}/12345678-1234-1234-1234-123456789abc`;
  await page.goto(`${task}#fixture-secret`);
  await page.waitForFunction(() => document.getElementById('status').textContent.startsWith('Link captured'));
  assert.equal(new URL(page.url()).hash, '');
  assert.deepEqual(await page.evaluate(() => ({ state: history.state, local: { ...localStorage }, session: { ...sessionStorage } })),
    { state: null, local: {}, session: {} });
  assert(!requests.some(value => value.includes('fixture-secret')));
  await page.reload();
  await page.waitForFunction(() => document.getElementById('status').textContent.startsWith('Enter'));
  await page.locator('#secret').fill('separately-delivered-fixture');
  await page.locator('button').click();
  assert.equal(await page.locator('#secret').inputValue(), '');
  const gui = [...await readFile('internal/frontend/assets/gui.js')];
  const result = await page.evaluate(async data => {
    const { verifyGUI, mountGUI } = await import('/assets/loader.mjs');
    const bytes = new Uint8Array(data);
    await verifyGUI(bytes);
    let rejected = false;
    try { await mountGUI(new Uint8Array([...data, 32])); } catch { rejected = true; }
    await mountGUI(bytes);
    return { rejected, text: document.getElementById('task').textContent };
  }, gui);
  assert(result.rejected);
  assert(result.text.includes('Task workspace'));
  const rejected = await page.evaluate(async () => {
    try { await navigator.serviceWorker.register('/api/v1/status'); return false; } catch { return true; }
  });
  assert(rejected);
  // The malicious body cannot execute as a script, a navigation, or a worker.
  await page.evaluate(() => new Promise(resolve => {
    const script = document.createElement('script');
    script.onload = resolve; script.onerror = resolve;
    const policy = trustedTypes.createPolicy('not-allowed', { createScriptURL: value => value });
    script.src = policy.createScriptURL('/api/v1/status');
    document.head.append(script);
  })).catch(() => {});
  assert.equal(await page.evaluate(() => globalThis.pwned), undefined);
  const response = await page.goto(`${server.url}/api/v1/status`);
  assert.equal(response.status(), 502);
  assert.equal(response.headers()['content-type'], 'application/json');
  assert(!response.headers()['service-worker-allowed']);
  assert.equal(await page.evaluate(() => globalThis.pwned), undefined);
  assert.deepEqual(await browser.contexts()[0].cookies(), []);
  const sw = await fetch(`${server.url}/assets/loader.mjs`, { headers: { 'Service-Worker': 'script' } });
  assert.equal(sw.status, 403);
  assert.equal(errors.length, 0, errors.join('\n'));
  console.log('Chromium: fragment cleanup, reload, secret entry, GUI verification/execution, ingress and worker isolation passed');
} finally {
  if (browser) await browser.close();
  if (server) {
    const exited = once(server.process, 'exit');
    server.process.kill('SIGTERM');
    const [code] = await exited;
    assert.equal(code, 0);
    assert(!server.logs().includes('fixture-secret'));
  }
  upstream.close();
}
