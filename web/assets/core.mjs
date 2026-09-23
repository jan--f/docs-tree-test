export function uuid() {
  if (globalThis.crypto.randomUUID) return globalThis.crypto.randomUUID();
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 15) | 64;
  bytes[8] = (bytes[8] & 63) | 128;
  const hex = Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export class SequenceConflict extends Error {
  constructor() { super('Your study progress changed in another tab, or saved events no longer match. Reload this page before continuing.'); this.name = 'SequenceConflict'; }
}

export function eventEqual(a, b) {
  return ['id', 'seq', 'type', 'elapsed_ms', 'epoch', 'visible'].every(key => a[key] === b[key]) && (a.node_id || null) === (b.node_id || null) && (a.outcome || null) === (b.outcome || null);
}

// Reconcile by identity and sequence, never renumber or silently merge streams.
export function reconcileEvents(serverEvents, pending, nextSeq) {
  const bySeq = new Map(serverEvents.map(event => [event.seq, event]));
  const byID = new Map(serverEvents.map(event => [event.id, event]));
  const remaining = [];
  for (const event of pending) {
    const existing = bySeq.get(event.seq) || byID.get(event.id);
    if (existing) {
      if (!eventEqual(existing, event)) throw new SequenceConflict();
    } else {
      if (event.seq < nextSeq) throw new SequenceConflict();
      remaining.push(event);
    }
  }
  remaining.sort((a, b) => a.seq - b.seq);
  const ids = new Set(serverEvents.map(event => event.id));
  remaining.forEach((event, index) => {
    if (event.seq !== nextSeq + index || ids.has(event.id)) throw new SequenceConflict();
    ids.add(event.id);
  });
  return {pending: remaining, events: [...serverEvents, ...remaining].sort((a, b) => a.seq - b.seq), nextSeq: nextSeq + remaining.length};
}

export function indexTree(tree) {
  const nodes = new Map();
  function walk(items, parents) {
    for (const node of items || []) {
      nodes.set(node.id, {node, parents});
      walk(node.children, [...parents, node.id]);
    }
  }
  walk(tree, []);
  return nodes;
}

export function replayNavigation(events, nodes) {
  let currentNodeId = null;
  let selectedId = null;
  for (const event of [...events].sort((a, b) => a.seq - b.seq)) {
    if (['tree_shown', 'root'].includes(event.type)) { currentNodeId = null; selectedId = null; }
    if (['enter', 'back'].includes(event.type)) {
      currentNodeId = nodes.has(event.node_id) ? event.node_id : null;
      selectedId = null;
    }
    if (event.type === 'select') selectedId = nodes.get(event.node_id)?.node.selectable ? event.node_id : null;
  }
  return {currentNodeId, selectedId};
}

export function ackEvents(events, nextSeq) {
  if (!Number.isInteger(nextSeq) || nextSeq < 1 || (events.length && nextSeq > Math.max(...events.map(event => event.seq)) + 1)) throw new SequenceConflict();
  return events.filter(event => event.seq >= nextSeq);
}

export function fraction(value) { return typeof value === 'number' && Number.isFinite(value) ? `${(value * 100).toFixed(1)}%` : '—'; }
export function duration(value) { return typeof value === 'number' && Number.isFinite(value) ? `${(value / 1000).toFixed(1)} s` : '—'; }

// This preview is deliberately advisory; publication always uses server validation.
export function previewTree(markdown) {
  const roots = [];
  const stack = [];
  const warnings = [];
  const ids = new Set();
  let heading = false;
  for (const [index, line] of markdown.split(/\r?\n/).entries()) {
    if (!line.trim()) continue;
    if (/^# /.test(line) && !heading && !roots.length) { heading = true; continue; }
    const match = line.trimEnd().match(/^( *)- \[([^\[\]<>]+)\]\((group|page):([a-z][a-z0-9-]{0,95})(?:\/([a-z][a-z0-9-]{0,95}))?\)$/);
    if (!match) { warnings.push(`Line ${index + 1}: expected - [Label](group:id) or - [Label](page:location-id/content-id).`); continue; }
    const [, spaces, label, kind, id, content] = match;
    const indent = spaces.length;
    if (indent % 2 || indent > 24 || (!stack.length && indent) || (stack.length && indent > stack.at(-1).indent + 2)) warnings.push(`Line ${index + 1}: use two spaces for each level, without skipping a level (maximum depth 12).`);
    if (label !== label.trim() || [...label].length > 180) warnings.push(`Line ${index + 1}: label must have no surrounding spaces and at most 180 characters.`);
    if ((kind === 'group' && content) || (kind === 'page' && !content)) warnings.push(`Line ${index + 1}: groups use a location ID; pages need both location and content IDs.`);
    if (ids.has(id)) warnings.push(`Line ${index + 1}: duplicate location ID ${id}.`);
    ids.add(id);
    const node = {id, label, content_id: kind === 'page' ? content || '' : '', children: []};
    while (stack.length && stack.at(-1).indent >= indent) stack.pop();
    if (stack.length) stack.at(-1).node.children.push(node); else roots.push(node);
    stack.push({indent, node});
  }
  function check(items) {
    for (const node of items) {
      if (!node.content_id && !node.children.length) warnings.push(`Group “${node.label}” needs at least one child.`);
      check(node.children);
    }
  }
  check(roots);
  if (!roots.length) warnings.push('No tree items found.');
  return {nodes: roots, warnings};
}

export const summaryColumns = [
  ['code', 'Arm'], ['task_id', 'Task'], ['participants', 'Participants'], ['completed', 'Completed'],
  ['assigned', 'Assigned'], ['shown', 'Started'], ['correct', 'Correct'], ['incorrect', 'Incorrect'],
  ['gave_up', 'Can’t find'], ['skipped', 'Skipped'], ['unfinished', 'Unfinished navigation'], ['unreached', 'Not started'],
  ['success_yield', 'Success yield', fraction], ['shown_success_rate', 'Started-task success', fraction],
  ['median_navigation_ms', 'Median uninterrupted navigation', duration], ['interrupted_attempts', 'Interrupted attempts'],
  ['direct_successes', 'Direct successes'], ['backtracks', 'Backtracks'],
];
