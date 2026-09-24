/// <reference types="@jlceda/pro-api-types" />

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { runAction } from './actions';

test('symbol source export preserves official archive bytes and exact identity', async () => {
	const bytes = Uint8Array.from([0x50, 0x4b, 0x03, 0x04, 0, 255, 42]);
	let received: unknown[] = [];
	(globalThis as any).eda = {
		sys_FileManager: { getSymbolFileBySymbolUuid: async (...args: unknown[]) => {
			received = args;
			return new Blob([bytes]);
		} },
	};
	try {
		const got: any = await runAction('library.symbol.export_source', { uuid: 'symbol-id', libraryUuid: 'library-id' });
		assert.deepEqual(received, ['symbol-id', 'library-id', 'elibz2']);
		assert.equal(got.result.size, bytes.length);
		assert.equal(got.result.artifactId, got.artifacts[0].id);
		assert.deepEqual(Buffer.from(got.artifacts[0].inlineBase64, 'base64'), Buffer.from(bytes));
	}
	finally { delete (globalThis as any).eda; }
});

test('symbol source export rejects missing, empty and oversized files without encoding', async () => {
	for (const file of [undefined, new Blob([]), { size: (8 << 20) + 1 }]) {
		(globalThis as any).eda = { sys_FileManager: { getSymbolFileBySymbolUuid: async () => file } };
		try {
			await assert.rejects(
				() => runAction('library.symbol.export_source', { uuid: 's', libraryUuid: 'l' }),
				(err: any) => err.code === 'INVALID_STATE' && /transfer limit/.test(err.message),
			);
		}
		finally { delete (globalThis as any).eda; }
	}
});

test('symbol source export needs explicit identity and the official getter', async () => {
	(globalThis as any).eda = { sys_FileManager: {} };
	try {
		await assert.rejects(
			() => runAction('library.symbol.export_source', { uuid: 's' }),
			(err: any) => err.code === 'MISSING_PAYLOAD_FIELD',
		);
		await assert.rejects(
			() => runAction('library.symbol.export_source', { uuid: 's', libraryUuid: 'l' }),
			(err: any) => err.code === 'EDA_API_UNAVAILABLE',
		);
	}
	finally { delete (globalThis as any).eda; }
});
