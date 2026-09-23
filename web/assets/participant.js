import {el, button, mount, api, announce, notice, clearNotice, focusHeading, field, select, confirmDialog} from './common.js';
import {uuid, SequenceConflict, reconcileEvents, indexTree, replayNavigation, ackEvents} from './core.mjs';

const app = document.getElementById('app');
let slug;
try { slug = decodeURIComponent(location.pathname.split('/')[2] || ''); } catch { slug = ''; }
const base = `/api/public/${encodeURIComponent(slug)}`;
const storageKey = `tree-study:v1:${slug}`;
const epoch = uuid();
const epochStart = performance.now();
let lastElapsed = 0;
let study;
let session;
let saved = {};
let nodes = new Map();
let currentNodeId = null;
let selectedId = null;
let busy = false;
let blocked = false;
let active = false;
let flushPromise = null;
let flushTimer;
let saveMessage = 'Progress saved';
let experience = '';
let familiarity = '';

function persist() {
  try { localStorage.setItem(storageKey, JSON.stringify(saved)); }
  catch {
    blocked = true;
    active = false;
    throw new Error('Your browser could not save study progress. Make storage space available and reload this page before continuing.');
  }
}

function showError(error, retry) {
  if (error instanceof SequenceConflict || error.status === 409) {
    blocked = true;
    active = false;
    notice(`${error.message} Navigation is paused to protect your saved progress.`, 'error', [button('Reload server progress', async () => {
      if (await confirmDialog({title: 'Reload saved progress?', message: 'This reloads the server’s latest progress. Any conflicting actions that have not been accepted by the server will be discarded.', confirm: 'Reload progress'})) {
        localStorage.removeItem(storageKey);
        location.reload();
      }
    })]);
    render();
    return;
  }
  notice(error.message, 'error', retry ? [button('Try again', retry)] : []);
}

function guarded(action) {
  return async (...args) => {
    if (busy || blocked) return;
    try { await action(...args); } catch (error) { showError(error); render(); }
  };
}

function setSaveMessage(text) {
  saveMessage = text;
  const target = document.getElementById('save-status');
  if (target) target.textContent = text;
}

function eventStamp() {
  lastElapsed = Math.max(lastElapsed, Math.round(performance.now() - epochStart));
  return {id: uuid(), elapsed_ms: lastElapsed, epoch, visible: document.visibilityState !== 'hidden'};
}

function makeEvent(type, nodeId, outcome, stamp = eventStamp()) {
  const event = {...stamp, seq: saved.nextSeq, type};
  if (nodeId) event.node_id = nodeId;
  if (outcome) event.outcome = outcome;
  return event;
}

function record(type, nodeId) {
  if (!active || blocked || saved.finishCommand) return;
  const event = makeEvent(type, nodeId);
  saved.events.push(event);
  saved.nextSeq += 1;
  try { persist(); } catch (error) { blocked = true; active = false; throw error; }
  setSaveMessage('Saved on this device');
  clearTimeout(flushTimer);
  flushTimer = setTimeout(() => { flush().catch(error => showError(error, retrySync)); }, 650);
}

async function flush(keepalive = false) {
  if (flushPromise) return flushPromise;
  if (!active || blocked || saved.finishCommand || !saved.events?.length) return;
  const attemptID = saved.attemptId;
  // Terminal events are committed only by /finish, never by a checkpoint.
  const batch = saved.events.filter(event => event.type !== 'submit').map(event => ({...event}));
  if (!batch.length) return;
  setSaveMessage('Saving…');
  flushPromise = (async () => {
    try {
      const response = await api(`${base}/events`, {method: 'POST', body: {attempt_id: attemptID, events: batch}, keepalive});
      if (saved.attemptId !== attemptID) throw new SequenceConflict();
      // Only this snapshot was sent; a higher acknowledgment is another writer.
      ackEvents(batch, response.next_seq);
      saved.events = ackEvents(saved.events, response.next_seq);
      persist();
      setSaveMessage(saved.events.length ? 'Saved on this device' : 'Progress saved');
    } catch (error) {
      setSaveMessage('Saved on this device · not synced');
      throw error;
    } finally { flushPromise = null; }
  })();
  return flushPromise;
}

async function retrySync() {
  clearNotice();
  try { await flush(); announce('Your progress is saved.'); } catch (error) { showError(error, retrySync); }
}

function acceptSession(value, {resuming = false, skipTree = false} = {}) {
  session = value;
  nodes = indexTree(session.tree || []);
  currentNodeId = null;
  selectedId = null;
  active = false;
  if (!session.attempt || session.completed) {
    saved = {sessionId: session.id, events: []};
    persist();
    return;
  }
  if (saved.attemptId && saved.attemptId !== session.attempt.id && saved.events?.length) throw new SequenceConflict();
  const pending = saved.attemptId === session.attempt.id ? saved.events || [] : [];
  const restored = reconcileEvents(session.attempt.events || [], pending, session.attempt.next_seq);
  const startingSkip = skipTree ? saved.startCommand : null;
  saved = {sessionId: session.id, attemptId: session.attempt.id, nextSeq: restored.nextSeq, events: restored.pending};
  // Keep the prompt-skip intent durable until its terminal command is saved.
  if (startingSkip) saved.startCommand = startingSkip;
  ({currentNodeId, selectedId} = replayNavigation(restored.events, nodes));
  persist();
  active = !skipTree;
  if (skipTree) return;
  if (!restored.events.some(event => event.type === 'tree_shown')) record('tree_shown');
  else if (resuming) record('resume', currentNodeId);
}

async function join() {
  if (busy || blocked) return;
  busy = true;
  clearNotice();
  renderLanding();
  try {
    const value = await api(`${base}/join`, {method: 'POST', body: {experience, docs_familiarity: familiarity}});
    acceptSession(value, {resuming: true});
    busy = false;
    render();
    focusHeading();
  } catch (error) {
    busy = false;
    if (error.status === 409) {
      study.status = 'paused';
      renderLanding();
      notice(error.message);
    } else { renderLanding(); showError(error, join); }
  }
}

async function start(skip = false) {
  if (blocked || busy) return;
  busy = true;
  clearNotice();
  try {
    if (!saved.startCommand) {
      saved.startCommand = {command_id: uuid(), task_id: session.task.id, skip};
      // A prompt skip is confirmed before /start can provide an attempt ID.
      // Preserve that click's clock, visibility and event identity across retries.
      if (skip) saved.startCommand.submission = eventStamp();
      persist();
    }
    render();
    const command = {...saved.startCommand};
    const value = await api(`${base}/start`, {method: 'POST', body: {command_id: command.command_id, task_id: command.task_id}});
    acceptSession(value, {resuming: true, skipTree: command.skip});
    busy = false;
    if (command.skip) await finish('skipped', undefined, command.submission);
    else { render(); focusHeading(); }
  } catch (error) { busy = false; render(); showError(error, () => start(skip)); }
}

async function finish(outcome, nodeId, submission) {
  if (blocked || busy) return;
  const stamp = saved.finishCommand ? null : submission || eventStamp();
  busy = true;
  clearTimeout(flushTimer);
  clearNotice();
  try {
    if (!saved.finishCommand) {
      const terminal = makeEvent('submit', outcome === 'selected' ? nodeId : undefined, outcome, stamp);
      saved.events.push(terminal);
      saved.nextSeq += 1;
      const command = {attempt_id: session.attempt.id, command_id: uuid(), outcome, events: saved.events.map(event => ({...event}))};
      if (outcome === 'selected') command.node_id = nodeId;
      saved.finishCommand = command;
      delete saved.startCommand;
      // The terminal event and immutable command are one durable update. This
      // happens at confirmation, before waiting for any in-flight checkpoint.
      persist();
    }
    active = false;
    render();
    // A checkpoint acknowledgment may prune the live outbox, but must never
    // change the frozen command. Its already-sent prefix is safe to duplicate
    // atomically in /finish, including after a lost checkpoint response.
    if (flushPromise) {
      try { await flushPromise; } catch (error) { if (error.status === 409 || error instanceof SequenceConflict) throw error; }
    }
    if (blocked) throw new SequenceConflict();
    const value = await api(`${base}/finish`, {method: 'POST', body: saved.finishCommand});
    // Acknowledgment is the only point at which the task and command advance.
    saved = {sessionId: value.id, events: []};
    persist();
    acceptSession(value);
    busy = false;
    clearNotice();
    render();
    focusHeading();
    announce(value.completed ? 'Study complete. Thank you.' : `Response saved. Task ${value.task_index + 1} of ${value.total_tasks}.`);
  } catch (error) {
    busy = false;
    render();
    showError(error, () => finish(outcome, nodeId));
  }
}

async function requestSkip() {
  if (await confirmDialog({title: 'Skip this situation?', message: 'If the task’s wording or situation is unclear, you can skip it. If you understand the task but cannot find a page, choose “I can’t find it” after starting.', confirm: 'Skip this task'})) {
    if (session.attempt) await finish('skipped'); else await start(true);
  }
}

async function requestGiveUp() {
  if (await confirmDialog({title: 'Can’t find a suitable page?', message: 'That is a useful response too. Submit this task without choosing a page and continue.', confirm: 'Submit “can’t find”'})) await finish('gave_up');
}

function progress() {
  const current = session.completed ? session.total_tasks : session.task_index;
  return el('div', {}, el('div', {class: 'progress-heading'}, el('span', {}, session.completed ? 'Study complete' : `Task ${session.task_index + 1} of ${session.total_tasks}`), el('span', {}, `${current} completed`)), el('div', {class: 'progress-track', role: 'progressbar', 'aria-label': 'Tasks completed', 'aria-valuemin': 0, 'aria-valuemax': session.total_tasks, 'aria-valuenow': current}, el('div', {class: 'progress-fill', style: `width:${session.total_tasks ? current / session.total_tasks * 100 : 0}%`} )));
}

function renderLanding() {
  app.setAttribute('aria-busy', String(busy));
  const available = study.status === 'open';
  mount(app,
    el('p', {class: 'eyebrow'}, 'Welcome to the study'),
    el('h1', {}, study.title),
    el('p', {class: 'lede'}, 'Help us understand where you would look for information. We’re studying the navigation, not testing your knowledge.'),
    study.instructions && el('div', {class: 'card instructions'}, study.instructions),
    el('h2', {}, 'What you’ll do'),
    el('ol', {class: 'steps'},
      el('li', {}, 'Read a series of short situations. Start each one when you’re ready to browse.'),
      el('li', {}, 'Explore the groups and page titles. You can go back or return to the top at any time.'),
      el('li', {}, 'Choose the page where you would expect to find the answer, then confirm your choice. You can also say you can’t find it or skip an unclear task.')),
    el('div', {class: 'inline-note'}, el('span', {class: 'note-icon', 'aria-hidden': 'true'}, '↳'), el('div', {}, el('h2', {}, 'Try the controls first'), el('p', {}, 'A short, unrelated library example. Practice stays in your browser and is not part of your study responses.'), button('Open practice', () => renderPractice(), 'secondary', {disabled: busy}))),
    available ? el('form', {onsubmit: event => { event.preventDefault(); if (!busy) join(); }},
      el('div', {class: 'card'}, el('h2', {}, 'A little context · optional'), el('p', {class: 'help'}, 'You can leave either question unanswered.'),
        el('div', {class: 'field-grid'}, field('Your experience with this subject', select('experience', [['', 'Prefer not to answer'], ['new', 'I’m new to it'], ['some', 'Some experience'], ['regular', 'I use it regularly'], ['extensive', 'Extensive experience']], experience, event => { experience = event.target.value; })),
          field('Familiarity with these docs', select('familiarity', [['', 'Prefer not to answer'], ['never', 'I haven’t used them'], ['occasionally', 'I use them occasionally'], ['regularly', 'I use them regularly']], familiarity, event => { familiarity = event.target.value; })))),
      el('button', {type: 'submit', disabled: busy || blocked}, busy ? 'Opening your tasks…' : 'Begin study'), el('p', {class: 'help'}, 'Your progress is saved in this browser. Return using this same study link.'))
      : el('div', {class: 'message warning'}, el('h2', {}, 'This study is not accepting new participants'), el('p', {}, 'If you already started, use the same browser and study link to resume. Otherwise, check with the person who shared your link.'))
  );
}

function render() {
  app.setAttribute('aria-busy', String(busy));
  if (!session) { if (study) renderLanding(); return; }
  // A GET on reload can already reflect a committed command whose response
  // was lost. Do not reveal that later task or completion until replay is acked.
  if (saved.finishCommand) {
    app.replaceChildren(el('p', {class: 'eyebrow'}, 'Your response'), el('h1', {}, busy ? 'Saving your response…' : 'Your response is ready to send'), el('div', {class: 'card'}, el('p', {}, 'This task will move forward once the server confirms it is saved. Retrying sends the same response safely.'), button(busy ? 'Saving…' : 'Retry saving response', () => finish(saved.finishCommand.outcome, saved.finishCommand.node_id), 'primary', {disabled: busy || blocked})));
    return;
  }
  if (session.completed) {
    app.replaceChildren(progress(), el('section', {class: 'completion'}, el('div', {class: 'completion-symbol', 'aria-hidden': 'true'}, '✓'), el('p', {class: 'eyebrow'}, 'All done'), el('h1', {}, 'Thank you for finding your way.'), el('p', {class: 'lede'}, 'Your responses have been saved. They’ll help us understand how to make information easier to find.'), el('p', {class: 'muted'}, 'You can close this page now.')));
    return;
  }
  const prompt = el('section', {class: 'task-card'}, el('p', {class: 'eyebrow'}, session.attempt ? 'Your task' : 'Read the situation'), el('h1', {class: 'task-prompt'}, session.task.prompt));
  if (!session.attempt) {
    app.replaceChildren(progress(), prompt, el('div', {class: 'card'}, el('h2', {}, 'Where would you look?'), el('p', {class: 'muted'}, 'When you’re ready, open the navigation tree and choose the page where you would expect to find this information.'), el('div', {class: 'actions'}, button(busy ? 'Opening tree…' : saved.startCommand ? 'Retry opening task' : 'Start exploring', () => start(), 'primary', {disabled: busy || blocked}), button('This task is unclear · skip', guarded(requestSkip), 'quiet', {disabled: busy || blocked || Boolean(saved.startCommand)}))), el('p', {class: 'help'}, 'There is no need to rush. Browse in the way that feels natural to you.'));
    return;
  }
  const tree = treeView({tree: session.tree, nodes, current: currentNodeId, selected: selectedId, disabled: busy || blocked,
    navigate: guarded((id, type) => { record(type, id); currentNodeId = id; selectedId = null; render(); document.getElementById('tree-location')?.focus(); announce(id ? `Opened ${nodes.get(id).node.label}` : 'At the top of the tree'); }),
    choose: guarded(id => { record('select', id); selectedId = id; render(); document.getElementById('confirm-selection')?.focus(); announce(`Selected ${nodes.get(id).node.label}. Confirm your choice to submit.`); }),
  });
  mount(app, progress(), prompt, tree,
    selectedId && selectionPanel(nodes, selectedId, () => finish('selected', selectedId), busy || blocked),
    el('div', {class: 'task-exits'}, button('I can’t find it', guarded(requestGiveUp), 'quiet', {disabled: busy || blocked}), button('This task is unclear · skip', guarded(requestSkip), 'quiet', {disabled: busy || blocked})),
    el('p', {class: 'help'}, 'Groups contain more choices. Pages can be selected; some pages also contain other pages.'));
}

function selectionPanel(nodeMap, id, submit, disabled = false) {
  const entry = nodeMap.get(id);
  const path = [...entry.parents, id].map(key => nodeMap.get(key).node.label).join(' / ');
  return el('section', {class: 'selection-card', 'aria-label': 'Confirm your selected page'}, el('p', {}, 'Your selected page'), el('p', {class: 'selection-path'}, path), el('div', {class: 'actions'}, button('Confirm this page', submit, 'primary', {id: 'confirm-selection', disabled}), el('span', {class: 'help'}, 'Or keep browsing to choose another page.')));
}

function treeView({tree, nodes: nodeMap, current, selected, disabled, navigate, choose, practice = false}) {
  const entry = current ? nodeMap.get(current) : null;
  const children = entry ? entry.node.children || [] : tree;
  const parents = entry ? entry.parents : [];
  const crumbs = [...parents, ...(current ? [current] : [])];
  const navigateTo = (id, type) => async () => {
    if (disabled) return;
    try { await navigate(id, type); } catch (error) { showError(error); render(); }
  };
  const chooseNode = id => async () => {
    if (disabled) return;
    try { await choose(id); } catch (error) { showError(error); render(); }
  };
  return el('section', {class: 'tree-panel', 'aria-label': practice ? 'Practice navigation' : 'Documentation navigation'},
    el('div', {class: 'tree-toolbar'}, button('← Back', navigateTo(parents.at(-1) || null, 'back'), 'secondary small', {disabled: disabled || !current}), button('↟ Top level', navigateTo(null, 'root'), 'quiet small', {disabled: disabled || !current}), !practice && el('span', {class: 'save-status', id: 'save-status', role: 'status'}, saveMessage)),
    el('nav', {class: 'breadcrumbs', 'aria-label': 'Your location'}, el('ol', {}, el('li', {}, current ? button('Top level', navigateTo(null, 'root'), 'quiet', {disabled}) : el('span', {'aria-current': 'location'}, 'Top level')), crumbs.map(id => el('li', {}, id === current ? el('span', {'aria-current': 'location'}, nodeMap.get(id).node.label) : button(nodeMap.get(id).node.label, navigateTo(id, 'back'), 'quiet', {disabled}))))),
    el('div', {class: 'tree-location'}, el('h2', {id: 'tree-location', tabindex: '-1'}, entry ? entry.node.label : 'Explore the navigation'), !children.length && el('p', {class: 'help'}, 'This page has no further items. Select it here, or go back to keep exploring.')),
    entry?.node.selectable && el('div', {class: 'current-page'}, el('div', {}, el('p', {}, 'This is also a selectable page.'), el('span', {class: 'help'}, entry.node.label)), button(selected === current ? 'Selected' : 'Select this page', chooseNode(current), 'secondary', {disabled, 'aria-pressed': selected === current ? 'true' : 'false'})),
    el('ul', {class: 'tree-list'}, children.map(node => {
      const hasChildren = Boolean(node.children?.length);
      return el('li', {}, el('div', {class: `tree-row${selected === node.id ? ' is-selected' : ''}`}, el('span', {class: 'node-icon', 'aria-hidden': 'true'}, node.selectable ? '▤' : '▱'), el('div', {class: 'node-info'}, el('span', {class: 'node-label'}, node.label), el('span', {class: 'node-meta'}, node.selectable ? hasChildren ? 'Page · contains more items' : 'Page' : 'Group')),
        el('div', {class: 'node-actions'}, hasChildren && button('Explore →', navigateTo(node.id, 'enter'), 'quiet', {disabled, 'aria-label': `Explore ${node.label}`}), node.selectable && button(selected === node.id ? 'Selected' : 'Select', chooseNode(node.id), 'secondary', {disabled, 'aria-label': `Select ${node.label}`, 'aria-pressed': selected === node.id ? 'true' : 'false'}), !hasChildren && !node.selectable && el('span', {class: 'help'}, 'Empty group'))));
    })));
}

function renderPractice() {
  clearNotice();
  const tree = [
    {id: 'visit', label: 'Visit the library', selectable: false, children: [{id: 'hours', label: 'Opening hours', selectable: true, children: []}, {id: 'travel', label: 'Getting here', selectable: true, children: [{id: 'parking', label: 'Bicycle parking', selectable: true, children: []}]}]},
    {id: 'borrow', label: 'Borrow and return', selectable: false, children: [{id: 'card', label: 'Get a library card', selectable: true, children: []}, {id: 'renew', label: 'Renew a book', selectable: true, children: []}]},
  ];
  const practiceNodes = indexTree(tree);
  let current = null;
  let selected = null;
  function paint() {
    mount(app, el('div', {class: 'practice-banner'}, 'Practice only · Nothing in this activity is saved or submitted.'), el('p', {class: 'eyebrow'}, 'Get comfortable with the controls'), el('h1', {}, 'Try finding a library page'), el('p', {class: 'lede'}, 'Imagine you want to check when the library is open. Explore a group, select a page, and confirm your choice.'), treeView({tree, nodes: practiceNodes, current, selected, practice: true, disabled: false,
      navigate: (id) => { current = id; selected = null; paint(); document.getElementById('tree-location').focus(); },
      choose: id => { selected = id; paint(); document.getElementById('confirm-selection').focus(); },
    }), selected && selectionPanel(practiceNodes, selected, () => {
      app.replaceChildren(el('div', {class: 'practice-banner'}, 'Practice only · No response was saved.'), el('h1', {}, 'You’ve tried the controls'), el('p', {class: 'lede'}, 'In the study, you’ll use these same controls to choose where you would look. You can explore freely before confirming each choice.'), el('div', {class: 'actions'}, button('Return to study instructions', () => { renderLanding(); focusHeading(); }, 'primary'), button('Practice again', renderPractice)));
      focusHeading();
    }), el('div', {class: 'task-exits'}, button('Return to study instructions', () => { renderLanding(); focusHeading(); }, 'quiet')));
  }
  paint();
  focusHeading();
}

async function init() {
  try {
    if (!slug) throw new Error('This study link is incomplete. Open the full link shared by the research team.');
    try {
      saved = JSON.parse(localStorage.getItem(storageKey) || '{}');
      if (!saved || typeof saved !== 'object' || Array.isArray(saved) || (saved.events && !Array.isArray(saved.events))) throw new Error('Invalid saved state');
    }
    catch { throw new Error('Saved progress could not be read. Please contact the study organizer before clearing this browser’s storage.'); }
    persist(); // Check durable storage before allowing enrollment or navigation.
    study = await api(base);
    document.title = `${study.title} · Tree study`;
    let value;
    try { value = await api(`${base}/session`); } catch (error) { if (error.status !== 404) throw error; }
    if (!value) {
      if (saved.finishCommand || saved.startCommand || saved.events?.length) throw new Error('Saved responses exist, but this browser’s study session could not be found. Restore the browser’s original cookies or contact the study organizer.');
      saved = {};
      renderLanding();
    } else {
      session = value;
      if (saved.sessionId && saved.sessionId !== value.id) throw new SequenceConflict();
      if (saved.finishCommand) await finish(saved.finishCommand.outcome, saved.finishCommand.node_id);
      else if (saved.startCommand) await start(saved.startCommand.skip);
      else { acceptSession(value, {resuming: true}); render(); }
    }
  } catch (error) {
    app.setAttribute('aria-busy', 'false');
    if (!session) app.replaceChildren(el('h1', {}, 'We couldn’t open your study'), el('p', {class: 'muted'}, 'Your saved progress has been kept.'));
    showError(error, () => location.reload());
  }
}

document.addEventListener('visibilitychange', () => {
  if (!active || busy || blocked || saved.finishCommand) return;
  try {
    record(document.visibilityState === 'hidden' ? 'visibility_hidden' : 'visibility_visible', currentNodeId);
    if (document.visibilityState === 'hidden') {
      clearTimeout(flushTimer);
      flush(true).catch(error => showError(error, retrySync));
    }
  } catch (error) { showError(error); render(); }
});
window.addEventListener('pageshow', event => {
  // A bfcache document may precede a newer page epoch or another writer.
  // A fresh load reconciles its outbox with the authoritative event history.
  if (event.persisted) location.reload();
});
window.addEventListener('storage', event => {
  if (event.key === storageKey && session && !session.completed && event.newValue !== JSON.stringify(saved)) showError(new SequenceConflict());
});
window.addEventListener('online', () => { if (active && !blocked && !saved.finishCommand) retrySync(); });
init();
