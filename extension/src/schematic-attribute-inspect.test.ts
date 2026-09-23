/// <reference types="@jlceda/pro-api-types" />
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { runAction } from './actions';

function attribute(id: string, keyVisible: unknown, valueVisible: unknown, reset?: () => Promise<unknown>): any {
	return {
		getState_PrimitiveId: () => id,
		getState_KeyVisible: () => keyVisible,
		getState_ValueVisible: () => valueVisible,
		toAsync: () => ({ getState_PrimitiveId: () => id, reset: reset ?? (async () => attribute(id, keyVisible, valueVisible)) }),
		done: () => { throw new Error('design write attempted'); },
	};
}

function installHost(bulk: unknown, direct: unknown, afterDocument = 'page-1') {
	const globals = globalThis as any;
	const previousEda = globals.eda;
	const previousTypes = globals.EDMT_EditorDocumentType;
	let documentReads = 0;
	let writes = 0;
	globals.EDMT_EditorDocumentType = { HOME: -1, BLANK: 0, SCHEMATIC_PAGE: 1, PCB: 3 };
	globals.eda = {
		dmt_Project: { getCurrentProjectInfo: async () => ({ uuid: 'project-1' }) },
		dmt_SelectControl: { getCurrentDocumentInfo: async () => ({
			uuid: ++documentReads >= 2 ? afterDocument : 'page-1', tabId: 'tab-1', documentType: 1,
		}) },
		sch_PrimitiveAttribute: {
			getAll: async () => [bulk],
			get: async () => direct,
			modify: async () => { writes++; throw new Error('design write attempted'); },
		},
		sch_Document: { save: async () => { writes++; throw new Error('design write attempted'); } },
		sys_FileManager: { setDocumentSource: async () => { writes++; throw new Error('design write attempted'); } },
	};
	return {
		writes: () => writes,
		restore: () => {
			if (previousEda === undefined) delete globals.eda;
			else globals.eda = previousEda;
			if (previousTypes === undefined) delete globals.EDMT_EditorDocumentType;
			else globals.EDMT_EditorDocumentType = previousTypes;
		},
	};
}

test('attribute inspect distinguishes an official reset value from two undefined getters', async () => {
	const bulk = attribute('attr-1', undefined, undefined);
	let keyVisible: boolean | undefined;
	let valueVisible: null | undefined;
	const direct = {
		getState_PrimitiveId: () => 'attr-1',
		getState_KeyVisible: () => keyVisible,
		getState_ValueVisible: () => valueVisible,
		toAsync: () => ({ getState_PrimitiveId: () => 'attr-1', reset: async () => {
			keyVisible = true;
			valueVisible = null;
			return direct;
		} }),
	};
	const host = installHost(bulk, direct);
	try {
		const result: any = (await runAction('schematic.attribute.inspect', { primitiveId: 'attr-1' })).result;
		assert.deepEqual(result.getAll.KeyVisible, { type: 'undefined', readable: false });
		assert.deepEqual(result.get.KeyVisible, { type: 'undefined', readable: false });
		assert.deepEqual(result.reset.KeyVisible, { type: 'boolean', value: true, readable: true });
		assert.deepEqual(result.reset.ValueVisible, { type: 'null', value: null, readable: true });
		assert.deepEqual(result.identityBefore, result.identityAfter);
		assert.equal(host.writes(), 0);
	}
	finally { host.restore(); }
});

test('attribute inspect reports three unreadable paths without design writes', async () => {
	const host = installHost(attribute('attr-1', undefined, undefined), attribute('attr-1', undefined, undefined));
	try {
		const result: any = (await runAction('schematic.attribute.inspect', { primitiveId: 'attr-1' })).result;
		for (const path of ['getAll', 'get', 'reset']) {
			assert.deepEqual(result[path].KeyVisible, { type: 'undefined', readable: false });
			assert.deepEqual(result[path].ValueVisible, { type: 'undefined', readable: false });
		}
		assert.equal(host.writes(), 0);
	}
	finally { host.restore(); }
});

test('attribute inspect refuses document drift', async () => {
	const host = installHost(attribute('attr-1', false, true), attribute('attr-1', false, true), 'page-2');
	try {
		await assert.rejects(() => runAction('schematic.attribute.inspect', { primitiveId: 'attr-1' }), /identity changed/);
		assert.equal(host.writes(), 0);
	}
	finally { host.restore(); }
});

test('attribute inspect refuses an unrelated direct primitive', async () => {
	const host = installHost(attribute('attr-1', false, true), attribute('attr-2', false, true));
	try {
		await assert.rejects(() => runAction('schematic.attribute.inspect', { primitiveId: 'attr-1' }), /different primitive ID/);
		assert.equal(host.writes(), 0);
	}
	finally { host.restore(); }
});
