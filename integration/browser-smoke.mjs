// Real Chromium checks, using its DevTools protocol and Node's built-in WebSocket.
// Run against a disposable local database; this creates pilot runs and a draft.
import assert from 'node:assert/strict';
import {mkdir, writeFile} from 'node:fs/promises';

const origin = process.env.TREETEST_TEST_URL || 'http://127.0.0.1:8080';
const debug = process.env.CHROME_DEBUG_URL || 'http://127.0.0.1:9222';
const password = process.env.TREETEST_TEST_PASSWORD;
if (!password) throw new Error('Set TREETEST_TEST_PASSWORD for the disposable owner and analyst accounts.');
const artifacts = process.env.TEST_ARTIFACTS || '/tmp/opencode/docs-tree-test-browser';
await mkdir(artifacts, {recursive: true});

class Page {
  static async open() {
    const target = await (await fetch(`${debug}/json/new?about:blank`, {method: 'PUT'})).json();
    const page = new Page(new WebSocket(target.webSocketDebuggerUrl));
    page.targetID = target.id;
    await new Promise(resolve => page.socket.addEventListener('open', resolve, {once: true}));
    await page.send('Page.enable');
    await page.send('Runtime.enable');
    await page.send('Network.enable');
    return page;
  }
  constructor(socket) {
    this.socket = socket; this.nextID = 0; this.pending = new Map(); this.errors = [];
    socket.addEventListener('message', ({data}) => {
      const message = JSON.parse(data);
      if (message.id) {
        const callbacks = this.pending.get(message.id);
        this.pending.delete(message.id);
        if (message.error) callbacks.reject(new Error(JSON.stringify(message.error))); else callbacks.resolve(message.result);
      } else if (message.method === 'Runtime.exceptionThrown') this.errors.push(message.params.exceptionDetails);
    });
  }
  send(method, params = {}) {
    return new Promise((resolve, reject) => {
      const id = ++this.nextID;
      this.pending.set(id, {resolve, reject});
      this.socket.send(JSON.stringify({id, method, params}));
    });
  }
  async evaluate(expression) {
    const value = await this.send('Runtime.evaluate', {expression, awaitPromise: true, returnByValue: true, replMode: true});
    if (value.exceptionDetails) throw new Error(JSON.stringify(value.exceptionDetails));
    return value.result.value;
  }
  async wait(expression, description = expression) {
    const deadline = Date.now() + 15000;
    while (Date.now() < deadline) {
      try { if (await this.evaluate(expression)) return; } catch (error) { if (!error.message.includes('context')) throw error; }
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    throw new Error(`Timed out: ${description}\n${await this.evaluate('document.body.innerText')}`);
  }
  async text(text) { await this.wait(`document.querySelector('main')?.innerText.includes(${JSON.stringify(text)})`, text); }
  async click(text, selector = 'button') {
    const expression = `[...document.querySelectorAll(${JSON.stringify(selector)})].find(el => el.textContent.trim() === ${JSON.stringify(text)} && !el.disabled)`;
    await this.wait(`Boolean(${expression})`, `enabled ${text}`);
    await this.evaluate(`${expression}.click()`);
  }
  async input(id, value) {
    await this.evaluate(`{ const input = document.getElementById(${JSON.stringify(id)}); input.value = ${JSON.stringify(value)}; input.dispatchEvent(new Event('input', {bubbles:true})); input.dispatchEvent(new Event('change', {bubbles:true})); }`);
  }
  async navigate(path) { await this.send('Page.navigate', {url: origin + path}); }
  async screenshot(name) {
    const {data} = await this.send('Page.captureScreenshot', {format: 'png'});
    await writeFile(`${artifacts}/${name}.png`, Buffer.from(data, 'base64'));
  }
}

const admin = await Page.open();
const participant = await Page.open();
try {
  await admin.send('Network.clearBrowserCookies');
  await admin.send('Network.clearBrowserCache');
  await admin.send('Storage.clearDataForOrigin', {origin, storageTypes: 'all'});
  await admin.send('Emulation.setDeviceMetricsOverride', {width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false});
  await admin.navigate('/admin');
  await admin.text('Welcome back');
  await admin.input('username', 'owner');
  await admin.input('password', password);
  await admin.click('Sign in');
  await admin.text('Runs & activity');
  const seedVersion = await admin.evaluate(`(await (await fetch('/admin/api/versions')).json()).versions.find(v=>v.title.includes('Prometheus')).id`);
  await admin.input('run-version', seedVersion);
  const slug = `browser-pilot-${Date.now().toString(36)}`;
  await admin.input('run-slug', slug);
  await admin.click('Create paused run');
  await admin.text(`Created pilot run “${slug}”`);
  await admin.evaluate(`[...document.querySelectorAll('.list-card')].find(el=>el.textContent.includes(${JSON.stringify(slug)})).querySelector('button.secondary.small').click()`);
  await admin.click('Open enrollment', 'dialog[open] button');
  await admin.wait(`[...document.querySelectorAll('.list-card')].find(el=>el.textContent.includes(${JSON.stringify(slug)})).querySelector('.badge.open')`);

  await participant.send('Emulation.setDeviceMetricsOverride', {width: 390, height: 844, deviceScaleFactor: 1, mobile: true});
  await participant.navigate(`/s/${slug}`);
  await participant.text('Begin study');
  await participant.click('Open practice');
  await participant.text('Try finding a library page');
  await participant.evaluate(`document.querySelector('[aria-label="Explore Visit the library"]').click()`);
  await participant.evaluate(`document.querySelector('[aria-label="Select Opening hours"]').click()`);
  await participant.click('Confirm this page');
  await participant.text('You’ve tried the controls');
  assert.equal(await participant.evaluate(`(await fetch('/api/public/${slug}/session')).status`), 404, 'practice allocated a participant');
  await participant.click('Return to study instructions');
  await participant.click('Begin study');
  await participant.text('Task 1 of 6');
  await participant.click('Start exploring');
  await participant.text('Explore the navigation');
  assert.equal(await participant.evaluate('document.documentElement.scrollWidth <= innerWidth + 1'), true, 'mobile horizontal overflow');
  await participant.screenshot('participant-mobile');

  // Browse with the network interrupted, then restore and reload the same task.
  await participant.send('Network.emulateNetworkConditions', {offline: true, latency: 0, downloadThroughput: -1, uploadThroughput: -1});
  await participant.evaluate(`document.querySelector('[aria-label^="Explore "]').click()`);
  await participant.wait(`Boolean(document.querySelector('[aria-label^="Select "]'))`);
  await participant.evaluate(`document.querySelector('[aria-label^="Select "]').click()`);
  await participant.text('Confirm this page');
  const selection = await participant.evaluate(`document.querySelector('.selection-path').textContent`);
  await participant.text('Saved on this device · not synced');
  await participant.send('Network.emulateNetworkConditions', {offline: false, latency: 0, downloadThroughput: -1, uploadThroughput: -1});
  await participant.send('Page.reload');
  await participant.text('Confirm this page');
  assert.equal(await participant.evaluate(`document.querySelector('.selection-path').textContent`), selection, 'reload lost selected location');

  // The server commits, but the browser loses the response. Reload must replay
  // the same command rather than submit a second answer or jump two tasks.
  await participant.evaluate(`{const original=window.fetch; let lose=true; window.fetch=async (...args)=>{const response=await original(...args); if(lose && String(args[0]).endsWith('/finish')){lose=false; throw new TypeError('simulated lost finish response');} return response;};}`);
  await participant.click('Confirm this page');
  await participant.text('Retry saving response');
  await participant.send('Page.reload');
  await participant.text('Task 2 of 6');
  await participant.click('This task is unclear · skip');
  await participant.click('Skip this task', 'dialog[open] button');
  await participant.text('Task 3 of 6');
  for (let i = 3; i <= 6; i++) {
    await participant.click('Start exploring');
    await participant.text('Explore the navigation');
    await participant.click('I can’t find it');
    await participant.click('Submit “can’t find”', 'dialog[open] button');
    await participant.text(i === 6 ? 'Thank you for finding your way.' : `Task ${i+1} of 6`);
  }

  // Real web authoring/publishing flow uses the valid illustrative starter.
  await admin.click('Drafts');
  await admin.click('New draft');
  await admin.click('Validate bundle');
  await admin.text('Bundle is valid');
  await admin.click('Save draft');
  await admin.text('Saved · revision 1');
  await admin.click('Publish version');
  await admin.click('Publish version', 'dialog[open] button');
  await admin.text('Create a run when ready.');
  await admin.click('Blinded results');
  await admin.input('mode-filter', 'pilot');
  await admin.text('Outcomes by blinded arm');
  const runID = await admin.evaluate(`(await (await fetch('/admin/api/runs')).json()).runs.find(r=>r.slug===${JSON.stringify(slug)}).id`);
  const exported = await admin.evaluate(`await (await fetch('/admin/api/runs/${runID}/export?format=json')).json()`);
  assert.equal(exported.rows.length, 6);
  assert.equal(exported.rows.filter(row => row.outcome === 'skipped').length, 1);
  assert.equal(exported.rows.filter(row => row.outcome === 'gave_up').length, 4);
  assert.ok(exported.rows.every(row => row.session_completed));
  const interrupted = exported.rows.find(row => row.outcome === 'selected');
  assert.equal(interrupted.timing_quality, 'interrupted');
  assert.equal(interrupted.navigation_ms, null);
  assert.ok(interrupted.clock_epochs >= 2);
  await admin.screenshot('admin-results');
  await admin.click('Sign out');
  await admin.text('Welcome back');
  await admin.input('username', 'analyst');
  await admin.input('password', password);
  await admin.click('Sign in');
  await admin.text('Blinded results');
  assert.equal(await admin.evaluate(`document.querySelector('.workspace-nav').textContent.includes('Drafts')`), false);
  assert.equal(await admin.evaluate(`(await fetch('/admin/api/versions')).status`), 403);
  assert.equal(await admin.evaluate(`(await fetch('/admin/api/runs/${runID}/key')).status`), 403);
  assert.deepEqual(admin.errors, [], 'admin JavaScript exceptions');
  assert.deepEqual(participant.errors, [], 'participant JavaScript exceptions');
  console.log(`Browser checks passed: mobile practice/navigation, offline reload, lost receipt, completion, owner publishing, analyst boundaries.\nPilot: ${origin}/s/${slug}\nScreenshots: ${artifacts}`);
} finally {
  admin.socket.close();
  participant.socket.close();
  await fetch(`${debug}/json/close/${admin.targetID}`);
  await fetch(`${debug}/json/close/${participant.targetID}`);
}
