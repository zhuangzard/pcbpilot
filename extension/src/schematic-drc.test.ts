/// <reference types="@jlceda/pro-api-types" />
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { runAction } from './actions';

for (const strict of [false, true]) {
 for (const verbose of [false, true]) {
  test(`DRC preserves host verdict: strict=${strict}, verbose=${verbose}`, async () => {
   const g = globalThis as unknown as { eda: unknown }; const old = g.eda;
   const calls: unknown[][] = [];
   g.eda = { sch_Drc: { check: async (...args: unknown[]) => {
    calls.push(args); return args[2] ? [{ type: 'warn', count: 1 }] : !strict;
   } } };
   try {
    const { result: r } = await runAction('schematic.drc.check', { strict, includeVerboseError: verbose });
    assert.equal(r?.passed, !strict); assert.equal(r?.nativePassed, !strict);
    assert.equal(r?.strict, strict); assert.equal(r?.countsAvailable, verbose);
    assert.equal(r?.detailsAvailable, false);
    if (verbose) { assert.equal((r?.summary as { warn: number }).warn, 1); assert.equal(r?.fatal, 0); }
    else { assert.equal(r?.summary, null); assert.equal(r?.fatal, null); }
    assert.deepEqual(calls, verbose ? [[strict,false,true],[strict,false,false]] : [[strict,false,false]]);
   } finally { g.eda = old; }
  });
 }
}
for (const scenario of ['clean','error','boolean-detail','missing-detail','throw','missing-verdict']) {
 test(`DRC result coverage: ${scenario}`, async () => {
  const g = globalThis as unknown as { eda: unknown }; const old = g.eda;
  g.eda = { sch_Drc: { check: async (_s: boolean, _u: boolean, verbose: boolean) => {
   if (scenario === 'throw') throw Error('host failure');
   if (!verbose) return scenario === 'missing-verdict' ? undefined : scenario !== 'error';
   if (scenario === 'missing-detail') return undefined;
   if (scenario === 'boolean-detail') return true;
   return scenario === 'error' ? [{type:'error',count:2}] : [];
  } } };
  try {
   const op = runAction('schematic.drc.check', {});
   if (['throw','missing-detail','missing-verdict'].includes(scenario)) await assert.rejects(op);
   else {
    const {result:r} = await op;
    assert.equal(r?.passed, scenario !== 'error');
    if (scenario === 'boolean-detail') {assert.equal(r?.countsAvailable,false);assert.equal(r?.summary,null);}
    if (scenario === 'clean') {assert.equal(r?.countsAvailable,true);assert.equal(r?.fatal,0);}
    if (scenario === 'error') {assert.equal(r?.fatal,2);assert.equal(r?.detailsAvailable,false);}
   }
  } finally {g.eda=old;}
 });
}
