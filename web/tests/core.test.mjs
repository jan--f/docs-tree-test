import test from 'node:test';
import assert from 'node:assert/strict';
import {reconcileEvents, replayNavigation, indexTree, SequenceConflict, ackEvents, previewTree, summaryColumns, fraction, duration} from '../assets/core.mjs';

function event(seq, type = 'enter', extra = {}) {
  return {id: `event-${seq}`, seq, type, epoch: 'first-page', elapsed_ms: seq * 100, visible: true, ...extra};
}

test('lost checkpoint acknowledgment reconciles identical persisted events without renumbering', () => {
  const first = event(1, 'tree_shown');
  const second = event(2, 'enter', {node_id: 'group'});
  const third = event(3, 'select', {node_id: 'page'});
  const restored = reconcileEvents([first, second], [first, second, third], 3);
  assert.deepEqual(restored.pending, [third]);
  assert.deepEqual(restored.events, [first, second, third]);
  assert.equal(restored.nextSeq, 4);
});

test('conflicting writers cannot overwrite an event, reuse its identity, or fill a sequence gap', () => {
  const first = event(1, 'tree_shown');
  const second = event(2, 'enter', {node_id: 'one'});
  for (const pending of [
    [{...second, node_id: 'two'}],
    [{...second, visible: false}],
    [{...second, seq: 3}],
    [event(4)],
    [event(3), {...event(4), id: 'event-3'}],
  ]) assert.throws(() => reconcileEvents([first, second], pending, 3), SequenceConflict);
  const terminal = event(3, 'submit', {outcome: 'gave_up'});
  assert.throws(() => reconcileEvents([first, second, terminal], [{...terminal, outcome: 'skipped'}], 4), SequenceConflict);
});

test('an acknowledged outbox event must also exist in authoritative server events', () => {
  assert.throws(() => reconcileEvents([event(1, 'tree_shown')], [event(2)], 3), SequenceConflict);
});

test('checkpoint acknowledgment preserves actions queued during the request', () => {
  const pending = [event(1), event(2), event(3)];
  assert.deepEqual(ackEvents(pending, 3), [event(3)]);
  assert.throws(() => ackEvents(pending.slice(0, 2), 4), SequenceConflict);
  assert.throws(() => ackEvents(pending, null), SequenceConflict);
});

test('reload restores page-with-children navigation and selection across page epochs', () => {
  const tree = [{id: 'group', label: 'Guides', selectable: false, children: [{id: 'page', label: 'Set up', selectable: true, children: [{id: 'nested', label: 'Invite', selectable: true, children: []}]}]}];
  const nodes = indexTree(tree);
  const events = [event(1, 'tree_shown'), event(2, 'enter', {node_id: 'group'}), event(3, 'enter', {node_id: 'page'}), event(4, 'visibility_hidden'), event(5, 'resume', {node_id: 'page', epoch: 'reloaded-page', elapsed_ms: 2}), event(6, 'select', {node_id: 'nested', epoch: 'reloaded-page', elapsed_ms: 8})];
  assert.deepEqual(replayNavigation(events, nodes), {currentNodeId: 'page', selectedId: 'nested'});
  assert.deepEqual(replayNavigation([...events, event(7, 'back', {node_id: 'group'})], nodes), {currentNodeId: 'group', selectedId: null});
  assert.deepEqual(replayNavigation([...events, event(7, 'root')], nodes), {currentNodeId: null, selectedId: null});
});

test('preview recognizes actual page/content grammar, duplicate content placements, and page children', () => {
  const source = '# Navigation\n- [Guides](group:guides)\n  - [Access](page:access-guide/access)\n    - [Invite](page:invite-page/invite)\n- [Access again](page:access-shortcut/access)\n';
  const result = previewTree(source);
  assert.deepEqual(result.warnings, []);
  assert.equal(result.nodes[0].content_id, '');
  assert.equal(result.nodes[0].children[0].content_id, 'access');
  assert.equal(result.nodes[0].children[0].children[0].content_id, 'invite');
  assert.equal(result.nodes[1].content_id, 'access');
});

test('preview surfaces malformed and HTML-bearing input instead of rendering HTML', () => {
  const result = previewTree('- [Bad <img src=x>](page:a/b)\n- [Group](group:group)\n   - [Page](page:page/content)\n- [Missing content](page:page)');
  assert.ok(result.warnings.some(message => message.includes('expected')));
  assert.ok(result.warnings.some(message => message.includes('two spaces')));
  assert.ok(result.warnings.some(message => message.includes('duplicate')));
  assert.ok(result.warnings.some(message => message.includes('both location and content')));
  assert.equal(result.nodes.some(node => node.label.includes('<img')), false);
});

test('summary rendering covers exact contract keys and null timings', () => {
  assert.deepEqual(summaryColumns.map(([key]) => key), ['code', 'task_id', 'participants', 'completed', 'assigned', 'shown', 'correct', 'incorrect', 'gave_up', 'skipped', 'unfinished', 'unreached', 'success_yield', 'shown_success_rate', 'median_navigation_ms', 'interrupted_attempts', 'direct_successes', 'backtracks']);
  assert.equal(summaryColumns.find(([key]) => key === 'shown')[1], 'Started');
  assert.equal(summaryColumns.find(([key]) => key === 'shown_success_rate')[1], 'Started-task success');
  assert.equal(fraction(0.25), '25.0%');
  assert.equal(fraction(0), '0.0%');
  assert.equal(duration(null), '—');
  assert.equal(duration(1234), '1.2 s');
});
