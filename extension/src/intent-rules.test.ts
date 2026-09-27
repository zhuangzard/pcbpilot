/// <reference types="@jlceda/pro-api-types" />
// Handlers used by `pcb rules apply` and `sch intent-annotate`.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { runAction } from './actions';

function withEda(mock: Record<string, unknown>, run: () => Promise<void>): Promise<void> {
	(globalThis as any).eda = mock;
	return run().finally(() => { delete (globalThis as any).eda; });
}

function classHost(initial: any[], opts: { drop?: boolean } = {}) {
	const state = { classes: structuredClone(initial), adds: 0 };
	return {
		state,
		mock: {
			pcb_Net: { getAllNetsName: async () => ['+5V', '+3V3', 'VBUS', 'GND', 'LED'] },
			pcb_Drc: {
				getAllNetClasses: async () => structuredClone(state.classes),
				addNetToNetClass: async (name: string, nets: string[]) => {
					state.adds++;
					if (!opts.drop) state.classes.find((c: any) => c.name === name).nets.push(...nets);
					return true;
				},
			},
		},
	};
}

test('pcb.net_class.add_nets adds only missing members and verifies by readback', async () => {
	const h = classHost([{ name: 'POWER', nets: ['+5V', 'LED'] }]);
	await withEda(h.mock, async () => {
		const res: any = await runAction('pcb.net_class.add_nets', { name: 'POWER', nets: ['+5V', '+3V3', 'VBUS'] });
		assert.deepEqual(res.result.added, ['+3V3', 'VBUS']);
		assert.deepEqual(res.result.alreadyMembers, ['+5V']);
		assert.equal(res.result.verified, true);
		assert.deepEqual(h.state.classes[0].nets, ['+5V', 'LED', '+3V3', 'VBUS']); // extra LED preserved
		const again: any = await runAction('pcb.net_class.add_nets', { name: 'POWER', nets: ['+3V3'] });
		assert.equal(again.result.verified, true);
		assert.equal(h.state.adds, 1);
	});
});

test('pcb.net_class.add_nets refuses missing class, foreign nets and nets owned by another class before writing', async () => {
	const h = classHost([{ name: 'POWER', nets: ['+5V'] }, { name: 'GND', nets: ['GND'] }]);
	await withEda(h.mock, async () => {
		await assert.rejects(runAction('pcb.net_class.add_nets', { name: 'NOPE', nets: ['+3V3'] }), /does not exist/);
		await assert.rejects(runAction('pcb.net_class.add_nets', { name: 'POWER', nets: ['NOT_ON_BOARD'] }), /not on this PCB/);
		await assert.rejects(runAction('pcb.net_class.add_nets', { name: 'POWER', nets: ['GND'] }), /another class/);
		assert.equal(h.state.adds, 0);
	});
});

test('pcb.net_class.add_nets reports partial when the host drops the write', async () => {
	const h = classHost([{ name: 'POWER', nets: ['+5V'] }], { drop: true });
	await withEda(h.mock, async () => {
		const res: any = await runAction('pcb.net_class.add_nets', { name: 'POWER', nets: ['+3V3'] });
		assert.equal(res.result.verified, false);
		assert.equal(res.result.partial, true);
		assert.deepEqual(res.result.notApplied, ['+3V3']);
	});
});

function textHost(opts: { shift?: number; noReturn?: boolean } = {}) {
	const store = new Map<string, any>();
	const calls: any[] = [];
	const prim = (t: any) => ({
		getState_PrimitiveId: () => t.id, getState_Content: () => t.content,
		getState_X: () => t.x, getState_Y: () => t.y + (opts.shift ?? 0), getState_FontSize: () => t.fontSize,
	});
	return {
		calls, store,
		mock: {
			sch_PrimitiveText: {
				create: async (x: number, y: number, content: string, rotation: number, color: any, font: any, fontSize: any) => {
					calls.push({ x, y, content, rotation, color, font, fontSize });
					if (opts.noReturn) return undefined;
					const t = { id: `t${store.size + 1}`, x, y, content, fontSize };
					store.set(t.id, t);
					return prim(t);
				},
				get: async (id: string) => (store.has(id) ? prim(store.get(id)) : undefined),
			},
		},
	};
}

test('schematic.text.create creates one text and verifies content and position', async () => {
	const h = textHost();
	await withEda(h.mock, async () => {
		const res: any = await runAction('schematic.text.create', { x: 100, y: 700, content: '[pcbpilot:intent] header', fontSize: 7, color: '#336699' });
		assert.equal(res.result.primitiveId, 't1');
		assert.equal(res.result.verified, true);
		assert.deepEqual(h.calls[0], { x: 100, y: 700, content: '[pcbpilot:intent] header', rotation: 0, color: '#336699', font: null, fontSize: 7 });
	});
});

test('schematic.text.create validates input and never reports an unconfirmed text as verified', async () => {
	const bad = textHost();
	await withEda(bad.mock, async () => {
		for (const payload of [{ x: 1, y: 2 }, { x: 'a', y: 2, content: 'c' }, { x: 1, y: 2, content: 'c', fontSize: -1 }, { x: 1, y: 2, content: 'c', color: 'red' }, { x: Infinity, y: 2, content: 'c' }]) {
			await assert.rejects(runAction('schematic.text.create', payload as any));
		}
		assert.equal(bad.calls.length, 0);
	});
	const shifted = textHost({ shift: 5 });
	await withEda(shifted.mock, async () => {
		const res: any = await runAction('schematic.text.create', { x: 1, y: 2, content: 'c' });
		assert.equal(res.result.verified, false);
		assert.equal(res.result.partial, true);
		assert.equal(res.result.primitiveId, 't1');
	});
	const none = textHost({ noReturn: true });
	await withEda(none.mock, async () => {
		await assert.rejects(runAction('schematic.text.create', { x: 1, y: 2, content: 'c' }), /no primitive/);
	});
});
