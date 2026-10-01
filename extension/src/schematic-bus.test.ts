/// <reference types="@jlceda/pro-api-types" />
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { runAction } from './actions';
import { sameBusLine, validateBusLine } from './schematic-bus';

function withEda(mock: Record<string, unknown>, run: () => Promise<void>): Promise<void> {
	(globalThis as any).eda = mock;
	return run().finally(() => { delete (globalThis as any).eda; });
}

function busHost(opts: { shiftLine?: boolean; noReturn?: boolean; keepOnDelete?: boolean; getAllNotArray?: boolean } = {}) {
	const store = new Map<string, any>();
	const calls: any[] = [];
	const prim = (b: any) => ({
		getState_PrimitiveId: () => b.id,
		getState_BusName: () => b.name,
		getState_Line: () => (opts.shiftLine ? (Array.isArray(b.line[0]) ? b.line : b.line.map((v: number) => v + 5)) : b.line),
		getState_Color: () => b.color,
		getState_LineWidth: () => b.width,
		getState_LineType: () => null,
	});
	return {
		calls,
		store,
		mock: {
			sch_PrimitiveBus: {
				getAll: async () => (opts.getAllNotArray ? undefined : [...store.values()].map(prim)),
				create: async (name: string, line: any, color: any, width: any, type: any) => {
					calls.push({ op: 'create', name, line, color, width, type });
					if (opts.noReturn) return undefined;
					const b = { id: `b${store.size + 1}`, name, line, color, width };
					store.set(b.id, b);
					return prim(b);
				},
				get: async (ids: any) => {
					if (Array.isArray(ids)) return ids.filter(id => store.has(id)).map(id => prim(store.get(id)));
					return store.has(ids) ? prim(store.get(ids)) : undefined;
				},
				delete: async (ids: any) => {
					calls.push({ op: 'delete', ids });
					if (!opts.keepOnDelete) for (const id of [].concat(ids)) store.delete(id);
					return true;
				},
			},
		},
	};
}

test('validateBusLine accepts orthogonal connected polylines and refuses the documented illegal cases', () => {
	assert.deepEqual(validateBusLine([0, 0, 0, 100, 50, 100]), [[0, 0, 0, 100, 50, 100]]);
	assert.equal(validateBusLine([[0, 0, 0, 100], [0, 50, 40, 50]]).length, 2); // T onto the trunk
	for (const bad of [
		[[], [0, 0, 0, 1]], // empty polyline
		[[1], [0, 0, 0, 1]], // x without y
		[[0, 0, -1, 0], [0, 0, 1, 1]], // diagonal
		[[0, 0, -1, 0, -1, 1], [0, 1, 1, 1]], // disjoint
		[[1, 1], [1, 2, 2, 2]], // one-point polyline (host silently drops it)
		[0, 0, 0, 0], // zero length
		[0, 0, Infinity, 0],
		'0,0,1,0',
	]) {
		assert.throws(() => validateBusLine(bad), /PRECONDITION|polyline|line|segment|bus/i, JSON.stringify(bad));
	}
	assert.equal(sameBusLine([0, 0, 0, 10], [[0, 0, 0, 10]]), true);
	assert.equal(sameBusLine([0, 0, 0, 10], [0, 0, 0, 15]), false);
});

test('schematic.bus.create creates one bus and verifies name and path by readback', async () => {
	const h = busHost();
	await withEda(h.mock, async () => {
		const res: any = await runAction('schematic.bus.create', { busName: 'D[0:7]', line: [400, 600, 400, 300], lineWidth: 2 });
		assert.equal(res.result.primitiveId, 'b1');
		assert.equal(res.result.verified, true);
		assert.deepEqual(h.calls[0], { op: 'create', name: 'D[0:7]', line: [400, 600, 400, 300], color: null, width: 2, type: null });
	});
});

test('schematic.bus.create refuses bad input before touching the canvas and never reports an unconfirmed bus as verified', async () => {
	const h = busHost();
	await withEda(h.mock, async () => {
		for (const p of [{ line: [0, 0, 0, 10] }, { busName: ' ', line: [0, 0, 0, 10] }, { busName: 'A', line: [0, 0, 10, 10] },
			{ busName: 'A', line: [0, 0, 0, 10], color: 'red' }, { busName: 'A', line: [0, 0, 0, 10], lineWidth: 11 }]) {
			await assert.rejects(runAction('schematic.bus.create', p as any));
		}
		assert.equal(h.calls.length, 0);
	});
	const shifted = busHost({ shiftLine: true });
	await withEda(shifted.mock, async () => {
		const res: any = await runAction('schematic.bus.create', { busName: 'A', line: [0, 0, 0, 10] });
		assert.equal(res.result.verified, false);
		assert.equal(res.result.partial, true);
		assert.equal(res.result.primitiveId, 'b1');
	});
	const none = busHost({ noReturn: true });
	await withEda(none.mock, async () => {
		await assert.rejects(runAction('schematic.bus.create', { busName: 'A', line: [0, 0, 0, 10] }), /no primitive/);
	});
});

test('schematic.bus.list is fail-closed and schematic.bus.delete reports survivors', async () => {
	const h = busHost();
	await withEda(h.mock, async () => {
		await runAction('schematic.bus.create', { busName: 'A', line: [0, 0, 0, 10] });
		const list: any = await runAction('schematic.bus.list', {});
		assert.equal(list.result.count, 1);
		assert.equal(list.result.buses[0].busName, 'A');
		const del: any = await runAction('schematic.bus.delete', { primitiveIds: ['b1', 'nope'] });
		assert.deepEqual(del.result.deleted, ['b1']);
		assert.deepEqual(del.result.notFound, ['nope']);
		assert.equal(del.result.verified, true);
		await assert.rejects(runAction('schematic.bus.delete', { primitiveIds: ['nope'] }), /none of/);
	});
	const sticky = busHost({ keepOnDelete: true });
	await withEda(sticky.mock, async () => {
		await runAction('schematic.bus.create', { busName: 'A', line: [0, 0, 0, 10] });
		const del: any = await runAction('schematic.bus.delete', { primitiveIds: 'b1' });
		assert.equal(del.result.partial, true);
		assert.deepEqual(del.result.survivedIds, ['b1']);
	});
	const broken = busHost({ getAllNotArray: true });
	await withEda(broken.mock, async () => {
		await assert.rejects(runAction('schematic.bus.list', {}), /not return an array/);
	});
	await withEda({}, async () => {
		await assert.rejects(runAction('schematic.bus.list', {}), /not available/);
	});
});
