/** Parameter edits against the observed Web 3.2.203 rule schema. No EDA writes. */
import { ActionError, ErrorCodes } from './protocol';
import { exactJSON } from './preserve-instance';

type Obj = Record<string, any>;
const fail = (message: string): never => { throw new ActionError(ErrorCodes.PRECONDITION_REFUSED, `pcb config: ${message}`); };
const object = (v: unknown, path: string): Obj => {
	if (!v || typeof v !== 'object' || Array.isArray(v)) fail(`${path} is unavailable or has an unsupported schema`);
	return v as Obj;
};
const own = (o: Obj, key: string): any => Object.hasOwn(o, key) ? o[key] : undefined;
const name = (v: unknown, field: string): string => {
	if (typeof v !== 'string' || !v.trim() || ['__proto__', 'constructor', 'prototype'].includes(v)) fail(`${field} must be a non-empty rule/class name`);
	return v as string;
};
const positive = (v: unknown, field: string): number => {
	if (typeof v !== 'number' || !Number.isFinite(v) || v <= 0) fail(`${field} must be a finite positive number`);
	return v as number;
};
const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value));

/**
 * The host stores every rule length as mil rounded to 4 decimals, whatever the
 * displayed unit (#258: an untouched 0.3 mm hole spacing reads back
 * 0.2999994 mm = 11.811 mil the first time a preset becomes custom; 17/19 live
 * mm values on a custom rule set sit exactly on that grid). This maps a value
 * onto that grid — the host's own canonical form, not a tolerance.
 */
export function pcbRuleCanonicalLength(value: number, unit: 'mm' | 'mil'): number {
	const mil = unit === 'mm' ? value / 0.0254 : value;
	const rounded = Math.round(mil * 1e4) / 1e4;
	return unit === 'mm' ? rounded * 0.0254 : rounded;
}

/** Host mm/raw conversions may shift the last IEEE-754 bits. This is NOT a
 * design tolerance: only relative machine roundoff is allowed, even near zero,
 * after both sides are put in the host's canonical length form when the
 * enclosing rule declares its unit. All keys, arrays, units and nonnumeric
 * fields must still match exactly. */
export function pcbRulesEqual(a: unknown, b: unknown, unit?: 'mm' | 'mil'): boolean {
	if (typeof a === 'number' && typeof b === 'number') {
		if (!Number.isFinite(a) || !Number.isFinite(b)) return false;
		const x = unit ? pcbRuleCanonicalLength(a, unit) : a, y = unit ? pcbRuleCanonicalLength(b, unit) : b;
		return x === y || Math.abs(x - y) <= 8 * Number.EPSILON * Math.max(Math.abs(x), Math.abs(y));
	}
	if (a === b) return true;
	if (!a || !b || typeof a !== 'object' || typeof b !== 'object' || Array.isArray(a) !== Array.isArray(b)) return false;
	if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((v, i) => pcbRulesEqual(v, b[i], unit));
	const keys = Object.keys(a);
	const own = (a as Obj).unit;
	const scope = own === 'mm' || own === 'mil' ? own : unit;
	return keys.length === Object.keys(b).length && keys.every(k => Object.hasOwn(b, k) && pcbRulesEqual((a as Obj)[k], (b as Obj)[k], scope));
}

// Web 3.2.203 reads {name, config}, but overwrite accepts only bare config.
// Older hosts may already return the bare object. Never unwrap malformed data.
export function barePcbRuleConfiguration(value: unknown): Obj | null {
	if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
	const r = value as Obj;
	if (Object.hasOwn(r, 'config')) return r.config && typeof r.config === 'object' && !Array.isArray(r.config) ? r.config : null;
	return r;
}

export function planPcbConfig(before: Obj, payload: Obj): { ruleConfiguration: Obj; netRules?: Obj[]; changes: Obj[] } {
	const fields: Record<string, string[]> = {
		clearance: ['name', 'trackToTrack'],
		track: ['name', 'copyFrom', 'min', 'default', 'max'],
		via: ['name', 'minOuter', 'defaultOuter', 'maxOuter', 'minHole', 'defaultHole', 'maxHole'],
		bind: ['netClass', 'trackRule'],
	};
	const kind = String(payload.kind);
	if (!Object.hasOwn(fields, kind)) fail('kind must be clearance, track, via or bind');
	for (const key of Object.keys(payload)) {
		if (!['kind', 'unit', 'dryRun', ...fields[kind]].includes(key)) fail(`unknown ${kind} field ${key}`);
	}
	if (payload.dryRun !== undefined && typeof payload.dryRun !== 'boolean') fail('dryRun must be boolean');
	const inputUnit = payload.unit ?? 'mil';
	if (inputUnit !== 'mil' && inputUnit !== 'mm') fail('unit must be mil or mm');
	const rules = barePcbRuleConfiguration(before.ruleConfiguration);
	if (!rules) fail('current rules are unavailable');
	const ruleConfiguration = clone(rules!);
	const changes: Obj[] = [];
	const group = (section: string, category: string): Obj => object(own(object(own(ruleConfiguration, section), section), category), `${section}.${category}`);
	const change = (target: Obj | any[], key: string | number, value: any, path: string, unit?: string) => {
		const old = (target as Obj)[key];
		if (pcbRulesEqual(old, value, unit === 'mm' || unit === 'mil' ? unit : undefined)) return;
		changes.push({ path, before: old ?? null, after: value, ...(unit ? { unit } : {}) });
		(target as Obj)[key] = value;
	};
	const convert = (v: unknown, rule: Obj, field: string): number => {
		const n = positive(v, field);
		if (rule.unit !== 'mm' && rule.unit !== 'mil') fail(`${field}: stored unit is unknown (${String(rule.unit)})`);
		return positive(inputUnit === rule.unit ? n : inputUnit === 'mil' ? n * 0.0254 : n / 0.0254, `${field} after unit conversion`);
	};
	const range = (min: unknown, def: unknown, max: unknown, path: string) => {
		const lo = positive(min, `${path}.min`), mid = positive(def, `${path}.default`);
		if (max !== null) positive(max, `${path}.max`);
		if (lo > mid + 1e-10 || (max !== null && mid > (max as number) + 1e-10)) fail(`${path}: require min <= default <= max; supply all needed values explicitly`);
	};
	let netRules: Obj[] | undefined;
	if (kind === 'bind') {
		const className = name(payload.netClass, 'netClass'), trackRule = name(payload.trackRule, 'trackRule');
		object(own(group('Physics', 'Track'), trackRule), `Track.${trackRule}`);
		if (!Array.isArray(before.classes) || !Array.isArray(before.netRules)) fail('net classes / net rules are unavailable');
		const classes = before.classes.filter((c: Obj) => c.name === className);
		if (classes.length !== 1 || !Array.isArray(classes[0].nets) || !classes[0].nets.length) fail(`create a non-empty ${className} with pcb net-class create first`);
		const members = classes[0].nets;
		if (members.some((v: unknown) => typeof v !== 'string' || !v) || new Set(members).size !== members.length) fail('invalid or duplicate class members');
		netRules = clone(before.netRules);
		const entries = netRules!.filter(r => r.type === 'netClass' && r.name === className);
		if (entries.length !== 1 || !Array.isArray(entries[0].sub)) fail(`missing or ambiguous netRules for ${className}`);
		const entry = entries[0];
		const children = entry.sub as Obj[];
		if (children.some(c => c.type !== 'net') || exactJSON(children.map(c => c.name).sort()) !== exactJSON([...members].sort())) fail('class members and netRules children differ; read fresh state before binding');
		for (const r of [entry, ...children]) {
			if (typeof r.Track !== 'string') fail(`missing Track assignment for ${r.name}`);
			change(r, 'Track', trackRule, `netRules.${className}.${r.type}:${r.name}.Track`);
		}
	} else {
		const ruleName = name(payload.name, 'name');
		const section = kind === 'clearance' ? 'Spacing' : 'Physics';
		const category = { clearance: 'Safe Spacing', track: 'Track', via: 'Via Size' }[kind]!;
		const rulesOfKind = group(section, category);
		const path = `${section}.${category}.${ruleName}`;
		if (payload.copyFrom !== undefined) name(payload.copyFrom, 'copyFrom');
		if (kind === 'track' && !Object.hasOwn(rulesOfKind, ruleName)) {
			const sourceName = name(payload.copyFrom, 'copyFrom (required for a new track rule)');
			const source = object(own(rulesOfKind, sourceName), `Track.${sourceName}`);
			rulesOfKind[ruleName] = { ...clone(source), editName: ruleName, isSetDefault: false };
		}
		const rule = object(own(rulesOfKind, ruleName), path);
		if (kind === 'clearance') {
			const value = convert(payload.trackToTrack, rule, 'trackToTrack');
			if (!Array.isArray(rule.row) || !Array.isArray(rule.column)) fail(`${path}: missing object-pair labels`);
			const row = rule.row.indexOf('Track'), col = rule.column.indexOf('Track');
			if (row < 0 || col < 0 || rule.row.filter((x: unknown) => x === 'Track').length !== 1 || rule.column.filter((x: unknown) => x === 'Track').length !== 1) fail(`${path}: ambiguous Track labels`);
			const tables = object(rule.tables, `${path}.tables`);
			if (!Object.keys(tables).length) fail(`${path}: empty spacing tables`);
			for (const [layer, table] of Object.entries(tables) as [string, Obj][]) {
				if (!Array.isArray(table?.content?.[row]) || typeof table.content[row][col] !== 'number') fail(`${path}: missing Track/Track cell on table ${layer}`);
				change(table.content[row], col, value, `${path}.tables.${layer}.content.${row}.${col}`, rule.unit);
			}
		} else if (kind === 'track') {
			if (!['min', 'default', 'max'].some(k => payload[k] !== undefined)) fail('supply at least one track dimension');
			const data = object(object(rule.form, `${path}.form`).data, `${path}.form.data`);
			if (!Object.keys(data).length) fail(`${path}: empty track tables`);
			for (const [layer, value] of Object.entries(data)) {
				const entry = object(value, `${path}.${layer}`);
				for (const key of ['min', 'default', 'max']) if (payload[key] !== undefined) {
					if (!Object.hasOwn(entry, `${key}Value`)) fail(`${path}: missing ${key}Value`);
					change(entry, `${key}Value`, convert(payload[key], rule, key), `${path}.form.data.${layer}.${key}Value`, rule.unit);
				}
				range(entry.minValue, entry.defaultValue, entry.maxValue, `${path}.${layer}`);
			}
			if (!Object.hasOwn(groupFrom(rules!, section, category), ruleName)) {
				changes.unshift({ path, before: null, after: clone(rule), copiedFrom: payload.copyFrom });
			}
		} else {
			const form = object(rule.form, `${path}.form`);
			const dimensions = { minOuter: 'viaOuterdiameterMin', defaultOuter: 'viaOuterdiameterDefault', maxOuter: 'viaOuterdiameterMax', minHole: 'viaInnerdiameterMin', defaultHole: 'viaInnerdiameterDefault', maxHole: 'viaInnerdiameterMax' };
			if (!Object.keys(dimensions).some(k => payload[k] !== undefined)) fail('supply at least one via dimension');
			for (const [key, field] of Object.entries(dimensions)) if (payload[key] !== undefined) {
				if (!Object.hasOwn(form, field)) fail(`${path}: missing ${field}`);
				change(form, field, convert(payload[key], rule, key), `${path}.form.${field}`, rule.unit);
			}
			for (const side of ['Outer', 'Inner']) range(form[`via${side}diameterMin`], form[`via${side}diameterDefault`], form[`via${side}diameterMax`], `${path}.${side}`);
			for (const bound of ['Min', 'Default', 'Max']) {
				const inner = form[`viaInnerdiameter${bound}`], outer = form[`viaOuterdiameter${bound}`];
				if (inner !== null && outer !== null && inner >= outer) fail(`${path}.${bound}: hole must be smaller than outer diameter`);
			}
		}
	}
	return { ruleConfiguration, ...(netRules ? { netRules } : {}), changes };
}

function groupFrom(rules: Obj, section: string, category: string): Obj {
	return object(own(object(own(rules, section), section), category), `${section}.${category}`);
}
