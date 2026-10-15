/// <reference types="@jlceda/pro-api-types" />
import assert from 'node:assert/strict';
import { test } from 'node:test';
import fixture from './testdata/pcb-config-web-3.2.203.json';
import { pcbRuleCanonicalLength, pcbRulesEqual, planPcbConfig } from './pcb-config';
import { runAction } from './actions';

const fresh = (): any => structuredClone(fixture);
const track = { kind: 'track', name: 'PWR', copyFrom: 'copperThickness1oz', min: 8, default: 20 };
const bind = { kind: 'bind', netClass: 'PWR_Class', trackRule: 'PWR' };
const close = (a: number, b: number) => assert.ok(Math.abs(a - b) < 1e-12, `${a} != ${b}`);

test('exam clearance changes only the labelled Track/Track cell and preserves the source', () => {
	const before = fresh(), original = fresh();
	const result = planPcbConfig(before, { kind: 'clearance', name: 'copperThickness1oz', trackToTrack: 6 });
	const expected = fresh().ruleConfiguration;
	expected.Spacing['Safe Spacing'].copperThickness1oz.tables['1'].content[0][0] = 6 * 0.0254;
	assert.deepEqual(result.ruleConfiguration, expected);
	assert.deepEqual(before, original);
	assert.equal(result.changes.length, 1);
	assert.equal(result.changes[0].unit, 'mm');
});

test('signal and copied PWR rules preserve maxima, other rules and default selection', () => {
	const before = fresh();
	const signal = planPcbConfig(before, { kind: 'track', name: 'copperThickness1oz', min: 8, default: 8 });
	before.ruleConfiguration = signal.ruleConfiguration;
	const power = planPcbConfig(before, track);
	const rules = power.ruleConfiguration.Physics.Track;
	close(rules.PWR.form.data['1'].minValue, 0.2032);
	close(rules.PWR.form.data['1'].defaultValue, 0.508);
	assert.equal(rules.PWR.form.data['1'].maxValue, 2.54);
	assert.equal(rules.PWR.isSetDefault, false);
	assert.equal(rules.PWR.editName, 'PWR');
	assert.equal(rules.copperThickness1oz.isSetDefault, true);
	delete rules.PWR;
	assert.deepEqual(power.ruleConfiguration, signal.ruleConfiguration);
});

test('mm input, mil storage, multiple layer entries and existing named rules', () => {
	const before = fresh();
	const rule = before.ruleConfiguration.Physics.Track.copperThickness1oz;
	rule.unit = 'mil';
	rule.form.data = { '1': { minValue: 5, defaultValue: 10, maxValue: 100 }, '2': { minValue: 6, defaultValue: 12, maxValue: null } };
	const result = planPcbConfig(before, { kind: 'track', name: 'copperThickness1oz', unit: 'mm', min: 0.2032, default: 0.508 });
	for (const row of Object.values(result.ruleConfiguration.Physics.Track.copperThickness1oz.form.data) as any[]) {
		close(row.minValue, 8); close(row.defaultValue, 20);
	}
	assert.equal(result.ruleConfiguration.Physics.Track.copperThickness1oz.form.data['2'].maxValue, null);
});

test('via 24/12 mil updates only minimum outer/hole diameter', () => {
	const result = planPcbConfig(fresh(), { kind: 'via', name: 'viaSize', minOuter: 24, minHole: 12 });
	const expected = fresh().ruleConfiguration;
	expected.Physics['Via Size'].viaSize.form.viaOuterdiameterMin = 24 * 0.0254;
	expected.Physics['Via Size'].viaSize.form.viaInnerdiameterMin = 12 * 0.0254;
	assert.deepEqual(result.ruleConfiguration, expected);
});

test('binding patches the parent and every member, preserves unrelated fields and signal rules', () => {
	const before = fresh();
	before.ruleConfiguration = planPcbConfig(before, track).ruleConfiguration;
	const result = planPcbConfig(before, bind);
	const expected = structuredClone(before.netRules);
	expected[0].Track = 'PWR';
	expected[0].sub.forEach((row: any) => { row.Track = 'PWR'; });
	assert.deepEqual(result.netRules, expected);
	assert.deepEqual(result.ruleConfiguration, before.ruleConfiguration);
	assert.equal(result.changes.length, 4);
	assert.deepEqual(before.classes, fresh().classes);
});

test('invalid values, unsupported schemas and incomplete memberships fail before mutation', () => {
	const cases: [any, (state: any) => void][] = [
		[{ ...track, min: 21 }, () => {}],
		[{ ...track, max: 19 }, () => {}],
		[{ ...track, unit: 'inch' }, () => {}],
		[{ ...track, min: NaN }, () => {}],
		[{ ...track, min: Infinity }, () => {}],
		[{ ...track, min: 0 }, () => {}],
		[{ ...track, typo: 8 }, () => {}],
		[{ ...track, dryRun: 'true' }, () => {}],
		[{ ...track, name: '__proto__' }, () => {}],
		[{ kind: 'track', name: 'PWR', min: 8 }, () => {}],
		[track, s => { delete s.ruleConfiguration.Physics.Track.copperThickness1oz.unit; }],
		[track, s => { s.ruleConfiguration.Physics.Track.copperThickness1oz.form.data = {}; }],
		[{ kind: 'via', name: 'viaSize', minOuter: 30 }, () => {}],
		[{ kind: 'via', name: 'viaSize', minHole: 24, defaultHole: 25 }, () => {}],
		[{ kind: 'clearance', name: 'copperThickness1oz', trackToTrack: 6 }, s => { s.ruleConfiguration.Spacing['Safe Spacing'].copperThickness1oz.row = []; }],
		[bind, s => { s.netRules[0].sub.pop(); }],
		[bind, s => { s.classes = []; }],
	];
	for (const [payload, alter] of cases) {
		const before = fresh();
		if (payload.kind === 'bind') before.ruleConfiguration = planPcbConfig(before, track).ruleConfiguration;
		alter(before);
		const original = structuredClone(before);
		assert.throws(() => planPcbConfig(before, payload), /pcb config:/, JSON.stringify(payload));
		assert.deepEqual(before, original);
	}
});

async function withHost(run: (host: any) => Promise<void>) {
	let state = fresh();
	const host: any = { writes: 0, drop: false, readFailure: false, netFailure: false, drift: false, reads: 0 };
	(globalThis as any).eda = { pcb_Drc: {
		getCurrentRuleConfiguration: async () => {
			host.reads++;
			if (host.readFailure && host.writes) throw Error('readback unavailable');
			if (host.drift && host.reads > 1) state.ruleConfiguration.unrelated = 'concurrent edit';
			return { name: 'custom', config: structuredClone(state.ruleConfiguration) };
		},
		getAllNetClasses: async () => structuredClone(state.classes),
		getNetRules: async () => structuredClone(state.netRules),
		overwriteCurrentRuleConfiguration: async (rules: any) => {
			host.writes++;
			if (!host.drop) state.ruleConfiguration = structuredClone(rules);
			if (host.roundoff || host.corrupt) {
				const data = state.ruleConfiguration.Spacing['Safe Spacing'].copperThickness1oz.tables['1'].content;
				// corrupt drift must exceed the host's own 0.0001-mil length grid (~2.5e-6 mm) to be real (#258)
				data[0][0] -= host.corrupt ? 1e-5 : Number.EPSILON * data[0][0];
			}
			return true;
		},
		overwriteNetRules: async (rules: any) => {
			host.writes++;
			if (host.netFailure) { host.netFailure = false; return false; }
			state.netRules = structuredClone(rules); return true;
		},
	} };
	try { await run(host); } finally { delete (globalThis as any).eda; }
}

test('typed config get exports restorable data; dry-run writes nothing; write verifies fresh state', async () => withHost(async host => {
	const snapshot = (await runAction('pcb.config.get', {})).result;
	assert.deepEqual(snapshot?.ruleConfiguration, fresh().ruleConfiguration);
	const preview = (await runAction('pcb.config.set', { ...track, dryRun: true })).result;
	assert.equal(preview?.dryRun, true); assert.equal(host.writes, 0);
	const applied = (await runAction('pcb.config.set', track)).result;
	assert.equal(applied?.verified, true); assert.equal(applied?.partial, false);
	assert.deepEqual(applied?.actual, preview?.requested);
	const writes = host.writes;
	const repeated = (await runAction('pcb.config.set', track)).result;
	assert.equal(repeated?.changed, false); assert.equal(repeated?.verified, true);
	assert.equal(host.writes, writes);
	const linked = (await runAction('pcb.config.set', bind)).result;
	assert.equal(linked?.verified, true);
}));

test('silent dropped write and failed final readback never report success', async () => {
	for (const mode of ['drop', 'readFailure']) await withHost(async host => {
		host[mode] = true;
		const result = (await runAction('pcb.config.set', track)).result;
		assert.equal(result?.verified, false); assert.equal(result?.partial, true);
	});
});

test('concurrent source change refuses overwrite', async () => withHost(async host => {
	host.drift = true;
	await assert.rejects(runAction('pcb.config.set', track), /changed during planning/);
	assert.equal(host.writes, 0);
}));

test('failed net-rule write rolls back and stays unsuccessful', async () => withHost(async host => {
	await runAction('pcb.config.set', track);
	const before = (await runAction('pcb.config.get', {})).result;
	host.netFailure = true;
	const result = (await runAction('pcb.config.set', bind)).result;
	assert.equal(result?.partial, true); assert.equal(result?.verified, false); assert.equal(result?.rolledBack, true);
	assert.deepEqual((await runAction('pcb.config.get', {})).result, before);
}));

test('live host roundoff is accepted but missing fields, unit changes and real numeric drift fail', async () => {
	assert.equal(pcbRulesEqual({ value: 0.1759966 }, { value: 0.17599659999999998 }), true);
	for (const [a, b] of [[0, 1e-30], [1, 1 + 1e-12], [NaN, NaN], [null, 0], [{ value: 1 }, {}], [{ unit: 'mil' }, { unit: 'mm' }], [[1, 2], [2, 1]]]) {
		assert.equal(pcbRulesEqual(a, b), false);
	}
	for (const mode of ['roundoff', 'corrupt']) await withHost(async host => {
		host[mode] = true;
		const payload = { kind: 'clearance', name: 'copperThickness1oz', trackToTrack: 7 };
		const result = (await runAction('pcb.config.set', payload)).result;
		assert.equal(result?.verified, mode === 'roundoff');
		assert.equal(result?.partial, mode !== 'roundoff');
		if (mode === 'roundoff') {
			const writes = host.writes;
			assert.equal((await runAction('pcb.config.set', payload)).result?.changed, false);
			assert.equal(host.writes, writes);
		}
	});
});

test('rule lengths compare in the host canonical form: 0.3 mm read back as 0.2999994 is the same value, real drift is not (#258)', () => {
	assert.equal(pcbRuleCanonicalLength(0.3, 'mm'), 11.811 * 0.0254);
	assert.equal(pcbRulesEqual({ unit: 'mm', hole2Hole: 0.3 }, { unit: 'mm', hole2Hole: 0.29999939999999997 }), true);
	assert.equal(pcbRulesEqual({ unit: 'mm', hole2Hole: 0.3 }, { unit: 'mm', hole2Hole: 0.29999 }), false);
	assert.equal(pcbRulesEqual({ unit: 'mil', w: 11.811 }, { unit: 'mil', w: 11.81100004 }), true);
	assert.equal(pcbRulesEqual({ unit: 'mil', w: 11.811 }, { unit: 'mil', w: 11.812 }), false);
	// No declared unit: plain machine-roundoff comparison, as before.
	assert.equal(pcbRulesEqual({ hole2Hole: 0.3 }, { hole2Hole: 0.29999939999999997 }), false);
});
