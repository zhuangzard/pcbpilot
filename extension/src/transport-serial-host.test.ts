/// <reference types="@jlceda/pro-api-types" />
/**
 * A host that awaits each WebSocket callback before delivering the next frame.
 *
 * Drives the real transport.ts with a fake `eda` global and models the host as
 * "the next frame is delivered only after the previous callback's promise
 * settled". Request A blocks inside its handler behind a gate the test owns;
 * while it is blocked, request B, a heartbeat pong and a daemon ping arrive.
 *
 * Guarded properties:
 *   1. the callback returns while A's handler is still running, so the host can
 *      keep delivering frames (heartbeat frames are handled, the ping is
 *      answered) instead of starving the liveness check;
 *   2. releasing the callback does not parallelise actions: B's handler starts
 *      only after A's handler finished, and the responses carry that order.
 *
 * No timing assumptions: A is released by the test, and everything asserted
 * "while A is blocked" is asserted before the gate opens.
 */

import assert from 'node:assert/strict';
import { test } from 'node:test';

type Frame = { type: string; id?: string; seq?: number; unordered?: boolean };

const sent: Frame[] = [];
let capturedOnMessage: ((event: { data: string }) => unknown) | null = null;

(globalThis as Record<string, unknown>).eda = {
	sys_WebSocket: {
		register(_id: string, _url: string, onMessage: (event: { data: string }) => unknown): void {
			capturedOnMessage = onMessage;
		},
		send(_id: string, data: string): void {
			sent.push(JSON.parse(data) as Frame);
		},
		close(): void { /* noop */ },
	},
	sys_Message: { showToastMessage(): void { /* noop */ } },
	sys_I18n: { text: (s: string) => s },
	sys_Log: { add(): void { /* noop */ } },
	sys_Storage: { getExtensionUserConfig: () => true },
	sys_Environment: { getEditorCurrentVersion: () => '3.2.175-test' },
	dmt_Project: { getCurrentProjectInfo: async (): Promise<never> => { throw new Error('no project'); } },
	dmt_SelectControl: { getCurrentDocumentInfo: async (): Promise<never> => { throw new Error('no doc'); } },
};

const events: string[] = [];
let releaseA: () => void = () => {};
const gateA = new Promise<void>((resolve) => { releaseA = resolve; });

// transport calls `actions_1.runAction(...)` through the exports object, so
// replacing the export swaps the handler while transport itself stays real.
// eslint-disable-next-line @typescript-eslint/no-var-requires
const actions = require('./actions') as { runAction: unknown };
actions.runAction = async (action: string): Promise<{ result: Record<string, unknown> }> => {
	events.push(`enter ${action}`);
	if (action === 'block.a') {
		await gateA;
	}
	events.push(`exit ${action}`);
	return { result: { action } };
};

// eslint-disable-next-line @typescript-eslint/no-var-requires
const transport = require('./transport') as { reconnect: () => void; stop: (showToast?: boolean) => void };

const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));
const flushMicrotasks = async (): Promise<void> => {
	for (let i = 0; i < 50; i++) {
		await Promise.resolve();
	}
};

test('transport: a serial-delivery host still gets its callback back while an action blocks, and actions stay FIFO', async () => {
	transport.reconnect();
	for (let i = 0; i < 100 && !capturedOnMessage; i++) {
		await sleep(20); // registration is delayed by a timer
	}
	assert.ok(capturedOnMessage, 'transport never called eda.sys_WebSocket.register()');
	const onMessage = capturedOnMessage;

	// The host: one frame at a time, the next only after this callback settled.
	// `delivered` reports whether the callback gave control back without the
	// test having to wait on a clock.
	const deliver = async (frame: Record<string, unknown>): Promise<boolean> => {
		let returned = false;
		void Promise.resolve(onMessage({ data: JSON.stringify(frame) })).then(() => { returned = true; });
		await flushMicrotasks();
		return returned;
	};

	assert.equal(await deliver({ type: 'handshake', service: 'pcbpilot' }), true);
	await sleep(50);
	sent.length = 0;
	events.length = 0;

	// A enters its handler and stays there.
	assert.equal(await deliver({ type: 'request', id: 'req-a', action: 'block.a', timeoutMs: 20000 }), true,
		'the callback must return while the action is still running');
	assert.deepEqual(events, ['enter block.a']);

	// B arrives behind it: accepted by the host callback, but not started.
	assert.equal(await deliver({ type: 'request', id: 'req-b', action: 'after.b', timeoutMs: 20000 }), true);
	assert.deepEqual(events, ['enter block.a'], 'B must not start while A is in its handler');

	// Heartbeat traffic is handled while A is still blocked.
	assert.equal(await deliver({ type: 'pong', id: 'hb-1' }), true, 'a heartbeat pong must be consumed at once');
	assert.equal(await deliver({ type: 'ping', id: 'daemon-1' }), true);
	assert.deepEqual(sent.filter((f) => f.type === 'pong').map((f) => f.id), ['daemon-1'],
		'the daemon ping must be answered while A is still blocked');
	assert.deepEqual(events, ['enter block.a']);
	assert.deepEqual(sent.filter((f) => f.type === 'response'), [], 'nothing may be answered before A finishes');

	releaseA();
	for (let i = 0; i < 200 && sent.filter((f) => f.type === 'response').length < 2; i++) {
		await sleep(5);
	}

	assert.deepEqual(events, ['enter block.a', 'exit block.a', 'enter after.b', 'exit after.b'],
		'B starts only after A finished');
	const responses = sent.filter((f) => f.type === 'response');
	assert.deepEqual(responses.map((f) => f.id), ['req-a', 'req-b']);
	assert.deepEqual(responses.map((f) => f.seq), [1, 2]);
	assert.deepEqual(responses.map((f) => f.unordered), [undefined, undefined]);

	transport.stop(false);
});
