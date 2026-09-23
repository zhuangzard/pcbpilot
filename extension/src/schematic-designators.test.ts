/// <reference types="@jlceda/pro-api-types" />
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { schematicDesignatorsList } from './actions';

type FixtureOptions = {
	parts?: Array<{ id: string; ref: string; type?: string }>;
	attrs?: Record<string, Array<{ id: string; parent?: string; key?: string; value?: string; visible?: boolean }>>;
	bbox?: unknown;
	changePage?: boolean;
};

function install(t: { after(fn: () => void): void }, options: FixtureOptions = {}) {
	const globals = globalThis as any, old = globals.eda;
	t.after(() => { globals.eda = old; });
	const parts = options.parts ?? [{ id: 'part-1', ref: 'R1' }, { id: 'sheet', ref: '', type: 'sheet' }];
	const attrs = options.attrs ?? { 'part-1': [{ id: 'attr-1', value: 'R1' }, { id: 'other', key: 'Name', value: 'Long model name' }] };
	const queried: Array<string | undefined> = [];
	let docReads = 0;
	globals.eda = {
		dmt_SelectControl: { getCurrentDocumentInfo: async () => ({ uuid: options.changePage && ++docReads > 1 ? 'other-page' : 'page-1' }) },
		sch_PrimitiveComponent: { getAll: async (_unused: unknown, allPages: boolean) => {
			assert.equal(allPages, false, 'must never silently read all pages');
			return parts.map(p => ({ getState_ComponentType: () => p.type ?? 'part', getState_PrimitiveId: () => p.id, getState_Designator: () => p.ref }));
		} },
		sch_PrimitiveAttribute: { getAll: async (parent?: string) => {
			queried.push(parent);
			return (attrs[parent ?? ''] ?? []).map(a => ({
				getState_Key: () => a.key ?? 'Designator', getState_PrimitiveId: () => a.id,
				getState_ParentPrimitiveId: () => a.parent ?? parent,
				getState_Value: () => a.value, getState_ValueVisible: () => a.visible ?? true,
			}));
		} },
		sch_Primitive: { getPrimitivesBBox: async () => options.bbox === undefined
			? { minX: 10, minY: 20, maxX: 16, maxY: 28 } : options.bbox },
	};
	return queried;
}

test('Designator action measures only current-page parts and excludes other attributes', async t => {
	const queried = install(t);
	const result: any = await schematicDesignatorsList({});
	assert.deepEqual(queried, ['part-1']);
	assert.equal(result.result.documentId, 'page-1');
	assert.equal(result.result.count, 1);
	assert.deepEqual(result.result.designators[0].bbox, { minX: 10, minY: 20, maxX: 16, maxY: 28 });
	assert.equal(result.result.designators[0].parentId, 'part-1');
	assert.equal(result.result.designators[0].value, 'R1');
	assert.match(result.result.designators[0].source, /getPrimitivesBBox/);
});

for (const [name, options] of Object.entries<FixtureOptions>({
	'missing Designator': { attrs: { 'part-1': [] } },
	'duplicate Designator attributes': { attrs: { 'part-1': [{ id: 'a', value: 'R1' }, { id: 'b', value: 'R1' }] } },
	'duplicate part reference': { parts: [{ id: 'a', ref: 'R1' }, { id: 'b', ref: 'R1' }] },
	'hidden Designator': { attrs: { 'part-1': [{ id: 'a', value: 'R1', visible: false }] } },
	'mismatched Designator value': { attrs: { 'part-1': [{ id: 'a', value: 'R2' }] } },
	'wrong parent': { attrs: { 'part-1': [{ id: 'a', parent: 'elsewhere', value: 'R1' }] } },
	'missing bbox': { bbox: null },
	'partial bbox': { bbox: { maxX: 16, maxY: 28 } },
	'zero-width bbox': { bbox: { minX: 10, minY: 20, maxX: 10, maxY: 28 } },
	'nonfinite bbox': { bbox: { minX: Number.NaN, minY: 20, maxX: 16, maxY: 28 } },
	'page changed during measurement': { changePage: true },
})) {
	test(`Designator action fails closed on ${name}`, async t => {
		install(t, options);
		await assert.rejects(schematicDesignatorsList({}));
	});
}
