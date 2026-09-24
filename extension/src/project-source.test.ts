/// <reference types="@jlceda/pro-api-types" />

import assert from 'node:assert/strict';
import { test } from 'node:test';
import { runAction } from './actions';

test('project source export preserves official archive bytes and checks current project twice', async () => {
	const bytes = Uint8Array.from([0x50, 0x4b, 0x03, 0x04, 0, 255, 42]);
	const calls: unknown[][] = [];
	(globalThis as any).eda = {
		dmt_Project: { getCurrentProjectInfo: async () => { calls.push(['current']); return { uuid: 'project-id' }; } },
		sys_FileManager: { getProjectFile: async (...args: unknown[]) => {
			calls.push(['export', ...args]);
			return new Blob([bytes]);
		} },
	};
	try {
		const got: any = await runAction('project.export_source', { uuid: 'project-id' });
		assert.deepEqual(calls.slice(0, 3), [
			['current'], ['export', 'easyeda-agent-project.epro2', undefined, 'epro2'], ['current'],
		]);
		assert.equal(got.result.size, bytes.length);
		assert.equal(got.result.artifactId, got.artifacts[0].id);
		assert.deepEqual(Buffer.from(got.artifacts[0].inlineBase64, 'base64'), Buffer.from(bytes));
	}
	finally { delete (globalThis as any).eda; }
});

test('project source export rejects identity mismatch before or after getter', async () => {
	for (const current of [['wrong'], ['project-id', 'wrong']]) {
		let reads = 0;
		let exports = 0;
		(globalThis as any).eda = {
			dmt_Project: { getCurrentProjectInfo: async () => ({ uuid: current[reads++] }) },
			sys_FileManager: { getProjectFile: async () => { exports++; return new Blob(['archive']); } },
		};
		try {
			await assert.rejects(
				() => runAction('project.export_source', { uuid: 'project-id' }),
				(err: any) => err.code === 'INVALID_STATE' && /project/i.test(err.message),
			);
			assert.equal(exports, current.length - 1);
		}
		finally { delete (globalThis as any).eda; }
	}
});

test('project source export rejects missing, empty and oversized files before encoding', async () => {
	for (const file of [undefined, new Blob([]), { size: (8 << 20) + 1 }]) {
		(globalThis as any).eda = {
			dmt_Project: { getCurrentProjectInfo: async () => ({ uuid: 'p' }) },
			sys_FileManager: { getProjectFile: async () => file },
		};
		try {
			await assert.rejects(
				() => runAction('project.export_source', { uuid: 'p' }),
				(err: any) => err.code === 'INVALID_STATE' && /transfer limit/.test(err.message),
			);
		}
		finally { delete (globalThis as any).eda; }
	}
});

test('project source export requires expected UUID and official getter', async () => {
	(globalThis as any).eda = { sys_FileManager: {} };
	try {
		await assert.rejects(
			() => runAction('project.export_source', {}),
			(err: any) => err.code === 'MISSING_PAYLOAD_FIELD',
		);
		await assert.rejects(
			() => runAction('project.export_source', { uuid: 'p' }),
			(err: any) => err.code === 'EDA_API_UNAVAILABLE',
		);
	}
	finally { delete (globalThis as any).eda; }
});
