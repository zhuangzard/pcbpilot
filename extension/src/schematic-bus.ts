/// <reference types="@jlceda/pro-api-types" />
/**
 * Native schematic buses — `eda.sch_PrimitiveBus` (@beta in
 * @jlceda/pro-api-types 0.4.25: create / delete / modify / get / getAll /
 * getAllPrimitiveId). Typed actions:
 *
 *   schematic.bus.list    read-only, active page
 *   schematic.bus.create  one bus from orthogonal, mutually connected polylines;
 *                         readback verifies name + path (partial on mismatch)
 *   schematic.bus.delete  delete by id; readback lists survivors (partial)
 *
 * The extension API has no bus-entry primitive (ESCH_PrimitiveType: Bus and
 * Wire only), so members are tapped with ordinary wires + net labels through
 * the existing typed connect paths. A bus never proves member connectivity:
 * that stays the per-pin netlist and `sch check`.
 *
 * Status: live-unverified on V3 3.2.x and V4 (offline tests only).
 */

import { ActionError, ErrorCodes, type ActionResult } from './protocol';
import { describeThrown, optionalNumber, optionalString, requireStringArray } from './util';

type Payload = Record<string, unknown>;

/** A bus polyline set as the API takes it: one flat polyline or several. */
export type BusLine = Array<number> | Array<Array<number>>;

function refuse(message: string): ActionError {
	return new ActionError(ErrorCodes.PRECONDITION_REFUSED, message);
}

/**
 * Validate a bus line against the documented `create` rules, fail closed:
 * every polyline is ≥2 finite x,y pairs; every segment is horizontal or
 * vertical and non-zero (diagonal is illegal, case 1.3); polylines must touch
 * each other (disjoint polylines fail, case 1.4). A one-point polyline is
 * silently dropped by the host (case 1.5) — refused here so readback can be
 * compared exactly.
 *
 * @param raw - payload `line`
 * @returns the polylines as flat arrays
 */
export function validateBusLine(raw: unknown): Array<Array<number>> {
	if (!Array.isArray(raw) || raw.length === 0) throw refuse('line must be [x1,y1,x2,y2,…] or [[…],[…]]');
	const polylines: Array<Array<number>> = raw.every(v => typeof v === 'number')
		? [raw as Array<number>]
		: (raw as Array<unknown>).map((l, i) => {
				if (!Array.isArray(l) || !l.every(v => typeof v === 'number')) throw refuse(`polyline #${i + 1} must be an array of numbers`);
				return l as Array<number>;
			});
	const pts: Array<Array<[number, number]>> = [];
	polylines.forEach((l, i) => {
		if (l.some(v => !Number.isFinite(v))) throw refuse(`polyline #${i + 1} has a non-finite coordinate`);
		if (l.length % 2 !== 0) throw refuse(`polyline #${i + 1} has an odd number of coordinates`);
		if (l.length < 4) throw refuse(`polyline #${i + 1} needs ≥2 points (a one-point polyline is dropped by the host)`);
		const p: Array<[number, number]> = [];
		for (let k = 0; k + 1 < l.length; k += 2) p.push([l[k], l[k + 1]]);
		for (let k = 1; k < p.length; k++) {
			const [ax, ay] = p[k - 1];
			const [bx, by] = p[k];
			if (ax === bx && ay === by) throw refuse(`polyline #${i + 1} segment ${k} has zero length`);
			if (ax !== bx && ay !== by) throw refuse(`polyline #${i + 1} segment ${k} is diagonal; bus segments must be horizontal or vertical`);
		}
		pts.push(p);
	});
	const onPoly = (q: [number, number], poly: Array<[number, number]>): boolean => {
		for (let k = 1; k < poly.length; k++) {
			const [sx, sy] = poly[k - 1];
			const [ex, ey] = poly[k];
			const inX = q[0] >= Math.min(sx, ex) && q[0] <= Math.max(sx, ex);
			const inY = q[1] >= Math.min(sy, ey) && q[1] <= Math.max(sy, ey);
			if (inX && inY && ((sx === ex && q[0] === sx) || (sy === ey && q[1] === sy))) return true;
		}
		return false;
	};
	const parent = pts.map((_, i) => i);
	const find = (x: number): number => (parent[x] === x ? x : (parent[x] = find(parent[x])));
	for (let i = 0; i < pts.length; i++) {
		for (let j = i + 1; j < pts.length; j++) {
			if (pts[i].some(q => onPoly(q, pts[j])) || pts[j].some(q => onPoly(q, pts[i]))) parent[find(i)] = find(j);
		}
	}
	for (let i = 1; i < pts.length; i++) {
		if (find(i) !== find(0)) throw refuse(`polyline #${i + 1} does not touch the rest of the bus (disjoint polylines are refused by the host)`);
	}
	return polylines;
}

/** Normalize an API line (flat or nested) to nested polylines. */
export function busPolylines(line: unknown): Array<Array<number>> {
	if (!Array.isArray(line)) return [];
	if (line.every(v => typeof v === 'number')) return [line as Array<number>];
	return (line as Array<unknown>).filter(Array.isArray).map(l => (l as Array<unknown>).map(Number)).filter(l => l.length >= 4);
}

/** Exact (0.01) comparison of two polyline sets, order-sensitive. */
export function sameBusLine(a: unknown, b: unknown): boolean {
	const pa = busPolylines(a);
	const pb = busPolylines(b);
	if (pa.length !== pb.length) return false;
	return pa.every((l, i) => l.length === pb[i].length && l.every((v, k) => Math.abs(v - pb[i][k]) < 0.01));
}

type BusPrim = {
	getState_PrimitiveId: () => string;
	getState_BusName: () => string;
	getState_Line: () => BusLine;
	getState_Color?: () => string | null;
	getState_LineWidth?: () => number | null;
	getState_LineType?: () => unknown;
};

function serializeBus(b: BusPrim): Record<string, unknown> {
	return {
		primitiveId: b.getState_PrimitiveId(),
		busName: b.getState_BusName(),
		line: b.getState_Line(),
		color: b.getState_Color?.() ?? null,
		lineWidth: b.getState_LineWidth?.() ?? null,
		lineType: b.getState_LineType?.() ?? null,
	};
}

function busApi(method: 'getAll' | 'create' | 'get' | 'delete'): any {
	const api = (globalThis as any).eda?.sch_PrimitiveBus;
	if (!api || typeof api[method] !== 'function') {
		throw new ActionError(ErrorCodes.EDA_API_UNAVAILABLE, `eda.sch_PrimitiveBus.${method} is not available on this host (bus API is @beta; live-unverified)`);
	}
	return api;
}

/** schematic.bus.list — every bus on the active page (read-only). */
export async function schematicBusList(): Promise<ActionResult> {
	const api = busApi('getAll');
	let buses: unknown;
	try { buses = await api.getAll(); }
	catch (err) { throw new ActionError(ErrorCodes.EDA_CALL_FAILED, 'Failed to list schematic buses.', describeThrown(err)); }
	if (!Array.isArray(buses)) throw new ActionError(ErrorCodes.INVALID_STATE, 'sch_PrimitiveBus.getAll() did not return an array: bus inventory unknown, not empty.');
	const items = (buses as Array<BusPrim>).map(serializeBus);
	return { result: { count: items.length, scope: 'activePage', buses: items } };
}

/** schematic.bus.create — one bus, verified by readback. */
export async function schematicBusCreate(payload: Payload): Promise<ActionResult> {
	const busName = typeof payload.busName === 'string' ? payload.busName.trim() : '';
	if (!busName) throw refuse('busName is required');
	if ([...busName].length > 64) throw refuse('busName longer than 64 characters');
	const polylines = validateBusLine(payload.line);
	const line: BusLine = polylines.length === 1 ? polylines[0] : polylines;
	const color = optionalString(payload, 'color');
	if (color !== undefined && !/^#[0-9a-fA-F]{6}$/.test(color)) throw refuse('color must be #RRGGBB');
	const lineWidth = optionalNumber(payload, 'lineWidth');
	if (lineWidth !== undefined && !(lineWidth >= 1 && lineWidth <= 10)) throw refuse('lineWidth must be 1…10');
	const api = busApi('create');
	let created: BusPrim | undefined;
	try { created = await api.create(busName, line, color ?? null, lineWidth ?? null, null); }
	catch (err) { throw new ActionError(ErrorCodes.EDA_CALL_FAILED, 'Failed to create schematic bus.', describeThrown(err)); }
	const primitiveId = created?.getState_PrimitiveId?.();
	if (!primitiveId) throw new ActionError(ErrorCodes.EDA_CALL_FAILED, 'sch_PrimitiveBus.create returned no primitive; nothing to verify (inspect the page before retrying).');
	let actual: Record<string, unknown> | null = null;
	let readbackError: string | undefined;
	try {
		const b = typeof api.get === 'function' ? await api.get(primitiveId) : undefined;
		if (b) actual = serializeBus(b);
	}
	catch (err) { readbackError = describeThrown(err); }
	const verified = !!actual && actual.busName === busName && sameBusLine(actual.line, line);
	return {
		result: { primitiveId, busName, line, actual, verified, partial: !verified, ...(readbackError ? { readbackError } : {}) },
		...(verified ? {} : { warnings: [`Bus ${primitiveId} was created but readback did not confirm its name/path; it is kept (id returned) — inspect before retrying.`] }),
	};
}

/** schematic.bus.delete — delete by id, survivors reported. */
export async function schematicBusDelete(payload: Payload): Promise<ActionResult> {
	const ids = requireStringArray(payload, 'primitiveIds');
	const api = busApi('delete');
	busApi('get');
	const present = async (): Promise<Array<string>> => {
		const got = await api.get(ids);
		return (Array.isArray(got) ? got : []).map((b: BusPrim) => b.getState_PrimitiveId());
	};
	let before: Array<string>;
	try { before = await present(); }
	catch (err) { throw new ActionError(ErrorCodes.EDA_CALL_FAILED, 'Failed to read buses before delete.', describeThrown(err)); }
	const notFound = ids.filter(id => !before.includes(id));
	if (before.length === 0) throw refuse(`none of the ${ids.length} id(s) is a bus on the active page`);
	try { await api.delete(before); }
	catch (err) { throw new ActionError(ErrorCodes.EDA_CALL_FAILED, 'Failed to delete schematic buses.', describeThrown(err)); }
	let survived: Array<string> = [];
	let readbackError: string | undefined;
	try { survived = await present(); }
	catch (err) { readbackError = describeThrown(err); }
	const verified = !readbackError && survived.length === 0;
	return {
		result: {
			deleted: before.filter(id => !survived.includes(id)),
			notFound,
			survivedIds: survived,
			verified,
			partial: !verified,
			...(readbackError ? { readbackError } : {}),
		},
		...(verified ? {} : { warnings: ['Bus delete not confirmed by readback; survivors listed — inspect before retrying.'] }),
	};
}
