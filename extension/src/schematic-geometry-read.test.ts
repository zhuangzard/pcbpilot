/// <reference types="@jlceda/pro-api-types" />
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { schematicGeometryRead, schematicPinSetNoConnect } from './actions';

function geometryStub(t: import('node:test').TestContext) {
	const globals = globalThis as any, previous = globals.eda;
	t.after(() => { globals.eda = previous; });
	const calls: string[] = [];
	const bbox = { minX: 10, minY: 20, maxX: 30, maxY: 40 };
	const api = {
		sch_PrimitiveComponent: {
			getAllPrimitiveId: async (kind: unknown, allPages: boolean) => {
				assert.equal(kind, 'part'); assert.equal(allPages, false);
				return ['part-1', 'part-2'];
			},
			getAllPinsByPrimitiveId: async (id: string) => {
				calls.push(id);
				return [{ getState_PrimitiveId: () => `${id}.1`, getState_PinNumber: () => '1',
					getState_PinName: () => 'P', getState_X: () => 0, getState_Y: () => 25,
					getState_Rotation: () => 0, getState_NoConnected: () => false }];
			},
		},
		sch_PrimitiveAttribute: { getAll: async () => [{ getState_Key: () => 'Designator',
			getState_ParentPrimitiveId: () => 'part-1', getState_Value: () => 'U1' }] },
		sch_Primitive: { getPrimitivesBBox: async (ids: string[]) => { calls.push(`bbox:${ids.join(',')}`); return bbox; } },
		sch_PrimitiveWire: { getAll: async () => [{ getState_PrimitiveId: () => 'wire-1',
			getState_Net: () => 'SIG', getState_Line: () => [[0, 25, -10, 25], [-10, 25, -10, 50]] }] },
	};
	globals.eda = api;
	return { api, calls, bbox };
}

test('geometry read obtains every part and wire without component/library hydration or netlist compilation', async t => {
	const { calls, bbox } = geometryStub(t);
	const { result }: any = await schematicGeometryRead({});
	assert.equal(result.scope, 'active-page-part-geometry');
	assert.equal(result.count, 2);
	assert.deepEqual(calls, ['bbox:part-1', 'part-1', 'bbox:part-2', 'part-2']);
	assert.equal(result.components[0].designator, 'U1');
	assert.deepEqual(result.components[1].bbox, bbox);
	assert.deepEqual(result.components[0].pins[0], { primitiveId: 'part-1.1', pinNumber: '1', pinName: 'P',
		x: 0, y: 25, rotation: 0, noConnected: false, otherProperty: undefined, net: null });
	assert.equal(result.components[1].pinsAvailable, true);
	assert.equal(result.wiresAvailable, true);
	assert.equal(result.wires.length, 2);
	assert.deepEqual(result.wires.map((w: any) => [w.x0, w.y0, w.x1, w.y1]), [[0, 25, -10, 25], [-10, 25, -10, 50]]);
	assert.equal(result.wires[1].primitiveId, 'wire-1');
	assert.equal(result.wires[1].segmentIndex, 1);
	assert.equal(result.wires[1].net, 'SIG');
});

test('geometry read fails when inventory changes or a required API fails; it never retries', async t => {
	const { api } = geometryStub(t);
	let reads = 0;
	api.sch_PrimitiveComponent.getAllPrimitiveId = async () => ++reads === 1 ? ['part-1'] : ['part-2'];
	await assert.rejects(schematicGeometryRead({}), /inventory changed/);
	assert.equal(reads, 2);
	api.sch_PrimitiveComponent.getAllPrimitiveId = async () => ['part-1'];
	let attempts = 0;
	api.sch_PrimitiveComponent.getAllPinsByPrimitiveId = async () => { attempts++; throw new Error('SDK disconnected'); };
	await assert.rejects(schematicGeometryRead({}), /SDK disconnected/);
	assert.equal(attempts, 1);
});

test('geometry read rejects missing geometry and malformed wires instead of a partial success', async t => {
	const { api } = geometryStub(t);
	const bbox = api.sch_Primitive.getPrimitivesBBox;
	api.sch_Primitive.getPrimitivesBBox = async () => undefined as any;
	await assert.rejects(schematicGeometryRead({}), /Geometry unavailable/);
	api.sch_Primitive.getPrimitivesBBox = bbox;
	api.sch_PrimitiveWire.getAll = async () => undefined as any;
	await assert.rejects(schematicGeometryRead({}), /Wire inventory is unavailable/);
});

test('NC lookup refuses ambiguous or off-page designators before fetching or mutating a component', async t => {
	const { api } = geometryStub(t);
	api.sch_PrimitiveComponent.getAllPrimitiveId = async () => ['part-1', 'part-2'];
	api.sch_PrimitiveAttribute.getAll = async () => ['part-1', 'part-2'].map(id => ({
		getState_Key: () => 'Designator', getState_Value: () => 'U1', getState_ParentPrimitiveId: () => id,
	}));
	await assert.rejects(schematicPinSetNoConnect({ designator: 'U1', pins: ['1'] }), /found 2/);
	api.sch_PrimitiveComponent.getAllPrimitiveId = async () => ['other-page-part'];
	await assert.rejects(schematicPinSetNoConnect({ designator: 'U1', pins: ['1'] }), /found 0/);
});

test('NC lookup resolves a same-page multi-unit designator to the one unit that owns the requested pins', async t => {
	const globals = globalThis as any, previous = globals.eda;
	t.after(() => { globals.eda = previous; });
	const unitPins: Record<string, string[]> = { 'unit-a': ['1', '2', '3', '8'], 'unit-b': ['5', '6', '7', '8'] };
	const stored = new Map<string, boolean>();
	const getCalls: string[] = [];
	const doneCalls: string[] = [];
	const pinHandles = (id: string) => unitPins[id].map((number) => {
		let staged = stored.get(`${id}.${number}`) ?? false;
		const pin: any = {
			getState_PinNumber: () => number,
			getState_NoConnected: () => staged,
			setState_NoConnected: (value: boolean) => { staged = value; return pin; },
			done: async () => { stored.set(`${id}.${number}`, staged); doneCalls.push(`${id}.${number}`); return pin; },
		};
		return pin;
	});
	globals.eda = {
		sch_PrimitiveAttribute: { getAll: async () => Object.keys(unitPins).map(id => ({
			getState_Key: () => 'Designator', getState_Value: () => 'U1', getState_ParentPrimitiveId: () => id,
		})) },
		sch_PrimitiveComponent: {
			getAllPrimitiveId: async () => Object.keys(unitPins),
			getAllPinsByPrimitiveId: async (id: string) => pinHandles(id),
			get: async (id: string) => { getCalls.push(id); return { getAllPins: async () => pinHandles(id) }; },
		},
	};

	const res: any = await schematicPinSetNoConnect({ designator: 'U1', pins: ['6', 7] });
	assert.deepEqual(res.result.pins, [{ pin: '6', noConnected: true }, { pin: '7', noConnected: true }]);
	assert.deepEqual(doneCalls, ['unit-b.6', 'unit-b.7']);
	assert.deepEqual([...new Set(getCalls)], ['unit-b'], 'only the owning unit is fetched');

	// Pins spread over two units, and a pin number both units carry, stay
	// ambiguous: refused before any component is fetched or any pin committed.
	getCalls.length = 0; doneCalls.length = 0;
	await assert.rejects(schematicPinSetNoConnect({ designator: 'U1', pins: ['1', '6'] }), /found 2, of which 0 own/);
	await assert.rejects(schematicPinSetNoConnect({ designator: 'U1', pins: ['8'] }), /found 2, of which 2 own/);
	await assert.rejects(schematicPinSetNoConnect({ designator: 'U1', pins: ['99'] }), /found 2, of which 0 own/);
	assert.deepEqual([getCalls, doneCalls], [[], []]);

	// An unreadable pin inventory is an error, never a guess.
	globals.eda.sch_PrimitiveComponent.getAllPinsByPrimitiveId = async () => undefined;
	await assert.rejects(schematicPinSetNoConnect({ designator: 'U1', pins: ['6'] }), /are unavailable/);
	assert.deepEqual(getCalls, []);
});
