/// <reference types="@jlceda/pro-api-types" />
import assert from 'node:assert/strict';
import { test } from 'node:test';
import JSZip from 'jszip';
import { runAction } from './actions';
import { readProjectNativeSourceArchive } from './native-footprint-source';
import { projectSchematicAttributeInventory } from './util';

const page = 'page-1';
const head = (uuid = page) => `{"type":"DOCHEAD"}||{"docType":"SCH_PAGE","uuid":"${uuid}"}|\n`;
const attr = (overrides: Record<string, unknown> = {}) =>
	`{"type":"ATTR","id":"attr-1"}||${JSON.stringify({ key: 'Unique ID', value: 'gge1', parentId: 'part-1', keyVisible: null, valueVisible: null, ...overrides })}|\n`;

async function archive(source: string): Promise<Blob> {
	const zip = new JSZip();
	zip.file('project.epru', source);
	return new Blob([await zip.generateAsync({ type: 'arraybuffer', compression: 'DEFLATE' })]);
}

test('native SCH_PAGE attributes recover explicit null visibility from the exact page', async () => {
	const source = head('other-page') + attr({ keyVisible: true }) + head() + attr();
	const inventory = projectSchematicAttributeInventory(await readProjectNativeSourceArchive(await archive(source)), page);
	assert.deepEqual(inventory.get('attr-1'), {
		id: 'attr-1', key: 'Unique ID', value: 'gge1', parentId: 'part-1', keyVisible: null, valueVisible: null,
	});
});

for (const [label, source] of [
	['missing page', head('other-page') + attr()],
	['duplicate page', head() + attr() + head()],
	['duplicate attribute', head() + attr() + attr()],
	['one visibility field', head() + attr({ keyVisible: undefined })],
	['invalid visibility', head() + attr({ keyVisible: 'false' })],
	['malformed row', head() + 'invalid'],
] as const) {
	test(`native SCH_PAGE attribute inventory refuses ${label}`, () => {
		assert.throws(() => projectSchematicAttributeInventory(source, page));
	});
}

function installHost(source: Blob | undefined, mutate?: (state: Record<string, unknown>) => void) {
	const globals = globalThis as any;
	const previousEda = globals.eda;
	const previousTypes = globals.EDMT_EditorDocumentType;
	let exports = 0;
	let deletes = 0;
	const state: Record<string, unknown> = {
		PrimitiveId: 'attr-1', X: 0, Y: 0, Rotation: 0, Color: null, FontName: null,
		FontSize: null, Bold: null, Italic: null, UnderLine: null, AlignMode: null,
		FillColor: null, Key: 'Unique ID', Value: 'gge1', KeyVisible: undefined,
		ValueVisible: undefined, ParentPrimitiveId: 'part-1',
	};
	mutate?.(state);
	const attribute = new Proxy({}, { get: (_target, prop) => {
		if (typeof prop === 'string' && prop.startsWith('getState_')) return () => state[prop.slice(9)];
		return undefined;
	} });
	const empty = () => ({ getAll: async () => [], delete: async () => { deletes++; return true; } });
	globals.EDMT_EditorDocumentType = { HOME: -1, BLANK: 0, SCHEMATIC_PAGE: 1, PCB: 3 };
	globals.eda = {
		dmt_Project: { getCurrentProjectInfo: async () => ({ uuid: 'project-1' }) },
		dmt_SelectControl: { getCurrentDocumentInfo: async () => ({ uuid: page, tabId: 'tab-1', documentType: 1 }) },
		sys_FileManager: source ? { getProjectFile: async () => { exports++; return source; } } : {},
		sch_PrimitiveComponent: empty(), sch_PrimitiveWire: empty(), sch_PrimitiveBus: empty(),
		sch_PrimitiveArc: empty(), sch_PrimitiveCircle: empty(), sch_PrimitiveRectangle: empty(),
		sch_PrimitivePolygon: empty(), sch_PrimitiveText: empty(), sch_PrimitiveObject: empty(),
		sch_PrimitiveAttribute: {
			getAll: async () => [attribute], getAllPrimitiveId: async () => ['attr-1'], get: async () => attribute,
			delete: async () => { deletes++; return true; },
		},
	};
	return {
		exports: () => exports,
		deletes: () => deletes,
		restore: () => {
			if (previousEda === undefined) delete globals.eda; else globals.eda = previousEda;
			if (previousTypes === undefined) delete globals.EDMT_EditorDocumentType; else globals.EDMT_EditorDocumentType = previousTypes;
		},
	};
}

test('page primitive read uses exact native nulls only for two unreadable visibility getters', async () => {
	const host = installHost(await archive(head() + attr()));
	try {
		const response: any = await runAction('schematic.components.list', { includePagePrimitives: true });
		const actual = response.result.pagePrimitives.attributes[0];
		assert.equal(actual.primitiveId, 'attr-1');
		assert.equal(actual.KeyVisible, null);
		assert.equal(actual.ValueVisible, null);
		assert.equal(actual.Value, 'gge1');
		assert.equal(host.exports(), 1);
		assert.equal(host.deletes(), 0);
	}
	finally { host.restore(); }
});

for (const [label, source, mutate] of [
	['missing archive', undefined, undefined],
	['different attribute value', head() + attr({ value: 'other' }), undefined],
	['different live parent', head() + attr(), (state: Record<string, unknown>) => { state.ParentPrimitiveId = 'another-part'; }],
	['missing attribute', head(), undefined],
	['native visibility absent', head() + attr({ keyVisible: undefined, valueVisible: undefined }), undefined],
] as const) {
	test(`page primitive read fails closed with ${label}`, async () => {
		const host = installHost(source ? await archive(source) : undefined, mutate);
		try {
			await assert.rejects(() => runAction('schematic.components.list', { includePagePrimitives: true }));
			assert.equal(host.deletes(), 0);
		}
		finally { host.restore(); }
	});
}
