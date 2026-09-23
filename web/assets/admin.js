import {el, button, mount, api, announce, notice, clearNotice, focusHeading, field, select, formatDate, confirmDialog} from './common.js';
import {previewTree, summaryColumns} from './core.mjs';

const app = document.getElementById('app');
let auth;
let runs = [];
let drafts = [];
let versions = [];
let view = 'runs';
let modeFilter = 'real';
let selectedRun = '';
let result = null;
let editor = null;
let operation = false;
let loadGeneration = 0;
let previewTimer;
const owner = () => auth?.role === 'owner';
const idPath = id => encodeURIComponent(id);
const runPath = id => `/admin/api/runs/${idPath(id)}`;
const mutate = (path, method, body) => api(path, {method, body, csrf: auth.csrf_token});

function handleError(error) {
  if (error.status === 401) {
    notice('Your sign-in has expired. Sign in again to continue. Copy or download any unsaved changes first.', 'error', [button('Download local edits', () => downloadLocal(), 'secondary', {disabled: !editor}), button('Sign in again', () => { auth = null; renderLogin(); })]);
    return;
  }
  notice(error.message);
}

async function perform(action) {
  if (operation) return;
  operation = true;
  clearNotice();
  app.setAttribute('aria-busy', 'true');
  const buttons = [...app.querySelectorAll('button')].filter(node => !node.disabled);
  buttons.forEach(node => { node.disabled = true; });
  try { return await action(); } catch (error) { handleError(error); }
  finally {
    operation = false;
    app.setAttribute('aria-busy', 'false');
    buttons.forEach(node => { if (node.isConnected) node.disabled = false; });
    const publish = document.getElementById('publish-draft');
    if (publish && editor) publish.disabled = !editor.id || editor.dirty;
  }
}

async function refreshData() {
  const requests = [api('/admin/api/runs')];
  if (owner()) requests.push(api('/admin/api/drafts'), api('/admin/api/versions'));
  const [runData, draftData, versionData] = await Promise.all(requests);
  runs = runData.runs || [];
  if (owner()) { drafts = draftData.drafts || []; versions = versionData.versions || []; }
}

function renderIdentity() {
  const target = document.getElementById('identity');
  target.replaceChildren();
  if (!auth?.authenticated) return;
  target.append(el('span', {class: 'help', style: 'margin:0'}, auth.username), el('span', {class: 'badge'}, owner() ? 'Owner' : 'Analyst'), button('Sign out', () => perform(async () => {
    if (!await mayLeaveEditor()) return;
    await mutate('/admin/api/logout', 'POST');
    auth = null;
    editor = null;
    runs = []; drafts = []; versions = []; result = null;
    renderLogin();
  }), 'quiet small'));
}

function renderLogin() {
  renderIdentity();
  app.setAttribute('aria-busy', 'false');
  const username = el('input', {id: 'username', name: 'username', autocomplete: 'username', required: true});
  const password = el('input', {id: 'password', name: 'password', type: 'password', autocomplete: 'current-password', required: true});
  const submit = el('button', {type: 'submit'}, 'Sign in');
  const form = el('form', {onsubmit: event => {
    event.preventDefault();
    perform(async () => {
      submit.textContent = 'Signing in…';
      try {
        auth = await api('/admin/api/login', {method: 'POST', body: {username: username.value, password: password.value}});
        password.value = '';
        if (!owner()) editor = null;
        await refreshData();
        view = owner() ? 'runs' : 'results';
        renderWorkspace();
        focusHeading();
      } finally { submit.textContent = 'Sign in'; }
    });
  }}, field('Username', username), field('Password', password), submit);
  app.replaceChildren(el('section', {class: 'login-card'}, el('p', {class: 'eyebrow'}, 'Research workspace'), el('h1', {}, 'Welcome back'), el('p', {class: 'lede'}, 'Sign in to manage a study or review its results.'), el('div', {class: 'card'}, form), el('p', {class: 'help'}, 'Access is provided by the study administrator. Owners manage studies; analysts review blinded outcomes.')));
}

async function mayLeaveEditor() {
  if (!editor?.dirty) return true;
  return confirmDialog({title: 'Leave unsaved changes?', message: 'Your draft has changes that have not been saved to the server. Stay here to save or download them first.', confirm: 'Leave without saving', danger: true});
}

async function switchView(next) {
  if (operation || !await mayLeaveEditor()) return;
  editor = null;
  clearNotice();
  view = next;
  result = null;
  renderWorkspace();
  focusHeading();
}

function renderWorkspace() {
  renderIdentity();
  app.setAttribute('aria-busy', 'false');
  const tabs = [['runs', 'Runs & activity'], ...(owner() ? [['drafts', 'Drafts'], ['versions', 'Published versions']] : []), ['results', 'Blinded results']];
  const content = el('div', {id: 'workspace-content'});
  app.replaceChildren(el('nav', {class: 'workspace-nav', 'aria-label': 'Workspace'}, tabs.map(([id, title]) => button(title, () => switchView(id), '', {'aria-current': view === id ? 'page' : undefined}))), content);
  if (view === 'drafts' && owner()) { if (editor) renderEditor(content); else renderDrafts(content); }
  else if (view === 'versions' && owner()) renderVersions(content);
  else if (view === 'results') renderResults(content);
  else renderRuns(content);
}

function filteredRuns() { return runs.filter(run => modeFilter === 'all' || run.mode === modeFilter); }

function modeField(onchange) {
  return field('Run type', select('mode-filter', [['real', 'Real studies only'], ['pilot', 'Pilots only'], ['all', 'All runs']], modeFilter, event => {
    modeFilter = event.target.value;
    selectedRun = '';
    result = null;
    onchange();
  }));
}

function runBadges(run) { return [el('span', {class: `badge ${run.mode}`}, run.mode === 'pilot' ? 'Pilot' : 'Real study'), el('span', {class: `badge ${run.status}`}, run.status)]; }

function renderRuns(content) {
  content.replaceChildren(el('div', {class: 'section-heading'}, el('div', {}, el('p', {class: 'eyebrow'}, 'Operations'), el('h1', {}, 'Runs & activity'), el('p', {class: 'muted'}, 'Monitor participation and enrollment. Review outcomes in Blinded results.')), button('Refresh', () => perform(async () => { await refreshData(); result = null; renderWorkspace(); }))),
    el('div', {class: 'filter-bar'}, modeField(() => renderWorkspace())),
    el('p', {class: 'help'}, 'Real studies are shown by default. Pilot data stays in its own run and is never combined here with real-study results.'));
  const visible = filteredRuns();
  if (!visible.length) content.append(el('div', {class: 'empty'}, modeFilter === 'real' ? 'No real-study runs yet. Select pilots to view trial runs.' : 'No runs match this filter.'));
  for (const run of visible) {
    const actions = [button('View activity', () => { selectedRun = run.id; loadActivity(); })];
    if (owner()) {
      for (const status of ['open', 'paused', 'closed']) if (status !== run.status) actions.push(button(status === 'open' ? 'Open enrollment' : status === 'paused' ? 'Pause' : 'Close', () => changeRunStatus(run, status), status === 'closed' ? 'danger small' : 'secondary small'));
    }
    content.append(el('article', {class: 'list-card'}, el('div', {}, el('h2', {class: 'word-break'}, run.slug), el('div', {class: 'status-strip'}, runBadges(run)), el('p', {style: 'margin-top:.5rem'}, `Created ${formatDate(run.created_at)}`), owner() && el('a', {href: `/s/${encodeURIComponent(run.slug)}`, class: 'subtle-link', target: '_blank', rel: 'noopener'}, 'Open participant link ↗')), el('div', {class: 'actions'}, actions)));
  }
  content.append(el('section', {id: 'activity-detail', 'aria-live': 'polite'}));
  if (selectedRun && visible.some(run => run.id === selectedRun)) loadActivity();
  if (owner()) content.append(createRunForm());
}

async function changeRunStatus(run, status) {
  const descriptions = {open: 'New participants will be able to join this run.', paused: 'New enrollment will pause. Participants who have already joined can continue.', closed: 'New enrollment will close. Participants who have already joined can still finish.'};
  if (!await confirmDialog({title: `${status === 'open' ? 'Open' : status === 'paused' ? 'Pause' : 'Close'} ${run.slug}?`, message: descriptions[status], confirm: status === 'open' ? 'Open enrollment' : status === 'paused' ? 'Pause enrollment' : 'Close enrollment', danger: status === 'closed'})) return;
  perform(async () => {
    await mutate(runPath(run.id), 'PATCH', {status});
    await refreshData();
    renderWorkspace();
    announce(`Run ${run.slug} is now ${status}.`);
  });
}

async function loadActivity() {
  const target = document.getElementById('activity-detail');
  if (!target) return;
  const requestID = ++loadGeneration;
  const run = runs.find(item => item.id === selectedRun);
  if (!run) return;
  target.replaceChildren(el('p', {class: 'help'}, 'Loading participation counts…'));
  try {
    const data = await api(`${runPath(run.id)}/results`);
    if (requestID !== loadGeneration || !target.isConnected) return;
    target.replaceChildren(el('div', {class: 'section-heading'}, el('div', {}, el('h2', {}, `Activity · ${run.slug}`), el('p', {class: 'help'}, 'Operational counts only. Enrollment and completion do not describe task success.'))), healthCards(data.health), button('Review blinded outcomes', () => { view = 'results'; result = data; renderWorkspace(); focusHeading(); }, 'secondary'));
  } catch (error) { if (target.isConnected) target.replaceChildren(el('div', {class: 'message error'}, error.message, button('Retry', loadActivity, 'quiet'))); }
}

function healthCards(health = {}) {
  const entries = [['Enrolled', health.enrolled ?? 0], ['Completed sessions', health.completed ?? 0], ['Incomplete sessions', health.incomplete ?? 0], ['Last recorded event', formatDate(health.last_event_at)]];
  return el('dl', {class: 'metric-grid'}, entries.map(([title, value], index) => el('div', {class: 'metric'}, el('dt', {}, title), el('dd', {class: index === 3 ? 'timestamp' : ''}, value))));
}

function createRunForm(versionID) {
  if (!versions.length) return el('div', {class: 'card'}, el('h2', {}, 'Create your first run'), el('p', {class: 'muted'}, 'Publish a validated draft to freeze a study version, then create a pilot or real run from it.'), button('Go to drafts', () => switchView('drafts')));
  const versionInput = select('run-version', versions.map(version => [version.id, `${version.title} · ${version.hash.slice(0, 10)}`]), versionID || versions[0].id);
  const slugInput = el('input', {id: 'run-slug', name: 'slug', required: true, pattern: '[a-z][a-z0-9-]{0,95}', maxlength: 96, placeholder: 'docs-navigation-pilot', autocomplete: 'off'});
  const modeInput = select('run-mode', [['pilot', 'Pilot · try the complete workflow'], ['real', 'Real · collect study responses']], 'pilot');
  return el('form', {class: 'card', onsubmit: event => {
    event.preventDefault();
    perform(async () => {
      const run = await mutate('/admin/api/runs', 'POST', {version_id: versionInput.value, slug: slugInput.value, mode: modeInput.value});
      await refreshData();
      modeFilter = run.mode;
      selectedRun = run.id;
      view = 'runs';
      renderWorkspace();
      notice(`Created ${run.mode === 'pilot' ? 'pilot' : 'real-study'} run “${run.slug}”. Enrollment is paused; open it when you’re ready to share the link.`, 'success');
    });
  }}, el('h2', {}, 'Create a run'), el('p', {class: 'help'}, 'Each run uses an immutable published version. New runs begin paused.'), field('Published version', versionInput), el('div', {class: 'field-grid'}, field('Public link name', slugInput, 'Lowercase letters, numbers and hyphens. This becomes /s/your-link-name.'), field('Purpose', modeInput)), el('button', {type: 'submit'}, 'Create paused run'));
}

function renderDrafts(content) {
  content.replaceChildren(el('div', {class: 'section-heading'}, el('div', {}, el('p', {class: 'eyebrow'}, 'Authoring'), el('h1', {}, 'Study drafts'), el('p', {class: 'muted'}, 'Prepare the trees, task bank, accepted pages, and balanced task panels.')), button('New draft', () => { editor = newEditor(); renderWorkspace(); focusHeading(); }, 'primary')),
    el('div', {class: 'message'}, 'As an owner, you know the trees and their accepted pages. Blinded result exports keep the arm mapping separate; owner access itself is not blinded.'));
  if (!drafts.length) content.append(el('div', {class: 'empty'}, 'No drafts yet. Start a new draft and upload a study bundle, or edit the included starter.'));
  for (const draft of drafts) content.append(el('article', {class: 'list-card'}, el('div', {}, el('h2', {}, draft.title), el('p', {}, `Revision ${draft.revision} · Updated ${formatDate(draft.updated_at)}`)), button('Edit draft', () => perform(async () => { await openDraft(draft.id); renderWorkspace(); focusHeading(); }))));
}

function starterBundle() {
  const pages = [['install', 'Install the application', 'You are using the application for the first time. Where would you look for installation steps?', 'easy'], ['connect', 'Connect a data source', 'You want to connect your first data source. Where would you look?', 'easy'], ['access', 'Manage team access', 'A teammate needs access to your workspace. Where would you look for the steps?', 'medium'], ['reports', 'Schedule a report', 'You want a report to arrive automatically each week. Where would you look?', 'medium'], ['backup', 'Restore a backup', 'You need to recover your workspace from a saved backup. Where would you look?', 'hard'], ['alerts', 'Configure alert delivery', 'You want to change where automated alerts are delivered. Where would you look?', 'hard']];
  return {config: {schema_version: 1, slug: 'navigation-study', title: 'Documentation navigation study', instructions: 'Choose the page where you would expect to find the information. You do not need to know the answer to the task itself.', tasks_per_session: 6, variants: [{id: 'current', name: 'Current navigation', tree: 'current.md'}, {id: 'proposed', name: 'Proposed navigation', tree: 'proposed.md'}], tasks: pages.map(([id, , prompt, difficulty]) => ({id, prompt, difficulty, answers: {current: [id], proposed: [id]}})), panels: [pages.map(([id]) => id)]}, trees: {'current.md': '# Current navigation\n- [Getting started](group:start)\n' + pages.slice(0, 2).map(([id, label]) => `  - [${label}](page:${id}/${id})\n`).join('') + '- [Workspace guides](group:guides)\n' + pages.slice(2).map(([id, label]) => `  - [${label}](page:${id}/${id})\n`).join(''), 'proposed.md': '# Proposed navigation\n' + pages.map(([id, label]) => `- [${label}](page:${id}/${id})\n`).join('')}};
}

function newEditor(bundle = starterBundle(), id = null, revision = null) {
  return {id, revision, configText: JSON.stringify(bundle.config, null, 2), trees: {...bundle.trees}, activeFile: Object.keys(bundle.trees)[0] || '', dirty: !id, validation: null};
}

async function openDraft(id) {
  const data = await api(`/admin/api/drafts/${idPath(id)}`);
  editor = newEditor(data.bundle, data.id, data.revision);
}

function editorBundle() {
  let config;
  try { config = JSON.parse(editor.configText); } catch (error) { throw new Error(`study.json is not valid JSON: ${error.message}`); }
  if (!config || typeof config !== 'object' || Array.isArray(config)) throw new Error('study.json must contain a configuration object.');
  return {config, trees: {...editor.trees}};
}

function markDirty() {
  editor.dirty = true;
  editor.validation = null;
  const status = document.getElementById('editor-status');
  if (status) status.textContent = 'Unsaved changes';
  const publish = document.getElementById('publish-draft');
  if (publish) publish.disabled = true;
  const validation = document.getElementById('validation-status');
  if (validation) validation.replaceChildren(el('p', {class: 'help'}, 'Changes have not been validated. Run server validation before publishing.'));
  clearTimeout(previewTimer);
  previewTimer = setTimeout(updatePreview, 300);
}

function renderEditor(content) {
  const bundleUpload = el('input', {id: 'study-files-upload', type: 'file', accept: '.json,.md,application/json,text/markdown,text/plain', multiple: true, onchange: async event => {
    const input = event.target;
    try { await importStudyFiles([...input.files]); } finally { input.value = ''; }
  }});
  const configInput = el('textarea', {id: 'study-config', class: 'editor-textarea', spellcheck: 'false', rows: 22, value: editor.configText, oninput: event => { editor.configText = event.target.value; markDirty(); }});
  const configUpload = el('input', {id: 'config-upload', type: 'file', accept: '.json,application/json', onchange: event => importConfig(event.target.files?.[0])});
  const treeUpload = el('input', {id: 'tree-upload', type: 'file', accept: '.md,text/markdown,text/plain', multiple: true, onchange: event => importTrees([...event.target.files])});
  content.replaceChildren(el('div', {class: 'section-heading'}, el('div', {}, el('p', {class: 'eyebrow'}, 'Study bundle editor'), el('h1', {}, editor.id ? 'Edit draft' : 'Create a draft'), el('p', {class: 'muted'}, editor.id ? `Revision ${editor.revision}. Saves check this revision to protect concurrent edits.` : 'Upload your bundle or replace the illustrative six-task starter.')), button('Back to drafts', () => switchView('drafts'))),
    el('section', {class: 'card'}, el('h2', {}, 'Import a complete study'), field('Import study files', bundleUpload, 'Select study.json and all its referenced .md files together. After confirmation, these replace the whole editor bundle, including any starter trees. You can then edit individual files below.')),
    el('div', {class: 'editor-grid'},
      el('div', {}, el('section', {class: 'editor-section'}, el('h2', {}, '1. Study configuration'), field('Upload study.json or downloaded edits', configUpload), field('study.json', configInput, 'Edit the title, instructions, variants, task prompts, accepted content IDs, and explicit panels. JSON only.')),
        el('section', {class: 'editor-section'}, el('h2', {}, '2. Tree files'), field('Upload Markdown files', treeUpload, 'Select all sibling .md files referenced by variants in study.json. Each file is editable below.'), el('div', {id: 'file-editor'}),
          el('details', {}, el('summary', {}, 'Tree format reference'), el('p', {class: 'help'}, 'Use two spaces per nesting level. A group needs children; a page can have children too. Placement IDs are unique in each tree. Content IDs identify accepted pages and may appear at multiple placements.'), el('pre', {}, '- [Guides](group:guides)\n  - [Set up access](page:access-guide/access)\n    - [Invite a teammate](page:invite-guide/invite)')))),
      el('aside', {}, el('section', {class: 'editor-section'}, el('h2', {}, '3. Preview & task mappings'), el('p', {class: 'help'}, 'Owner-only authoring preview. All labels are shown as plain text. Server validation is authoritative.'), el('div', {id: 'bundle-preview'})), el('section', {class: 'editor-section'}, el('h2', {}, 'Validation'), el('div', {id: 'validation-status'})))),
    el('div', {class: 'sticky-actions'}, el('div', {class: 'actions spread'}, el('div', {class: 'actions'}, button('Validate bundle', () => perform(validateBundle)), button('Save draft', () => perform(saveDraft), 'primary'), button('Publish version', () => perform(publishDraft), 'secondary', {id: 'publish-draft', disabled: !editor.id || editor.dirty})), el('div', {class: 'actions'}, el('span', {id: 'editor-status', class: 'dirty-indicator'}, editor.dirty ? 'Unsaved changes' : `Saved · revision ${editor.revision}`), button('Download edits', downloadLocal, 'quiet small')))));
  renderFileEditor();
  updatePreview();
  renderValidation();
}

function renderFileEditor() {
  const target = document.getElementById('file-editor');
  if (!target) return;
  const names = Object.keys(editor.trees);
  if (!names.includes(editor.activeFile)) editor.activeFile = names[0] || '';
  const nameInput = el('input', {id: 'new-file-name', placeholder: 'navigation.md', pattern: '[^.][^/\\\\]*\\.md', autocomplete: 'off'});
  target.replaceChildren(el('div', {class: 'file-tabs', role: 'group', 'aria-label': 'Tree files'}, names.map(name => button(name, () => { editor.activeFile = name; renderFileEditor(); }, 'secondary', {'aria-pressed': editor.activeFile === name ? 'true' : 'false'}))));
  if (editor.activeFile) {
    const name = editor.activeFile;
    target.append(field(`Edit ${name}`, el('textarea', {id: 'tree-source', class: 'editor-textarea', rows: 16, value: editor.trees[name], spellcheck: 'false', oninput: event => { editor.trees[name] = event.target.value; markDirty(); }})), button('Remove this file', async () => {
      if (!await confirmDialog({title: `Remove ${name}?`, message: 'This removes the file from your draft. Update any variant that references it in study.json before saving.', confirm: 'Remove file', danger: true})) return;
      delete editor.trees[name]; editor.activeFile = ''; markDirty(); renderFileEditor();
    }, 'danger small'));
  } else target.append(el('p', {class: 'help'}, 'No tree files yet. Upload Markdown files or create one below.'));
  target.append(el('hr', {class: 'divider'}), field('New tree filename', nameInput), button('Add empty file', () => {
    const name = nameInput.value.trim();
    if (!/^[^.][^/\\]*\.md$/.test(name) || name.length > 128) { notice('Use a sibling Markdown filename ending in .md, without slashes or a leading dot.'); nameInput.focus(); return; }
    if (Object.hasOwn(editor.trees, name)) { notice('A file with that name already exists. Select it above to edit.'); return; }
    editor.trees[name] = ''; editor.activeFile = name; markDirty(); renderFileEditor(); document.getElementById('tree-source').focus();
  }, 'secondary small'));
}

async function importStudyFiles(files) {
  if (!files.length || operation) return;
  const targetEditor = editor;
  try {
    const jsonFiles = files.filter(file => file.name.endsWith('.json'));
    if (jsonFiles.length !== 1) throw new Error('Select exactly one study.json configuration together with its referenced Markdown files.');
    if (files.some(file => !file.name.endsWith('.json') && !file.name.endsWith('.md'))) throw new Error('A study import accepts only .json and .md files.');
    if (new Set(files.map(file => file.name)).size !== files.length) throw new Error('Two selected files have the same name. Select files from a single study bundle.');
    if (files.reduce((total, file) => total + file.size, 0) > 8 * 1024 * 1024) throw new Error('The selected study files exceed the 8 MiB bundle limit.');
    const configFile = jsonFiles[0];
    if (configFile.size > 2 * 1024 * 1024) throw new Error('The study configuration must be at most 2 MiB.');
    const configText = await configFile.text();
    let config;
    try { config = JSON.parse(configText); } catch (error) { throw new Error(`${configFile.name} is not valid JSON: ${error.message}`); }
    if (!config || typeof config !== 'object' || Array.isArray(config) || !Array.isArray(config.variants) || !config.variants.length) throw new Error('The JSON file must be a study configuration with a variants array. Use the downloaded-edits input below for a saved bundle.');
    const references = [...new Set(config.variants.map(variant => variant?.tree))];
    if (references.some(name => typeof name !== 'string' || !/^[^.][^/\\]*\.md$/.test(name) || name.length > 128)) throw new Error('Every variant must reference a sibling .md filename, without slashes or a leading dot.');
    const selectedFiles = new Map(files.map(file => [file.name, file]));
    const missing = references.filter(name => !selectedFiles.has(name));
    if (missing.length) throw new Error(`Select all referenced Markdown files. Missing: ${missing.join(', ')}.`);
    const trees = Object.fromEntries(await Promise.all(references.map(async name => {
      const file = selectedFiles.get(name);
      if (file.size > 1024 * 1024) throw new Error(`${name} exceeds the 1 MiB tree size limit.`);
      return [name, await file.text()];
    })));
    if (editor !== targetEditor) return;
    if (!await confirmDialog({title: 'Replace the complete study bundle?', message: `Import “${config.title || configFile.name}” with ${references.length} referenced tree files? This replaces the configuration and every tree currently in the editor.`, confirm: 'Import study files'})) return;
    if (editor !== targetEditor) return;
    editor.configText = configText;
    editor.trees = trees;
    editor.activeFile = references[0];
    markDirty();
    clearNotice();
    renderWorkspace();
    announce('Complete study bundle imported. Validate and save it to keep it on the server.');
  } catch (error) { handleError(error); }
}

async function importConfig(file) {
  if (!file) return;
  try {
    if (file.size > 8 * 1024 * 1024) throw new Error('Configuration or saved bundle must be at most 8 MiB.');
    const text = await file.text();
    const parsed = JSON.parse(text);
    const isBundle = parsed && (parsed.config || typeof parsed.config_text === 'string') && parsed.trees && typeof parsed.trees === 'object' && !Array.isArray(parsed.trees);
    if (isBundle && Object.values(parsed.trees).some(value => typeof value !== 'string')) throw new Error('Every tree in a saved bundle must be Markdown text.');
    if (!await confirmDialog({title: isBundle ? 'Restore downloaded edits?' : 'Replace study.json?', message: isBundle ? 'This restores the configuration and all tree files from the downloaded edits, replacing the contents currently in the editor.' : 'The uploaded file will replace the configuration currently in the editor. Your tree files will stay in the editor.', confirm: isBundle ? 'Restore edits' : 'Replace configuration'})) return;
    editor.configText = isBundle ? parsed.config_text ?? JSON.stringify(parsed.config, null, 2) : text;
    if (isBundle) { editor.trees = {...parsed.trees}; editor.activeFile = ''; }
    markDirty();
    renderWorkspace();
    announce('Configuration loaded into the editor. Save the draft to keep it on the server.');
  } catch (error) { handleError(error); }
}

async function importTrees(files) {
  if (!files.length) return;
  try {
    const loaded = await Promise.all(files.map(async file => {
      if (!/^[^.][^/\\]*\.md$/.test(file.name) || file.name.length > 128) throw new Error(`${file.name}: expected a sibling .md filename.`);
      if (file.size > 1024 * 1024) throw new Error(`${file.name} exceeds the 1 MiB tree size limit.`);
      return [file.name, await file.text()];
    }));
    const duplicates = loaded.filter(([name]) => Object.hasOwn(editor.trees, name)).map(([name]) => name);
    if (duplicates.length && !await confirmDialog({title: 'Replace existing tree files?', message: `These uploaded files will replace editor contents: ${duplicates.join(', ')}.`, confirm: 'Replace files'})) return;
    for (const [name, text] of loaded) editor.trees[name] = text;
    editor.activeFile = loaded[0][0];
    markDirty(); renderFileEditor();
    announce(`${loaded.length} tree files loaded. Save the draft to keep them on the server.`);
  } catch (error) { handleError(error); }
}

function updatePreview() {
  const target = document.getElementById('bundle-preview');
  if (!target || !editor) return;
  try {
    const bundle = editorBundle();
    const config = bundle.config;
    if (!Array.isArray(config.variants) || !Array.isArray(config.tasks)) throw new Error('Add variants and tasks arrays to study.json to preview the bundle.');
    const pathsByVariant = new Map();
    const treeSections = [];
    for (const variant of config.variants) {
      if (typeof bundle.trees[variant.tree] !== 'string') { treeSections.push(el('div', {class: 'message warning'}, `Missing file: ${variant.tree}`)); continue; }
      const parsed = previewTree(bundle.trees[variant.tree]);
      const paths = new Map();
      function walk(items, parents = []) {
        return el('ul', {class: 'preview-tree'}, items.map(node => {
          const path = [...parents, node.label];
          if (node.content_id) paths.set(node.content_id, [...(paths.get(node.content_id) || []), path.join(' / ')]);
          return el('li', {}, el('span', {}, node.label), el('span', {class: 'badge'}, node.content_id ? 'page' : 'group'), node.content_id && el('code', {}, node.content_id), node.children.length && walk(node.children, path));
        }));
      }
      const treeDOM = walk(parsed.nodes);
      pathsByVariant.set(variant.id, paths);
      treeSections.push(el('details', {open: true}, el('summary', {}, `${variant.name || variant.id} · ${variant.tree}`), treeDOM, parsed.warnings.length > 0 && el('div', {class: 'message warning'}, el('ul', {}, parsed.warnings.map(text => el('li', {}, text))))));
    }
    const mappings = config.tasks.map(task => el('article', {class: 'mapping-card'}, el('div', {class: 'status-strip'}, el('strong', {}, task.id), el('span', {class: 'badge'}, task.difficulty)), el('p', {}, task.prompt), el('ul', {}, config.variants.map(variant => {
      const answers = task.answers?.[variant.id];
      const labels = Array.isArray(answers) ? answers.map(id => {
        const matches = pathsByVariant.get(variant.id)?.get(id);
        return `${id}: ${matches?.join(' OR ') || '⚠ not found in this tree'}`;
      }) : ['⚠ no accepted content IDs'];
      return el('li', {}, el('strong', {}, `${variant.name || variant.id} — `), labels.join('; '));
    }))));
    const panels = Array.isArray(config.panels) ? config.panels : [];
    target.replaceChildren(el('p', {class: 'help'}, `${config.variants.length} variants · ${config.tasks.length} tasks · ${panels.length} panels · ${config.tasks_per_session ?? '—'} tasks per session`), ...treeSections,
      el('details', {open: true}, el('summary', {}, 'Accepted pages by task'), mappings), el('details', {}, el('summary', {}, 'Task panels'), el('ol', {}, panels.map(panel => el('li', {class: 'help'}, Array.isArray(panel) ? panel.join(' · ') : 'Invalid panel')))));
  } catch (error) { target.replaceChildren(el('p', {class: 'help'}, error.message)); }
}

function renderValidation() {
  const target = document.getElementById('validation-status');
  if (!target) return;
  const valid = editor.validation;
  if (!valid) { target.replaceChildren(el('p', {class: 'help'}, 'Run server validation to check tree grammar, accepted pages, panel balance, and the complete bundle.')); return; }
    target.replaceChildren(el('div', {class: 'message'}, el('strong', {}, 'Bundle is valid'), el('p', {class: 'help'}, `${valid.task_count} tasks · ${valid.panel_count} panels`)), el('ul', {class: 'help'}, Object.entries(valid.node_counts || {}).map(([id, count]) => el('li', {}, `${id}: ${count} nodes`))), el('p', {class: 'help'}, 'Version hash · content and policy'), el('code', {}, valid.hash));
}

async function validateBundle() {
  const bundle = editorBundle();
  const original = JSON.stringify(bundle);
  const valid = await mutate('/admin/api/validate', 'POST', bundle);
  if (JSON.stringify(editorBundle()) !== original) { notice('The checked bundle was valid, but you made further edits. Validate again to check the current contents.', 'warning'); return; }
  editor.validation = valid;
  renderValidation();
  announce('Server validation passed.');
}

async function saveDraft() {
  const bundle = editorBundle();
  const snapshot = JSON.stringify(bundle);
  try {
    const response = editor.id ? await mutate(`/admin/api/drafts/${idPath(editor.id)}`, 'PUT', {revision: editor.revision, bundle}) : await mutate('/admin/api/drafts', 'POST', bundle);
    editor.id = response.id;
    editor.revision = response.revision;
    editor.dirty = JSON.stringify(editorBundle()) !== snapshot;
    await refreshData();
    renderWorkspace();
    announce(`Draft saved as revision ${editor.revision}.`);
  } catch (error) {
    if (error.status !== 409) throw error;
    notice('This draft has been updated elsewhere. Your edits are still in this editor. Download them before loading the latest server revision.', 'warning', [button('Download local edits', downloadLocal), button('Load latest revision', async () => {
      if (!await mayLeaveEditor()) return;
      perform(async () => { await openDraft(editor.id); renderWorkspace(); clearNotice(); });
    })]);
  }
}

async function publishDraft() {
  if (!editor.id || editor.dirty) throw new Error('Save your draft before publishing.');
  if (!await confirmDialog({title: 'Publish an immutable version?', message: `Revision ${editor.revision} will be frozen with its trees, task mappings, and scoring policy. You can create separate pilot and real runs from this version.`, confirm: 'Publish version'})) return;
  const published = await mutate(`/admin/api/drafts/${idPath(editor.id)}/publish`, 'POST', {revision: editor.revision});
  await refreshData();
  editor = null;
  view = 'versions';
  renderWorkspace();
  notice(`Published version ${published.id}. Its content hash is ${published.hash}. Create a run when ready.`, 'success');
}

function downloadBlob(blob, filename) {
  const url = URL.createObjectURL(blob);
  const link = el('a', {href: url, download: filename});
  document.body.append(link); link.click(); link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10000);
}

function downloadLocal() {
  if (!editor) return;
  let data;
  try { data = editorBundle(); } catch { data = {config_text: editor.configText, trees: editor.trees}; }
  downloadBlob(new Blob([JSON.stringify(data, null, 2)], {type: 'application/json'}), 'study-bundle-edits.json');
  announce('Local edits downloaded.');
}

function renderVersions(content) {
  content.replaceChildren(el('p', {class: 'eyebrow'}, 'Frozen study snapshots'), el('h1', {}, 'Published versions'), el('p', {class: 'muted'}, 'Each version preserves its source bundle, parsed trees, and scoring policy. Editing a draft never changes an existing version or run.'));
  if (!versions.length) content.append(el('div', {class: 'empty'}, 'No published versions yet. Validate and save a draft, then publish it.'));
  for (const version of versions) content.append(el('article', {class: 'list-card'}, el('div', {}, el('h2', {}, version.title), el('p', {}, `${version.slug} · ${formatDate(version.created_at)}`), el('p', {class: 'help'}, 'SHA-256 ', el('code', {}, version.hash))), button('Use for a new run', () => {
    const target = document.getElementById('version-run-form');
    target.replaceChildren(createRunForm(version.id));
    target.scrollIntoView({behavior: 'auto', block: 'start'});
    document.getElementById('run-slug').focus({preventScroll: true});
  })));
  content.append(el('div', {id: 'version-run-form'}));
}

function renderResults(content) {
  const visible = filteredRuns();
  if (!visible.some(run => run.id === selectedRun)) { selectedRun = visible[0]?.id || ''; result = null; }
  const runInput = select('results-run', visible.length ? visible.map(run => [run.id, `${run.slug} · ${run.mode} · ${run.status}`]) : [['', 'No matching runs']], selectedRun, event => { selectedRun = event.target.value; result = null; loadResults(); });
  runInput.disabled = !visible.length;
  content.replaceChildren(el('div', {class: 'section-heading'}, el('div', {}, el('p', {class: 'eyebrow'}, 'Analysis'), el('h1', {}, 'Blinded results'), el('p', {class: 'muted'}, 'Compare outcomes using neutral arm codes. Runs are analyzed separately.')), button('Refresh', () => perform(async () => { await refreshData(); result = null; renderWorkspace(); }))),
    el('div', {class: 'filter-bar'}, modeField(() => renderWorkspace()), field('Run', runInput)),
    el('p', {class: 'help'}, 'Real runs only by default. Pilot responses are exploratory and remain separate.'),
    el('div', {id: 'results-detail'}));
  if (!selectedRun) document.getElementById('results-detail').append(el('div', {class: 'empty'}, 'No runs match this filter.'));
  else if (result) paintResults();
  else loadResults();
}

async function loadResults() {
  const target = document.getElementById('results-detail');
  if (!target || !selectedRun) return;
  const requestID = ++loadGeneration;
  target.replaceChildren(el('div', {class: 'loading-card'}, el('span', {class: 'spinner', 'aria-hidden': 'true'}), el('p', {}, 'Loading blinded outcomes…')));
  try {
    const data = await api(`${runPath(selectedRun)}/results`);
    if (requestID !== loadGeneration || !target.isConnected) return;
    result = data;
    paintResults();
    announce('Blinded results loaded.');
  } catch (error) { if (target.isConnected) target.replaceChildren(el('div', {class: 'message error'}, el('p', {}, error.message), button('Retry', loadResults))); }
}

function summaryTable(rows, caption, withTask) {
  if (!rows?.length) return el('div', {class: 'empty'}, 'No assigned tasks to summarize yet.');
  const columns = summaryColumns.filter(([key]) => withTask || key !== 'task_id');
  return el('div', {class: 'table-wrap', role: 'region', 'aria-label': caption, tabindex: '0'}, el('table', {}, el('caption', {}, caption), el('thead', {}, el('tr', {}, columns.map(([key, label]) => el('th', {scope: 'col', title: key}, label)))), el('tbody', {}, rows.map(row => el('tr', {}, columns.map(([key, , formatter]) => el(key === 'code' ? 'th' : 'td', key === 'code' ? {scope: 'row'} : {}, formatter ? formatter(row[key]) : row[key] ?? '—')))))));
}

function exportLink(run, suffix, label, filename) {
  const href = `${runPath(run.id)}/${suffix}`;
  return el('a', {href, download: filename, class: 'button secondary small', onclick: async event => {
    if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    const link = event.currentTarget;
    if (link.getAttribute('aria-busy') === 'true') return;
    link.setAttribute('aria-busy', 'true');
    try {
      const response = await fetch(href, {credentials: 'same-origin', mode: 'same-origin', cache: 'no-store'});
      if (!response.ok) { let message = `Download failed (${response.status}).`; try { message = (await response.json()).error || message; } catch {} throw new Error(message); }
      downloadBlob(await response.blob(), filename);
      announce(`${label} downloaded.`);
    } catch (error) { notice(error.message || 'Download failed. Check your connection and try again.'); }
    finally { link.removeAttribute('aria-busy'); }
  }}, label);
}

function paintResults() {
  const target = document.getElementById('results-detail');
  const run = runs.find(item => item.id === selectedRun);
  if (!target || !run || !result) return;
  mount(target, el('div', {class: 'section-heading'}, el('div', {}, el('h2', {class: 'word-break'}, run.slug), el('div', {class: 'status-strip'}, runBadges(run))), el('div', {class: 'download-links'}, exportLink(run, 'export?format=csv', 'Download blinded CSV', `${run.slug}-blinded.csv`), exportLink(run, 'export?format=json', 'Download blinded JSON', `${run.slug}-blinded.json`))),
    run.mode === 'pilot' && el('div', {class: 'message warning'}, 'Pilot run. Use these responses to check the workflow and refine the study; keep them separate from real-study analysis.'),
    el('p', {class: 'help'}, 'Exports contain every assigned task, including tasks whose navigation was not started. A participant may have read a prompt without starting it. Arm codes do not identify the source trees.'),
    summaryTable(result.arms, 'Outcomes by blinded arm', false), summaryTable(result.tasks, 'Outcomes by arm and task', true),
    el('details', {class: 'card', open: true}, el('summary', {}, 'Reading these results'), el('div', {class: 'definition-grid'},
      el('p', {}, el('strong', {}, 'Success yield'), 'Correct selections divided by all assigned tasks. Not-started tasks and unfinished navigation stay in the denominator.'),
      el('p', {}, el('strong', {}, 'Started-task success'), 'Correct selections divided by started tasks. Started means the server accepted a task start, including a comprehension skip; it does not mean the navigation tree was viewed.'),
      el('p', {}, el('strong', {}, 'Unfinished navigation and not started'), 'Unfinished navigation has a recorded start but no submitted outcome. Not started has no recorded start; the participant may still have read that task’s prompt. Incorrect selections, “can’t find,” and comprehension skips are separate outcomes.'),
      el('p', {}, el('strong', {}, 'Median uninterrupted navigation'), 'Seconds from navigation start through the confirmation click, only for finalized selections and “can’t find” responses with complete, uninterrupted timing. Skips, unfinished attempts, and interrupted timing are excluded. — means no eligible timing.'),
      el('p', {}, el('strong', {}, 'Interrupted and partial timing'), 'Interrupted attempts are counted separately. Export fields timing_quality and clock_epochs identify timing coverage; observed_navigation_ms may contain only partial observed time. server_elapsed_ms includes server-side elapsed time and is not precise active navigation time.'),
      el('p', {}, el('strong', {}, 'Uncertainty'), 'Use participants—not individual task attempts—as the independent units for uncertainty analysis. Read timing alongside outcomes and interruption counts.'))),
    owner() && el('section', {class: 'private-section'}, el('h2', {}, 'Owner-only research material'), el('p', {class: 'help'}, 'Owners already know the authored trees. The mapping key and immutable source bundle are separate from blinded exports. Detailed event paths can also reveal the arms. Analysts do not have access to these files.'), el('div', {class: 'download-links'}, exportLink(run, 'key', 'Download mapping & source bundle', `${run.slug}-owner-key.json`), exportLink(run, 'events', 'Download detailed events', `${run.slug}-owner-events.json`))));
}

window.addEventListener('beforeunload', event => { if (editor?.dirty) { event.preventDefault(); event.returnValue = ''; } });

async function init() {
  try {
    auth = await api('/admin/api/session');
    if (!auth.authenticated) { renderLogin(); return; }
    await refreshData();
    view = owner() ? 'runs' : 'results';
    renderWorkspace();
  } catch (error) {
    app.setAttribute('aria-busy', 'false');
    app.replaceChildren(el('h1', {}, 'Couldn’t open the workspace'), button('Try again', () => location.reload()));
    handleError(error);
  }
}
init();
