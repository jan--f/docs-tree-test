// A deliberately small DOM adapter exercises the real controllers without a
// browser dependency. Layout, focus visibility, and responsive styling still
// need a real-browser check; durable commands and route access do not.
import test from 'node:test';
import assert from 'node:assert/strict';
const realPerformance = globalThis.performance;

class DOMNode {
  constructor(tag = '', text = '') { this.tagName = tag.toUpperCase(); this.children = []; this.attributes = {}; this.listeners = new Map(); this.parentNode = null; this._text = text; this.disabled = false; this.hidden = false; this.value = ''; }
  set textContent(text) { this.children = []; this._text = String(text); }
  get textContent() { return this._text + this.children.map(child => child.textContent).join(''); }
  get id() { return this.attributes.id || ''; }
  set id(value) { this.attributes.id = value; }
  get isConnected() { return this === globalThis.document?.body || Boolean(this.parentNode?.isConnected); }
  setAttribute(key, value) { this.attributes[key] = String(value); }
  getAttribute(key) { return this.attributes[key] ?? null; }
  removeAttribute(key) { delete this.attributes[key]; }
  append(...children) { for (let child of children) { if (!(child instanceof DOMNode)) child = new DOMNode('', String(child)); child.parentNode = this; this.children.push(child); } }
  replaceChildren(...children) { for (const child of this.children) child.parentNode = null; this.children = []; this._text = ''; this.append(...children); }
  remove() { if (this.parentNode) this.parentNode.children = this.parentNode.children.filter(child => child !== this); this.parentNode = null; }
  addEventListener(type, fn) { this.listeners.set(type, [...(this.listeners.get(type) || []), fn]); }
  async dispatch(type, extra = {}) { for (const listener of this.listeners.get(type) || []) await listener({target: this, currentTarget: this, preventDefault() {}, ...extra}); }
  async click() { if (!this.disabled) await this.dispatch('click'); }
  focus() { globalThis.document.activeElement = this; }
  scrollIntoView() {}
  showModal() { this.open = true; }
  close() { this.open = false; }
  querySelectorAll(selector) {
    const matches = node => selector.split(',').some(part => {
      const value = part.trim();
      return value.startsWith('#') ? node.id === value.slice(1) : node.tagName === value.toUpperCase();
    });
    const found = [];
    function walk(node) { for (const child of node.children) { if (matches(child)) found.push(child); walk(child); } }
    walk(this);
    return found;
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
}

function environment(path, storage = new Map(), clock) {
  const document = new DOMNode('document');
  document.body = new DOMNode('body');
  document.visibilityState = 'visible';
  document.createElement = tag => new DOMNode(tag);
  document.createTextNode = text => new DOMNode('', text);
  document.getElementById = id => document.body.querySelector(`#${id}`);
  for (const id of ['app', 'notice', 'announcer', 'identity']) { const node = new DOMNode('div'); node.id = id; document.body.append(node); }
  const window = new DOMNode('window');
  Object.defineProperty(globalThis, 'performance', {configurable: true, value: clock ? {now: clock} : realPerformance});
  Object.assign(globalThis, {Node: DOMNode, document, window, location: {pathname: path, reload() {}}, localStorage: {getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key)}, requestAnimationFrame: callback => callback()});
  return {document, window, storage};
}

const json = (body, status = 200) => new Response(JSON.stringify(body), {status, headers: {'Content-Type': 'application/json'}});
const waitFor = async predicate => {
  for (let i = 0; i < 80; i++) { if (predicate()) return; await new Promise(resolve => setTimeout(resolve, 5)); }
  throw new Error('UI condition was not reached.');
};
function byText(text) { const node = document.body.querySelectorAll('button').find(button => button.textContent === text); assert.ok(node, `Button not found: ${text}\n${document.body.textContent}`); return node; }
function byLabel(label) { const node = document.body.querySelectorAll('button').find(button => button.getAttribute('aria-label') === label); assert.ok(node, `Button not found: ${label}`); return node; }
const tree = [{id: 'g', label: 'Guides', selectable: false, children: [{id: 'p', label: 'Access', selectable: true, children: [{id: 'leaf', label: 'Invites', selectable: true, children: []}]}]}];
const session = (index = 0, attempt = null) => ({id: 'session', title: 'Study', completed: false, task_index: index, total_tasks: 6, task: {id: `task-${index}`, prompt: `Situation ${index + 1}`}, tree, attempt});
const firstEvent = {id: 'first', seq: 1, type: 'tree_shown', elapsed_ms: 200, epoch: 'old-page', visible: true};
let moduleID = 0;
async function loadParticipant() { await import(`../assets/participant.js?test=${moduleID++}`); }

test('practice does not enroll; lost finish acknowledgment is replayed byte-for-byte after reload', async () => {
  const {storage} = environment('/s/example');
  let current;
  const requests = [];
  const finishes = [];
  globalThis.fetch = async (path, options = {}) => {
    requests.push(path);
    if (path === '/api/public/example') return json({title: 'Study', instructions: 'Try finding a page.', status: 'open', mode: 'real'});
    if (path.endsWith('/session')) return current ? json(current) : json({error: 'Not enrolled'}, 404);
    if (path.endsWith('/join')) { current = session(); return json(current); }
    if (path.endsWith('/start')) { assert.equal(JSON.parse(options.body).task_id, 'task-0'); current = session(0, {id: 'attempt', next_seq: 1, events: [], started_at: '2026-01-01'}); return json(current); }
    if (path.endsWith('/finish')) {
      const saved = JSON.parse(storage.get('tree-study:v1:example'));
      assert.equal(options.body, JSON.stringify(saved.finishCommand), 'command must be durable before request');
      finishes.push(options.body);
      current = session(1);
      if (finishes.length === 1) throw new TypeError('Response lost after server commit');
      return json(current);
    }
    throw new Error(`Unexpected endpoint ${path}`);
  };
  await loadParticipant();
  await waitFor(() => document.body.textContent.includes('Begin study'));
  await byText('Open practice').click();
  await byLabel('Explore Visit the library').click();
  await byLabel('Select Opening hours').click();
  await byText('Confirm this page').click();
  assert.deepEqual(requests, ['/api/public/example', '/api/public/example/session']);
  await byText('Return to study instructions').click();
  const form = document.body.querySelector('form');
  await form.dispatch('submit');
  await waitFor(() => document.body.textContent.includes('Start exploring'));
  await byText('Start exploring').click();
  await byLabel('Explore Guides').click();
  await byLabel('Select Access').click();
  await byText('Confirm this page').click();
  assert.ok(document.body.textContent.includes('Retry saving response'));
  assert.equal(finishes.length, 1);
  const originalEvents = JSON.parse(finishes[0]).events;
  assert.deepEqual(originalEvents.map(event => event.type), ['tree_shown', 'enter', 'select', 'submit']);
  assert.ok(originalEvents.every(event => event.visible === true));
  assert.equal(originalEvents.at(-1).outcome, 'selected');
  assert.equal(originalEvents.at(-1).node_id, 'p');
  assert.ok(!document.body.textContent.includes('Situation 2'), 'must not advance before acknowledgment');
  environment('/s/example', storage);
  await loadParticipant();
  await waitFor(() => document.body.textContent.includes('Situation 2'));
  assert.equal(finishes.length, 2);
  assert.equal(finishes[0], finishes[1]);
  assert.deepEqual(JSON.parse(storage.get('tree-study:v1:example')), {sessionId: 'session', events: []});
});

test('reload combines server history with unsent navigation, then submits the current page', async () => {
  const stored = {sessionId: 'session', attemptId: 'attempt', nextSeq: 4, events: [{id: 'third', seq: 3, type: 'enter', node_id: 'p', elapsed_ms: 600, epoch: 'old-page', visible: true}]};
  const storage = new Map([['tree-study:v1:resume', JSON.stringify(stored)]]);
  environment('/s/resume', storage);
  const serverEvents = [firstEvent, {id: 'second', seq: 2, type: 'enter', node_id: 'g', elapsed_ms: 300, epoch: 'old-page', visible: true}];
  let final;
  globalThis.fetch = async (path, options = {}) => {
    if (path === '/api/public/resume') return json({title: 'Study', status: 'closed', mode: 'real'});
    if (path.endsWith('/session')) return json(session(0, {id: 'attempt', next_seq: 3, events: serverEvents}));
    if (path.endsWith('/finish')) { final = JSON.parse(options.body); return json(session(1)); }
    throw new Error(`Unexpected endpoint ${path}`);
  };
  await loadParticipant();
  await waitFor(() => document.body.textContent.includes('Select this page'));
  assert.equal(document.getElementById('tree-location').textContent, 'Access');
  await byText('Select this page').click();
  await byText('Confirm this page').click();
  assert.equal(final.node_id, 'p');
  assert.deepEqual(final.events.map(event => event.type), ['enter', 'resume', 'select', 'submit']);
  assert.deepEqual(final.events.map(event => event.seq), [3, 4, 5, 6]);
  assert.ok(final.events.every(event => event.visible === true));
  assert.notEqual(final.events[1].epoch, 'old-page');
  assert.equal(final.events[1].epoch, final.events[2].epoch);
  assert.ok(final.events[2].elapsed_ms >= final.events[1].elapsed_ms);
});

test('sequence conflict pauses navigation and does not schedule repeated sends', async () => {
  const {document, storage} = environment('/s/conflict');
  let checkpoints = 0;
  globalThis.fetch = async path => {
    if (path === '/api/public/conflict') return json({title: 'Study', status: 'open', mode: 'real'});
    if (path.endsWith('/session')) return json(session(0, {id: 'attempt', next_seq: 2, events: [firstEvent]}));
    if (path.endsWith('/events')) { checkpoints++; return json({error: 'event sequence conflict; reload this session'}, 409); }
    throw new Error(`Unexpected endpoint ${path}`);
  };
  await loadParticipant();
  await waitFor(() => document.body.textContent.includes('Explore the navigation'));
  document.visibilityState = 'hidden';
  await document.dispatch('visibilitychange');
  await waitFor(() => document.body.textContent.includes('Navigation is paused'));
  assert.equal(byLabel('Explore Guides').disabled, true);
  assert.equal(checkpoints, 1);
  assert.ok(JSON.parse(storage.get('tree-study:v1:conflict')).events.length > 0);
  await document.dispatch('visibilitychange');
  assert.equal(checkpoints, 1);
});

test('comprehension skip survives a lost start response and reuses the start command', async () => {
  const {storage} = environment('/s/skip');
  let current = session();
  const starts = [];
  let final;
  globalThis.fetch = async (path, options = {}) => {
    if (path === '/api/public/skip') return json({title: 'Study', status: 'open', mode: 'real'});
    if (path.endsWith('/session')) return json(current);
    if (path.endsWith('/start')) {
      starts.push(options.body);
      assert.equal(JSON.parse(storage.get('tree-study:v1:skip')).startCommand.command_id, JSON.parse(options.body).command_id);
      current = session(0, {id: 'attempt', events: [], next_seq: 1});
      if (starts.length === 1) throw new TypeError('Lost start response');
      return json(current);
    }
    if (path.endsWith('/finish')) { final = JSON.parse(options.body); current = session(1); return json(current); }
    throw new Error(`Unexpected endpoint ${path}`);
  };
  await loadParticipant();
  await waitFor(() => document.body.textContent.includes('Start exploring'));
  const skip = byText('This task is unclear · skip').click();
  await waitFor(() => document.body.textContent.includes('Skip this situation?'));
  await byText('Skip this task').click();
  await skip;
  assert.equal(starts.length, 1);
  const skipCommand = JSON.parse(storage.get('tree-study:v1:skip')).startCommand;
  assert.equal(skipCommand.skip, true);
  assert.equal(JSON.parse(starts[0]).task_id, 'task-0');
  environment('/s/skip', storage);
  await loadParticipant();
  await waitFor(() => document.body.textContent.includes('Situation 2'));
  assert.equal(starts.length, 2);
  assert.equal(starts[0], starts[1]);
  assert.equal(final.outcome, 'skipped');
  assert.deepEqual(final.events.map(event => event.type), ['submit']);
  assert.equal(final.events[0].seq, 1);
  assert.equal(final.events[0].outcome, 'skipped');
  assert.equal(final.events[0].visible, true);
  assert.equal(final.events[0].id, skipCommand.submission.id);
  assert.equal(final.events[0].elapsed_ms, skipCommand.submission.elapsed_ms);
  assert.equal(final.events[0].epoch, skipCommand.submission.epoch);
  assert.equal(final.node_id, undefined);
});

test('reload waits for the finish receipt even when GET already reports a completed session', async () => {
  const terminal = {id: 'last', seq: 2, type: 'submit', outcome: 'gave_up', elapsed_ms: 800, epoch: 'old-page', visible: true};
  const command = {attempt_id: 'attempt', command_id: 'finish-id', outcome: 'gave_up', events: [firstEvent, terminal]};
  const storage = new Map([['tree-study:v1:complete', JSON.stringify({sessionId: 'session', attemptId: 'attempt', nextSeq: 3, events: [firstEvent, terminal], finishCommand: command})]]);
  environment('/s/complete', storage);
  let acknowledge;
  const complete = {id: 'session', title: 'Study', completed: true, task_index: 6, total_tasks: 6};
  globalThis.fetch = async (path, options = {}) => {
    if (path === '/api/public/complete') return json({title: 'Study', status: 'closed', mode: 'real'});
    if (path.endsWith('/session')) return json(complete);
    if (path.endsWith('/finish')) { assert.deepEqual(JSON.parse(options.body), command); return new Promise(resolve => { acknowledge = () => resolve(json(complete)); }); }
    throw new Error(`Unexpected endpoint ${path}`);
  };
  await loadParticipant();
  await waitFor(() => acknowledge);
  assert.ok(document.body.textContent.includes('Saving your response'));
  assert.ok(!document.body.textContent.includes('Thank you for finding your way'));
  acknowledge();
  await waitFor(() => document.body.textContent.includes('Thank you for finding your way'));
  assert.equal(JSON.parse(storage.get('tree-study:v1:complete')).finishCommand, undefined);
});

test('confirmation freezes terminal timing before an in-flight checkpoint and retries the identical batch', async () => {
  let clock = 1000;
  const {document, storage} = environment('/s/timing', new Map(), () => clock);
  let acknowledgeCheckpoint;
  let checkpoint;
  const finishes = [];
  globalThis.fetch = async (path, options = {}) => {
    if (path === '/api/public/timing') return json({title: 'Study', status: 'open', mode: 'real'});
    if (path.endsWith('/session')) return json(session(0, {id: 'attempt', next_seq: 1, events: []}));
    if (path.endsWith('/events')) {
      checkpoint = JSON.parse(options.body);
      return new Promise(resolve => { acknowledgeCheckpoint = () => resolve(json({next_seq: checkpoint.events.at(-1).seq + 1})); });
    }
    if (path.endsWith('/finish')) {
      finishes.push(options.body);
      if (finishes.length === 1) throw new TypeError('Lost finish response');
      return json(session(1));
    }
    throw new Error(`Unexpected endpoint ${path}`);
  };
  await loadParticipant();
  await waitFor(() => document.body.textContent.includes('Explore the navigation'));
  clock = 1010;
  await byLabel('Explore Guides').click();
  clock = 1020;
  await byLabel('Select Access').click();
  clock = 1040;
  document.visibilityState = 'hidden';
  await document.dispatch('visibilitychange');
  assert.ok(acknowledgeCheckpoint);
  clock = 1060;
  document.visibilityState = 'visible';
  await document.dispatch('visibilitychange');
  clock = 1100;
  const confirm = byText('Confirm this page').click();
  const frozen = JSON.parse(storage.get('tree-study:v1:timing')).finishCommand;
  assert.ok(frozen, 'finish command must be durable while the checkpoint is still pending');
  assert.equal(finishes.length, 0, 'wait for the existing checkpoint after freezing the command');
  assert.deepEqual(frozen.events.map(event => event.type), ['tree_shown', 'enter', 'select', 'visibility_hidden', 'visibility_visible', 'submit']);
  assert.equal(frozen.events.at(-1).elapsed_ms, 100);
  assert.equal(frozen.events.at(-1).visible, true);
  assert.equal(frozen.events.at(-1).outcome, 'selected');
  assert.equal(frozen.events.at(-1).node_id, 'p');
  assert.equal(frozen.events[3].visible, false);
  assert.equal(checkpoint.events.some(event => event.type === 'submit'), false);
  clock = 9000;
  acknowledgeCheckpoint();
  await confirm;
  assert.equal(finishes[0], JSON.stringify(frozen));
  const liveOutbox = JSON.parse(storage.get('tree-study:v1:timing')).events;
  assert.deepEqual(liveOutbox.map(event => event.seq), [5, 6]);
  clock = 20000;
  await byText('Retry saving response').click();
  assert.equal(finishes[1], finishes[0]);
  assert.equal(JSON.parse(finishes[1]).events.at(-1).elapsed_ms, 100);
  assert.deepEqual(JSON.parse(storage.get('tree-study:v1:timing')), {sessionId: 'session', events: []});
});

test('initial tree and resume events explicitly carry hidden-page visibility', async () => {
  for (const restoring of [false, true]) {
    const slug = restoring ? 'hidden-resume' : 'hidden-first';
    const {document} = environment(`/s/${slug}`);
    document.visibilityState = 'hidden';
    let final;
    globalThis.fetch = async (path, options = {}) => {
      if (path === `/api/public/${slug}`) return json({title: 'Study', status: 'open', mode: 'real'});
      if (path.endsWith('/session')) return json(session(0, {id: 'attempt', next_seq: restoring ? 2 : 1, events: restoring ? [firstEvent] : []}));
      if (path.endsWith('/finish')) { final = JSON.parse(options.body); return json(session(1)); }
      throw new Error(`Unexpected endpoint ${path}`);
    };
    await loadParticipant();
    await waitFor(() => document.body.textContent.includes('Explore the navigation'));
    const giveUp = byText('I can’t find it').click();
    await waitFor(() => document.body.textContent.includes('Can’t find a suitable page?'));
    await byText('Submit “can’t find”').click();
    await giveUp;
    assert.deepEqual(final.events.map(event => event.type), [restoring ? 'resume' : 'tree_shown', 'submit']);
    assert.ok(final.events.every(event => event.visible === false));
    assert.equal(final.events.at(-1).outcome, 'gave_up');
    assert.equal(final.events.at(-1).node_id, undefined);
  }
});

test('a stale displayed task sends its original public ID and pauses on 409', async () => {
  const {storage} = environment('/s/stale');
  const starts = [];
  globalThis.fetch = async (path, options = {}) => {
    if (path === '/api/public/stale') return json({title: 'Study', status: 'open', mode: 'real'});
    if (path.endsWith('/session')) return json(session());
    if (path.endsWith('/start')) { starts.push(JSON.parse(options.body)); return json({error: 'Task changed; reload this session'}, 409); }
    throw new Error(`Unexpected endpoint ${path}`);
  };
  await loadParticipant();
  await waitFor(() => document.body.textContent.includes('Start exploring'));
  await byText('Start exploring').click();
  assert.equal(starts.length, 1);
  assert.equal(starts[0].task_id, 'task-0');
  assert.deepEqual(starts[0], {command_id: JSON.parse(storage.get('tree-study:v1:stale')).startCommand.command_id, task_id: 'task-0'});
  assert.ok(document.body.textContent.includes('Navigation is paused'));
  assert.ok(!document.body.textContent.includes('Situation 2'));
  assert.equal(byText('Retry opening task').disabled, true);
});

test('analyst workspace never requests owner-only endpoints', async () => {
  environment('/admin');
  const paths = [];
  globalThis.fetch = async path => {
    paths.push(path);
    if (path === '/admin/api/session') return json({authenticated: true, username: 'analyst', role: 'analyst', csrf_token: 'token'});
    if (path === '/admin/api/runs') return json({runs: []});
    throw new Error(`Analyst requested forbidden endpoint: ${path}`);
  };
  await import(`../assets/admin.js?test=${moduleID++}`);
  await waitFor(() => document.body.textContent.includes('Blinded results'));
  await byText('Runs & activity').click();
  assert.deepEqual(paths, ['/admin/api/session', '/admin/api/runs']);
  assert.equal(document.body.querySelectorAll('button').some(button => ['Drafts', 'Published versions'].includes(button.textContent)), false);
});

test('owner edits use CSRF, server validation and optimistic revisions; a stale save keeps local content', async () => {
  environment('/admin');
  let draft;
  const writes = [];
  globalThis.fetch = async (path, options = {}) => {
    if (options.method !== 'GET') {
      assert.equal(options.headers['X-CSRF-Token'], 'owner-csrf');
      writes.push({path, method: options.method, body: JSON.parse(options.body)});
    }
    if (path === '/admin/api/session') return json({authenticated: true, username: 'owner', role: 'owner', csrf_token: 'owner-csrf'});
    if (path === '/admin/api/runs') return json({runs: []});
    if (path === '/admin/api/versions') return json({versions: []});
    if (path === '/admin/api/drafts' && options.method === 'GET') return json({drafts: draft ? [{id: 'draft', revision: 1, title: draft.config.title}] : []});
    if (path === '/admin/api/validate') return json({hash: 'hash', node_counts: {current: 8, proposed: 6}, task_count: 6, panel_count: 1});
    if (path === '/admin/api/drafts') { draft = JSON.parse(options.body); return json({id: 'draft', revision: 1}); }
    if (path === '/admin/api/drafts/draft' && options.method === 'PUT') return json({error: 'stale revision'}, 409);
    throw new Error(`Unexpected endpoint ${path}`);
  };
  await import(`../assets/admin.js?test=${moduleID++}`);
  await waitFor(() => document.body.textContent.includes('Runs & activity'));
  await byText('Drafts').click();
  await byText('New draft').click();
  assert.ok(document.getElementById('bundle-preview').textContent.includes('Install the application'));
  assert.equal(document.getElementById('publish-draft').disabled, true);
  await byText('Validate bundle').click();
  assert.ok(document.getElementById('validation-status').textContent.includes('Bundle is valid'));
  await byText('Save draft').click();
  assert.ok(draft.trees['current.md'].includes('(group:start)'));
  assert.equal(document.getElementById('publish-draft').disabled, false);
  const textarea = document.getElementById('study-config');
  const edited = JSON.parse(textarea.value);
  edited.title = 'My unsaved revision';
  textarea.value = JSON.stringify(edited);
  await textarea.dispatch('input');
  assert.equal(document.getElementById('publish-draft').disabled, true);
  await byText('Save draft').click();
  assert.equal(JSON.parse(document.getElementById('study-config').value).title, 'My unsaved revision');
  assert.ok(document.getElementById('notice').textContent.includes('updated elsewhere'));
  assert.equal(writes.at(-1).body.revision, 1);
  assert.equal(writes.at(-1).body.bundle.config.title, 'My unsaved revision');
  assert.equal(document.getElementById('publish-draft').disabled, true);
});

test('whole-study file import requires complete files and confirmation, then replaces starter trees', async () => {
  environment('/admin');
  let validated;
  globalThis.fetch = async (path, options = {}) => {
    if (path === '/admin/api/session') return json({authenticated: true, username: 'owner', role: 'owner', csrf_token: 'csrf'});
    if (path === '/admin/api/runs') return json({runs: []});
    if (path === '/admin/api/drafts') return json({drafts: []});
    if (path === '/admin/api/versions') return json({versions: []});
    if (path === '/admin/api/validate') {
      validated = JSON.parse(options.body);
      return json({hash: 'validated', node_counts: {current: 8, candidate: 8}, task_count: 6, panel_count: 1});
    }
    throw new Error(`Unexpected endpoint ${path}`);
  };
  await import(`../assets/admin.js?test=${moduleID++}`);
  await waitFor(() => document.body.textContent.includes('Runs & activity'));
  await byText('Drafts').click();
  await byText('New draft').click();
  const originalConfig = document.getElementById('study-config').value;
  const config = JSON.parse(originalConfig);
  config.title = 'Imported study';
  config.variants[1] = {id: 'candidate', name: 'Candidate navigation', tree: 'candidate.md'};
  for (const task of config.tasks) { task.answers.candidate = task.answers.proposed; delete task.answers.proposed; }
  const markdown = document.getElementById('tree-source').value;
  const configFile = new File([JSON.stringify(config)], 'study.json', {type: 'application/json'});
  const currentFile = new File([markdown], 'current.md');
  const candidateFile = new File([markdown], 'candidate.md');
  const input = document.getElementById('study-files-upload');
  input.files = [configFile, currentFile];
  await input.dispatch('change');
  assert.ok(document.getElementById('notice').textContent.includes('Missing: candidate.md'));
  assert.equal(document.getElementById('study-config').value, originalConfig);
  input.files = [configFile, currentFile, candidateFile];
  const cancelled = input.dispatch('change');
  await waitFor(() => document.body.textContent.includes('Replace the complete study bundle?'));
  await byText('Cancel').click();
  await cancelled;
  assert.equal(document.getElementById('study-config').value, originalConfig);
  const confirmed = input.dispatch('change');
  await waitFor(() => document.body.textContent.includes('Replace the complete study bundle?'));
  await byText('Import study files').click();
  await confirmed;
  assert.equal(JSON.parse(document.getElementById('study-config').value).title, 'Imported study');
  assert.ok(document.getElementById('file-editor').textContent.includes('candidate.md'));
  assert.ok(!document.getElementById('file-editor').textContent.includes('proposed.md'));
  await byText('Validate bundle').click();
  assert.deepEqual(Object.keys(validated.trees).sort(), ['candidate.md', 'current.md']);
  assert.equal(validated.trees['candidate.md'], markdown);
  assert.equal(validated.config.title, 'Imported study');
});
