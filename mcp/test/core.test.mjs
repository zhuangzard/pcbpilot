import test from 'node:test';
import assert from 'node:assert/strict';
import {
  buildBlocksArgs,
  buildProjectTransferArgs,
  buildCallArgs,
  buildActionCallArgs,
  buildWorkflowArgs,
  filterActions,
  parseOutput,
  toMcpResult,
} from '../src/core.mjs';

test('buildCallArgs pins project/doc and keeps payload structured', () => {
  assert.deepEqual(
    buildCallArgs('pcb.drc.run', {
      project: 'Motor',
      doc: 'PCB1',
      window: 'win-1',
      payload: { rebuild: true },
    }),
    ['--project', 'Motor', '--doc', 'PCB1', 'call', 'pcb.drc.run', '--payload', '{"rebuild":true}', '--window', 'win-1'],
  );
});

test('filterActions applies domain, mutation, and text filters', () => {
  const actions = [
    { name: 'pcb.drc.run', domain: 'pcb', mutates: false, description: 'native check', inputs: [] },
    { name: 'pcb.track.create', domain: 'pcb', mutates: true, description: 'route copper', inputs: ['net'] },
    { name: 'schematic.check', domain: 'schematic', mutates: false, description: 'lint', inputs: [] },
  ];
  assert.deepEqual(filterActions(actions, { domain: 'pcb', mutates: true, search: 'copper' }), [actions[1]]);
});

test('workflow and blocks arguments are shell-free arrays', () => {
  assert.deepEqual(buildWorkflowArgs({ project: 'P', doc: 'PCB1', operation: 'status', reconcile: true }),
    ['--project', 'P', '--doc', 'PCB1', 'workflow', 'status', '--json', '--reconcile']);
  assert.deepEqual(buildWorkflowArgs({ project: 'P', operation: 'confirm', confirmation: 'layout', note: 'reviewed' }),
    ['--project', 'P', 'workflow', 'confirm', 'layout', '--note', 'reviewed']);
  assert.throws(() => buildWorkflowArgs({ project: 'P', operation: 'reset' }), /reset requires/);
  assert.deepEqual(buildBlocksArgs({ operation: 'search', query: 'usb serial' }), ['blocks', 'search', 'usb serial']);
});

test('parseOutput and MCP result preserve structured JSON', () => {
  assert.deepEqual(parseOutput('{"ok":true}'), { ok: true });
  assert.equal(parseOutput('plain'), 'plain');
  const result = toMcpResult({ ok: true, result: { passed: true } });
  assert.deepEqual(result.structuredContent, { passed: true });
  assert.equal(result.isError, false);

  const warned = toMcpResult({ ok: true, result: { passed: true }, stderr: 'staleRisk' });
  assert.deepEqual(warned.structuredContent, { result: { passed: true }, warnings: 'staleRisk' });
});

test('project creation routes to a window without an existing project or document', () => {
  const payload = { friendlyName: 'New project', open: true, teamUuid: 'team-1' };
  assert.deepEqual(
    buildActionCallArgs({ name: 'project.create', mutates: true }, { window: 'win-home', payload }),
    ['call', 'project.create', '--payload', JSON.stringify(payload), '--window', 'win-home'],
  );
});

test('project creation rejects ambiguous or contradictory routing', () => {
  const action = { name: 'project.create', mutates: true };
  for (const window of [undefined, '', '   ']) {
    assert.throws(() => buildActionCallArgs(action, { window }), /requires an explicit window/);
  }
  for (const routing of [{ project: 'Not created yet' }, { doc: 'tab_page1' }]) {
    assert.throws(() => buildActionCallArgs(action, { window: 'win-home', ...routing }), /does not accept project or doc/);
  }
});

test('other mutations still require both existing targets; reads preserve routing', () => {
  for (const name of ['schematic.page.create', 'board.create', 'schematic.component.place', 'pcb.track.create']) {
    const action = { name, mutates: true };
    for (const route of [{}, { project: 'P' }, { doc: 'D' }, { window: 'W' }]) {
      assert.throws(() => buildActionCallArgs(action, route), /requires both project and doc/);
    }
    assert.deepEqual(buildActionCallArgs(action, { project: 'P', doc: 'D' }),
      ['--project', 'P', '--doc', 'D', 'call', name]);
  }
  assert.deepEqual(buildActionCallArgs({ name: 'project.current', mutates: false }, { window: 'W' }),
    ['call', 'project.current', '--window', 'W']);
});

test('project transfer preserves project identity and requires explicit discard acknowledgement', () => {
  const input = { operation: 'open', window: 'w', projectUuid: 'p' };
  assert.throws(() => buildProjectTransferArgs(input), /acknowledge/);
  assert.deepEqual(buildProjectTransferArgs({ ...input, allowDiscardUnsaved: true }), ['project','open','--window','w','--project-uuid','p','--allow-discard-unsaved']);
  assert.deepEqual(buildProjectTransferArgs({ ...input, operation:'export', out:'test project.epro2' }), ['project','export','--window','w','--project-uuid','p','--out','test project.epro2']);
  assert.throws(() => buildProjectTransferArgs({ ...input, operation:'export', out:'wrong.zip' }), /epro2/);
  assert.throws(() => buildProjectTransferArgs({ ...input, project:'wrong', allowDiscardUnsaved:true }), /routing/);
  assert.throws(() => buildProjectTransferArgs({ ...input, window:' ', allowDiscardUnsaved:true }), /window/);
});

test('catalogued project transfers enforce window, identity and acknowledgement', () => {
  for (const name of ['project.open', 'project.export']) {
    const action = { name, mutates: name === 'project.open' };
    const input = { window: 'w', payload: { projectUuid: 'p', allowDiscardUnsaved: true } };
    assert.ok(buildActionCallArgs(action, input).includes(name));
    for (const bad of [{}, { ...input, window: '' }, { ...input, project: 'old' }, { ...input, doc: 'old' }, { window: 'w', payload: {} }]) assert.throws(() => buildActionCallArgs(action, bad));
  }
  assert.throws(() => buildActionCallArgs({ name: 'project.open', mutates: true }, { window: 'w', payload: { projectUuid: 'p', allowDiscardUnsaved: 'true' } }));
});
