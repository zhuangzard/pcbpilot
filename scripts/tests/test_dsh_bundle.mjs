import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath, pathToFileURL } from 'node:url';

// Read the shipped expressions, not a copy of the conversion implementation.
// These double-quoted YAML scalars use the JSON-compatible subset of YAML.
const patch = readFileSync(new URL('../../cordis.patch.yml', import.meta.url), 'utf8');
const expressions = [...patch.matchAll(/^\s+- !!js ("[^\n]+")\s*$/gm)]
  .map((match) => JSON.parse(match[1]));
assert.equal(expressions.length, 2, 'expected MCP args and Skill directory expressions');
const targets = [
  { expression: expressions[0], relative: 'node_modules/pcbpilot-dsh/mcp/src/server.mjs' },
  { expression: expressions[1], relative: 'node_modules/pcbpilot-dsh/.agents/skills/pcbpilot' },
];

// DSH evaluates !!js in with(ctx), without injecting a CommonJS require.
const evaluate = new Function('ctx', 'expression', 'with (ctx) { return eval(expression); }');

const cases = [
  { name: 'Windows drive', windows: true,
    baseUrl: 'file:///C:/Users/xieyu/.dsh/profiles/web/',
    directory: 'C:\\Users\\xieyu\\.dsh\\profiles\\web' },
  { name: 'Windows encoded Unicode, spaces, hash and percent', windows: true,
    baseUrl: 'file:///c:/Users/%E7%94%A8%E6%88%B7%20A%23100%25/.dsh/profiles/web/',
    directory: 'c:\\Users\\用户 A#100%\\.dsh\\profiles\\web' },
  { name: 'Windows UNC server and share', windows: true,
    baseUrl: 'file://server/DSH%20profiles/web/',
    directory: '\\\\server\\DSH profiles\\web' },
  { name: 'Windows localhost URL', windows: true,
    baseUrl: 'file://localhost/D:/DSH/web/', directory: 'D:\\DSH\\web' },
  { name: 'POSIX', windows: false,
    baseUrl: 'file:///home/user/.dsh/profiles/web/',
    directory: '/home/user/.dsh/profiles/web' },
  { name: 'POSIX encoded Unicode, spaces, hash and percent', windows: false,
    baseUrl: 'file:///Users/%E7%94%A8%E6%88%B7%20A%23100%25/.dsh/profiles/web/',
    directory: '/Users/用户 A#100%/.dsh/profiles/web' },
];

for (const fixture of cases) {
  test(`both DSH bundle paths: ${fixture.name}`, () => {
    // Exercise Node's real platform conversion on any CI host. Only select the
    // target platform; do not implement drive/UNC/percent handling in the test.
    const scopedProcess = {
      getBuiltinModule(name) {
        assert.equal(name, 'node:url');
        return { fileURLToPath: (url) => fileURLToPath(url, { windows: fixture.windows }) };
      },
    };
    for (const target of targets) {
      const actual = evaluate({ baseUrl: fixture.baseUrl, process: scopedProcess }, target.expression);
      const paths = fixture.windows ? path.win32 : path.posix;
      assert.equal(actual, paths.join(fixture.directory, target.relative));
      assert.ok(paths.isAbsolute(actual));
    }
  });
}

test('native profile starts its MCP entry and reads its Skill from encoded paths', (t) => {
  const temporary = mkdtempSync(path.join(tmpdir(), 'easyeda-dsh-'));
  t.after(() => rmSync(temporary, { recursive: true, force: true }));
  // realpath avoids macOS /var versus /private/var aliases during child startup.
  const outside = realpathSync(temporary);
  const profile = path.join(outside, '用户 DSH #50%', 'web');
  const expectedServer = path.join(profile, targets[0].relative);
  const expectedSkill = path.join(profile, targets[1].relative);
  mkdirSync(path.dirname(expectedServer), { recursive: true });
  mkdirSync(expectedSkill, { recursive: true });
  writeFileSync(expectedServer, 'console.log(JSON.stringify({ argv: process.argv.slice(1) }));\n');
  writeFileSync(path.join(expectedSkill, 'SKILL.md'), '---\nname: pcbpilot\n---\n');
  const ctx = { baseUrl: pathToFileURL(profile + path.sep).href };
  const server = evaluate(ctx, targets[0].expression);
  const skill = evaluate(ctx, targets[1].expression);
  assert.equal(server, expectedServer);
  assert.equal(skill, expectedSkill);
  const child = spawnSync(process.execPath, [server, 'argument with space'], {
    cwd: outside, encoding: 'utf8', timeout: 10_000,
  });
  assert.ifError(child.error);
  assert.equal(child.status, 0, child.stderr);
  assert.deepEqual(JSON.parse(child.stdout), { argv: [server, 'argument with space'] });
  assert.match(readFileSync(path.join(skill, 'SKILL.md'), 'utf8'), /name: pcbpilot/);
});
