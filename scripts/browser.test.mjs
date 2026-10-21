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
// A task as the authenticated transport will hand it to the GUI. Its run records
// each call and settles only when the test says so.
const TASK = {
  title: 'Rename <img src=x onerror=globalThis.pwned=1> files',
  description: 'Fixture task.',
  owner: 'Dana',
  output: { kind: 'file', label: 'a ZIP archive' },
  inputs: [
    { name: 'prefix', type: 'text', label: 'Prefix', required: true },
    { name: 'count', type: 'number', label: 'Count', min: 1, max: 9, step: 1, required: true },
    { name: 'dry', type: 'boolean', label: 'Dry run', default: true },
    { name: 'region', type: 'choice', label: 'Region', options: ['EMEA', { value: 2, label: 'Americas' }], default: 'EMEA', required: true },
    { name: 'list', type: 'file', label: 'List', accept: '.csv' },
    // Named controls become form properties, so this name would break submission.
    { name: 'contains', type: 'text', label: 'Note' },
    { name: 'tone', type: 'choice', label: 'Tone', options: ['Warm', 'Cool'] },
  ],
};
function installTask(definition) {
  // Record the type of every object URL the GUI creates.
  const create = URL.createObjectURL;
  window.blobTypes = [];
  URL.createObjectURL = blob => {
    if (blob.type !== 'text/javascript') window.blobTypes.push(blob.type);
    return create.call(URL, blob);
  };
  window.calls = [];
  window.task = {
    definition,
    run: (values, progress) => new Promise((resolve, reject) => window.calls.push({ values, progress, resolve, reject })),
  };
}

try {
  const origin = await within(frontend.origin, 10_000, 'runlink-server startup');
  browser = await chromium.launch();
  const page = await browser.newPage();
  const errors = [];
  const logged = [];
  const requests = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => message.type() === 'error' && logged.push(message.text()));
  page.on('request', request => requests.push(request.url()));
  page.on('websocket', socket => requests.push(socket.url()));
  const state = () => page.locator('#gate').getAttribute('data-state');

  // The fragment secret is captured, then removed before anything else runs. A
  // fragment that cannot be a whole secret is reported as an incomplete link.
  await page.goto(`${origin}${TASK_PATH}#fixture-secret`);
  await page.waitForFunction(() => document.getElementById('gate').dataset.reason === 'incomplete');
  assert.equal(new URL(page.url()).hash, '');
  assert.deepEqual(await page.evaluate(() => ({ state: history.state, local: { ...localStorage }, session: { ...sessionStorage } })),
    { state: null, local: {}, session: {} });
  assert(!requests.some(url => url.includes('fixture-secret')));
  assert.match(await page.locator('#note').textContent(), /browser history/);
  // The trusted fonts load under the page CSP; a refused font would log a console error.
  assert.deepEqual(await page.evaluate(async () => {
    await Promise.all([document.fonts.load('16px Archivo'), document.fonts.load('16px "Martian Mono"')]);
    return [...document.fonts].filter(font => font.status === 'loaded').map(font => font.family.replaceAll('"', '')).sort();
  }), ['Archivo', 'Martian Mono']);

  // Reloading needs the secret again; blank and partial entries are refused, and entering one clears the field.
  await page.reload();
  await page.waitForFunction(() => document.getElementById('gate').dataset.state === 'secret');
  assert.equal(await page.evaluate(() => document.activeElement.id), 'secret');
  for (const entry of ['   ', 'separately-delivered-fixture']) {
    await page.locator('#secret').fill(entry);
    await page.locator('#secret-form button').click();
    assert.equal(await state(), 'secret');
    assert.equal(await page.locator('#secret').getAttribute('aria-invalid'), 'true');
    assert.equal(await page.locator('#secret').inputValue(), '');
  }
  assert.equal(await page.evaluate(() => window.runlinkSession.consume()), '');
  // A hostile upstream cannot complete signaling, so the loader reports the owner unreachable and offers a retry.
  const secret = 'separately-delivered-fixture-0123456789abcd';
  await page.locator('#secret').fill(` ${secret} `);
  await page.locator('#secret-form button').click();
  assert.equal(await page.locator('#secret').inputValue(), '');
  await page.waitForFunction(() => document.getElementById('gate').dataset.reason === 'unreachable');
  assert(await page.locator('#retry').isVisible());
  assert(requests.some(url => url.endsWith(`/api/v1/connect${TASK_PATH}`)));
  assert(!requests.some(url => url.includes('fixture-secret') || url.includes(secret)));
  // The browser itself logs the refused handshake: A answered with its own 502.
  assert.deepEqual(logged.splice(0).map(text => text.replace(/127\.0\.0\.1:\d+/, 'A')),
    [`WebSocket connection to 'ws://A/api/v1/connect${TASK_PATH}' failed: Error during WebSocket handshake: Unexpected response code: 502`]);

  // Only the approved GUI bytes execute, and they replace the gate.
  const gui = [...await readFile('internal/frontend/assets/gui.js')];
  await page.evaluate(installTask, TASK);
  const mounted = await page.evaluate(async data => {
    const { verifyGUI, mountGUI } = await import('/assets/loader.mjs');
    const bytes = new Uint8Array(data);
    await verifyGUI(bytes);
    let rejected = false;
    try { await mountGUI(new Uint8Array([...data, 32]), window.task); } catch { rejected = true; }
    await mountGUI(bytes, window.task);
    return { rejected, title: document.querySelector('.title').textContent, gate: document.getElementById('gate-view').hidden };
  }, gui);
  assert.deepEqual(mounted, { rejected: true, title: TASK.title, gate: true });
  assert.equal(await page.locator('#task img').count(), 0);
  // The page's helix draws along the task's wires: a strip across the middle of the
  // result-side wire holds its strands once the helix has unrolled. Nothing is drawn
  // out of view, so the wire is scrolled in first.
  await page.locator('.w2').scrollIntoViewIfNeeded();
  await page.waitForFunction(() => {
    const canvas = document.getElementById('helix'), wire = document.querySelector('.w2').getBoundingClientRect();
    const box = canvas.getBoundingClientRect(), scale = canvas.width / box.width;
    if (canvas.hidden || !canvas.width) return false;
    const x = Math.round((wire.left + wire.width / 2 - box.left) * scale), y = Math.round((wire.top - box.top - 40) * scale);
    const { data } = canvas.getContext('2d').getImageData(x - Math.round(20 * scale), y, Math.ceil(40 * scale), Math.ceil(80 * scale));
    return data.filter((alpha, i) => i % 4 === 3 && alpha).length >= 60 * scale * scale;
  });

  // Required input is checked before anything is sent.
  const calls = () => page.evaluate(() => window.calls.length);
  await page.locator('.run').click();
  assert.equal(await calls(), 0);
  assert.equal(await page.locator('#input-0').getAttribute('aria-invalid'), 'true');
  assert.equal(await page.evaluate(() => document.activeElement.id), 'input-0');
  await page.locator('#input-0').fill('night-');
  await page.locator('#input-1').fill('12');
  await page.locator('.run').click();
  assert.equal(await calls(), 0);
  assert.match(await page.locator('#input-1-error').textContent(), /from 1 to 9/);
  await page.locator('#input-1').fill('3');
  // A rejected file keeps its input focusable while the picked file shows, and removing it clears the error.
  await page.locator('#input-4').setInputFiles({ name: 'names.txt', mimeType: 'text/plain', buffer: Buffer.from('x') });
  await page.locator('.run').click();
  assert.equal(await calls(), 0);
  assert.equal(await page.locator('#input-4').getAttribute('aria-invalid'), 'true');
  assert.match(await page.locator('#input-4-error').textContent(), /\.csv/);
  assert.equal(await page.evaluate(() => document.activeElement.id), 'input-4');
  await page.getByRole('button', { name: 'Remove names.txt' }).click();
  assert.equal(await page.locator('#input-4').getAttribute('aria-invalid'), null);
  assert.equal(await page.evaluate(() => document.activeElement.id), 'input-4');
  await page.locator('.segments label', { hasText: 'Americas' }).click();
  // An optional choice without a preset starts on None, which sends nothing.
  assert(await page.getByRole('radio', { name: 'None' }).isChecked());
  await page.locator('#input-5').fill('quiet');
  await page.locator('#input-5').press('Control+Enter');

  // Values arrive typed; the empty optional fields are left out.
  assert.deepEqual(await page.evaluate(() => window.calls[0].values), { prefix: 'night-', count: 3, dry: true, region: 2, contains: 'quiet' });
  const stage = () => page.locator('.bench').getAttribute('data-stage');
  assert.equal(await stage(), 'sending');
  // Acceptance is not execution: the clock starts only once the task runs.
  await page.evaluate(() => window.calls[0].progress({ stage: 'accepted' }));
  assert.equal(await stage(), 'accepted');
  assert.equal(await page.locator('.readout').textContent(), 'Accepted');
  assert.equal(await page.locator('.readout.time').count(), 0);
  await page.evaluate(() => window.calls[0].progress({ stage: 'running', message: 'Renaming 3 files', fraction: 0.5 }));
  assert.equal(await stage(), 'running');
  assert.equal(await page.locator('.readout.time').count(), 1);
  assert.equal(await page.locator('.log li').last().textContent(), 'Renaming 3 files');
  // While a run is in flight each value is locked in place as text and Run gives way to a status line.
  assert(await page.locator('.run').isHidden());
  assert(await page.locator('.sent').isVisible());
  assert(await page.locator('#input-0').isDisabled());
  assert.deepEqual(await page.locator('.shown').allTextContents(), ['night-', '3', 'Yes', 'Americas', 'None', 'quiet', 'None']);
  assert(await page.locator('.shown').first().isVisible());

  // Files are offered only as downloads, never rendered in this origin.
  await page.evaluate(() => window.calls[0].resolve([
    { kind: 'file', name: 'renamed.zip', blob: new Blob(['<script>globalThis.pwned=1</script>'], { type: 'text/html' }) },
    { kind: 'text', label: 'Log', value: '<b>3 renamed</b>' },
    { kind: 'file', name: 'names.csv', blob: new Blob(['name,count,note\r\n"Doe, Jane",1200,"said ""hi"""\r\nRoe,7,'], { type: 'text/csv' }) },
    { kind: 'json', value: { order_id: 7, eta: '2026-10-14', giftWrapped: false } },
    { kind: 'file', name: 'values.csv', blob: new Blob(['value\n""\n\nhello\n'], { type: 'text/csv' }) },
    // A raster image shows as a picture whatever its name says; an SVG never does.
    { kind: 'file', name: 'chart.bin', blob: new Blob([Uint8Array.from(atob(
      'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='), c => c.charCodeAt(0))]) },
    { kind: 'file', name: 'logo.svg', blob: new Blob(['<svg xmlns="http://www.w3.org/2000/svg" onload="globalThis.pwned=1"/>'], { type: 'image/svg+xml' }) },
  ]));
  assert.equal(await stage(), 'done');
  const download = page.locator('a.download').first();
  assert.equal(await download.getAttribute('download'), 'renamed.zip');
  await page.waitForSelector('img.picture');
  assert.deepEqual(await page.evaluate(() => window.blobTypes.sort()), [...Array(5).fill('application/octet-stream'), 'image/png']);
  assert.equal(await page.locator('img.picture').count(), 1);
  assert.equal(await page.locator('img.picture').evaluate(img => img.naturalWidth), 1);
  const [saved] = await Promise.all([page.waitForEvent('download'), download.click()]);
  assert.equal(saved.suggestedFilename(), 'renamed.zip');
  assert.equal(await page.getByLabel('Result text').textContent(), '<b>3 renamed</b>');
  // A CSV shows its first rows, quotes and a final empty cell included, with numbers on the
  // right; a text-like file gets a preview, still as text.
  await page.waitForSelector('.sheet');
  assert.deepEqual(await page.locator('.sheet').first().evaluate(table => [...table.rows].map(row => [...row.cells].map(cell => cell.textContent))),
    [['name', 'count', 'note'], ['Doe, Jane', '1200', 'said "hi"'], ['Roe', '7', '']]);
  assert.deepEqual(await page.locator('.sheet').first().locator('.num').allTextContents(), ['count', '1200', '7']);
  // A quoted empty record is data; a blank line is not.
  await page.waitForFunction(() => document.querySelectorAll('.sheet').length === 2);
  assert.deepEqual(await page.locator('.sheet').nth(1).evaluate(table => [...table.rows].map(row => [...row.cells].map(cell => cell.textContent))),
    [['value'], [''], ['hello']]);
  assert.deepEqual(await page.locator('.pairs dt').allTextContents(), ['Order ID', 'ETA', 'Gift wrapped']);
  assert.equal(await page.locator('.peek pre').textContent(), '<script>globalThis.pwned=1</script>');
  assert.equal(await page.locator('.result b').count(), 0);
  assert.match(await page.title(), /^Done/);

  // The runner's field errors land on the fields; uncoded errors reveal nothing.
  await page.locator('.run').click();
  await page.evaluate(() => window.calls[1].reject(Object.assign(new Error('Prefix in use'), { code: 'invalid', fields: { prefix: 'Already taken.' } })));
  assert.equal(await page.locator('.bench').getAttribute('data-fault'), 'invalid');
  assert.match(await page.locator('#input-0-error').textContent(), /Already taken/);
  // The owner's message reads in full in the result.
  assert.equal(await page.locator('.fault .detail').textContent(), 'Prefix in use');
  // The rejected field is editable again and holds focus.
  assert.equal(await page.evaluate(() => document.activeElement.id), 'input-0');
  assert(await page.locator('#input-0').isEnabled());
  await page.locator('.run').click();
  await page.evaluate(() => window.calls[2].reject(new TypeError('internal detail')));
  assert.equal(await page.locator('.bench').getAttribute('data-fault'), 'unknown');
  assert(!(await page.locator('.bench').textContent()).includes('internal detail'));

  // Nothing on the wire moves between states, even with a short form and a long owner name.
  const tops = await page.evaluate(async data => {
    const { mountGUI } = await import('/assets/loader.mjs');
    let run;
    const owner = 'University of California Berkeley Department of Civil and Environmental Engineering';
    await mountGUI(new Uint8Array(data), { definition: { title: 'Short', owner, inputs: [{ name: 'q', type: 'text' }] },
      run: (values, progress) => new Promise((resolve, reject) => { run = { progress, resolve, reject }; }) });
    // Page coordinates: submitting scrolls the machine into view.
    const top = () => Math.round(document.querySelector('.w2').getBoundingClientRect().top + scrollY);
    const seen = [top()];
    document.querySelector('.run').click();
    for (const message of ['one', 'two', 'three', 'four', 'five']) run.progress({ stage: 'running', message });
    seen.push(top());
    run.reject(Object.assign(new Error('x'.repeat(300)), { code: 'failed' }));
    await new Promise(resolve => setTimeout(resolve));
    return [...seen, top()];
  }, gui);
  assert.equal(new Set(tops).size, 1, `the wire moved: ${tops}`);

  // A field of several values takes a numbered row per value, chips for several
  // choices, or any number of files, and sends each list in order.
  await page.evaluate(installTask, { title: 'Lists', inputs: [
    { name: 'tags', type: 'text', label: 'Tags', multiple: true, required: true },
    { name: 'days', type: 'date', label: 'Days', multiple: true, default: ['2026-10-20'] },
    { name: 'only', type: 'choice', label: 'Only', multiple: true, options: ['a', { value: 2, label: 'Two' }, 'c'], default: ['c'] },
    { name: 'docs', type: 'file', label: 'Docs', multiple: true, accept: '.txt' },
  ] });
  await page.evaluate(async data => (await import('/assets/loader.mjs')).mountGUI(new Uint8Array(data), window.task), gui);
  assert.equal(await page.locator('.optional').count(), 3);
  await page.locator('.run').click();
  assert.equal(await calls(), 0);
  assert.match(await page.locator('#input-0-error').textContent(), /Enter a value/);
  const tags = page.locator('.field', { has: page.locator('#input-0') });
  await page.locator('#input-0').fill('first');
  for (const [i, text] of [[1, 'second'], [2, 'dropped']]) {
    await tags.getByRole('button', { name: 'Add another' }).click();
    assert.equal(await page.evaluate(() => document.activeElement.id), `input-0-${i}`);
    await page.locator(`#input-0-${i}`).fill(text);
  }
  await tags.getByRole('button', { name: 'Remove value 3' }).click();
  assert.equal(await tags.locator('.row').count(), 2);
  await page.locator('.checks label', { hasText: 'Two' }).click();
  const txt = name => ({ name, mimeType: 'text/plain', buffer: Buffer.from(name) });
  await page.locator('#input-3').setInputFiles([txt('a.txt'), txt('b.txt')]);
  await page.locator('#input-3').setInputFiles([txt('c.csv')]); // adds to the files, and is refused
  assert.equal(await page.locator('.pick').count(), 3);
  await page.locator('.run').click();
  assert.equal(await calls(), 0);
  assert.match(await page.locator('#input-3-error').textContent(), /c\.csv isn't a \.txt file/);
  await page.getByRole('button', { name: 'Remove c.csv' }).click();
  await page.locator('.run').click();
  assert.deepEqual(await page.evaluate(() => {
    const { docs, ...rest } = window.calls[0].values;
    return { ...rest, docs: docs.map(file => file.name) };
  }), { tags: ['first', 'second'], days: ['2026-10-20'], only: [2, 'c'], docs: ['a.txt', 'b.txt'] });
  const receipts = await page.locator('.shown').allTextContents();
  assert.deepEqual([receipts[0], ...receipts.slice(2)], ['first, second', 'Two, c', 'a.txt, b.txt']);
  await page.evaluate(() => window.calls[0].resolve([]));

  // A definition the GUI does not understand, down to one property's type, fails loudly instead of rendering.
  assert.deepEqual(await page.evaluate(async data => {
    const { mountGUI } = await import('/assets/loader.mjs');
    const inputs = [{ name: 'a', type: 'html' }, { name: 'a', type: ['text'] }, { name: 'a', type: 'file', accept: 5 },
      { name: 'a', type: 'choice', options: [{ value: {} }] }, { name: 'a', type: 'date', default: 'tomorrow' },
      { name: 'a', type: 'date', default: '2026-13-01' }, { name: 'a', type: 'date', max: '2026-02-30' },
      { name: 'a', type: 'date', default: '0000-01-01' }, { name: 'a', type: 'date', min: '0000-01-01' }, { name: 'a', type: 'date', max: '0000-12-31' },
      { name: 'a', type: 'boolean', multiple: true }, { name: 'a', type: 'text', multiple: true, default: 'x' }, { name: 'a', type: 'text', multiple: 'yes' }];
    return Promise.all(inputs.map(input => mountGUI(new Uint8Array(data), { definition: { title: 'x', inputs: [input] }, run() {} })
      .then(() => 'mounted', error => error.message)));
  }, gui), Array(13).fill('Unsupported task definition'));
  // CSP refusals, such as a blocked font, surface only as console errors.
  assert.deepEqual(logged, []);

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
  console.log('Chromium: fragment cleanup, reload, secret entry, fonts, GUI verification, input checks, run states, ' +
    'download-only files, fields of several values, ingress and worker isolation passed');
} finally {
  // Every teardown step runs even when another fails; the first failure is reported.
  upstream.close();
  const teardown = await Promise.allSettled([browser?.close(), stop(frontend.child, frontend.logs)]);
  const failure = teardown.find(result => result.status === 'rejected');
  if (failure) throw failure.reason;
  assert(!frontend.logs.join('\n').includes('fixture'));
}
